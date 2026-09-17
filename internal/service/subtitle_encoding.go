package service

import (
	"bytes"
	"encoding/binary"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// 外挂字幕文件的编码归一化。
//
// 字幕文件由字幕组/工具生成，编码并不统一：除 UTF-8 外，中文圈常见 GBK/GB18030，
// 港台常见 Big5，部分 Windows 工具（Aegisub 早期版本、Subtitle Edit 的某些导出）
// 会写成带 BOM 的 UTF-16LE。浏览器 <track> 与 libass（JASSUB）都只按 UTF-8 解析，
// 后端如果原样下发字节，非 UTF-8 的字幕会整篇变成替换字符——表现就是「字幕轨已经
// 选中，但画面上一个字都不显示」。所以外挂字幕在服务端就要统一解码成 UTF-8。
//
// 注意：只用于下发给浏览器/播放器的文本路径。Emby 兼容层的 /subtitles 原始字节
// 接口（ServeRaw）必须保持字节一致，不能经过这里。

// 各编码的 BOM 前缀。
var (
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
	utf16BEBOM = []byte{0xFE, 0xFF}
	utf16LEBOM = []byte{0xFF, 0xFE}
)

// subtitleLegacyEncodings 是无 BOM 时的候选编码，按优先级排列。
// GB18030 是 GBK/GB2312 的超集，中文外挂字幕绝大多数是它，因此排在最前——
// 多个候选都能解出合法字幕时，靠前的优先。
var subtitleLegacyEncodings = []struct {
	name string
	enc  encoding.Encoding
}{
	{"gb18030", simplifiedchinese.GB18030},
	{"big5", traditionalchinese.Big5},
	{"shift_jis", japanese.ShiftJIS},
	{"euc-kr", korean.EUCKR},
}

// subtitleTimecodePattern 匹配 SRT（00:00:01,000 --> ...）与 WebVTT
// （00:00:01.000 --> ...）的时间行。
var subtitleTimecodePattern = regexp.MustCompile(`\d{1,2}:\d{2}:\d{2}[,.]\d{1,3}\s*-->`)

// decodeSubtitleText 把字幕文件字节归一化成 UTF-8 文本。识别不出编码时原样返回，
// 保证不会比「直接透传」更糟。
func decodeSubtitleText(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, utf8BOM):
		return string(raw[len(utf8BOM):])
	case bytes.HasPrefix(raw, utf16LEBOM):
		return decodeUTF16(raw[len(utf16LEBOM):], false)
	case bytes.HasPrefix(raw, utf16BEBOM):
		return decodeUTF16(raw[len(utf16BEBOM):], true)
	}

	// 无 BOM 且是合法 UTF-8：这是绝大多数情况，直接返回。
	if utf8.Valid(raw) {
		return string(raw)
	}

	best := ""
	bestScore := math.MinInt
	consider := func(text string) {
		if !utf8.ValidString(text) || !looksLikeSubtitle(text) {
			return
		}
		score := subtitlePlausibility(text)
		if score > bestScore {
			best, bestScore = text, score
		}
	}

	// 无 BOM 的 UTF-16 少见（多来自手工编辑），但一旦是它，所有单字节编码都会
	// 解出带大量 NUL 的垃圾，因此先用「格式特征」筛掉这些候选。
	consider(decodeUTF16(raw, false))
	consider(decodeUTF16(raw, true))

	for _, candidate := range subtitleLegacyEncodings {
		text, err := candidate.enc.NewDecoder().Bytes(raw)
		if err != nil {
			continue
		}
		consider(string(text))
	}
	if best != "" {
		return best
	}
	return string(raw)
}

// decodeUTF16 按给定字节序把 UTF-16 字节解码为字符串（含代理对）。
func decodeUTF16(raw []byte, bigEndian bool) string {
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		if bigEndian {
			units = append(units, binary.BigEndian.Uint16(raw[i:i+2]))
		} else {
			units = append(units, binary.LittleEndian.Uint16(raw[i:i+2]))
		}
	}
	return string(utf16.Decode(units))
}

// looksLikeSubtitle 判断解码结果是否真的像一份字幕。解码器「没报错」并不代表
// 解对了——很多编码能把任意字节解成合法文本，只有格式特征才能确认。
func looksLikeSubtitle(text string) bool {
	head := text
	if len(head) > 4096 {
		head = head[:4096]
	}
	if strings.Contains(head, "[Script Info]") || strings.Contains(head, "Dialogue:") {
		return true // ASS/SSA
	}
	if strings.HasPrefix(strings.TrimSpace(head), "WEBVTT") {
		return true
	}
	return subtitleTimecodePattern.MatchString(head) // SRT / WebVTT 时间行
}

// subtitlePlausibility 给解码结果打分，返回 -100..100 的可信度：正常文本字符加分，
// 替换字符（解码失败）、控制字符、未分配码位扣分。
//
// 刻意用比例而不是字符数量：单字节回退会把一个双字节字符拆成两个字符，字符多的
// 候选看起来「更正常」，实际是解错了——GBK 中文字幕被当成 Shift_JIS 解，就会得到
// 一串半角片假名，数量比正确结果还多。半角片假名单独扣分正是为了压住这种情况。
func subtitlePlausibility(text string) int {
	total := 0
	weight := 0
	for _, r := range text {
		total++
		switch {
		case r == utf8.RuneError:
			weight -= 8
		case r == '\n' || r == '\r' || r == '\t':
			// 正常换行不计分也不扣分。
		case r < 0x20 || r == 0x7F:
			weight -= 8
		case unicode.Is(unicode.Co, r) || unicode.Is(unicode.Cn, r):
			weight -= 4
		case r >= 0xFF61 && r <= 0xFF9F:
			// 半角片假名：GBK 汉字被按 Shift_JIS 解出来的典型产物。
			weight -= 2
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul),
			unicode.Is(unicode.Latin, r), unicode.IsDigit(r), unicode.IsPunct(r):
			weight++
		case unicode.IsSpace(r):
			// 空格（含全角空格）不参与打分。
		default:
			weight--
		}
	}
	if total == 0 {
		return 0
	}
	return weight * 100 / total
}
