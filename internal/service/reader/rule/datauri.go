package rule

import (
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
)

// 本文件：data: 地址与「type 声明」——书源用它当参数信封。
//
// 聚合类书源（光遇聚合等）会把上一阶段的结果打包进一个 data: 地址当 URL 用：
//
//	searchUrl → `data:;base64,<base64(参数JSON)>,{"type":"gysearch"}`
//	bookUrl   → `data:;base64,<base64({book_id,sources,…})>,{"type":"gydetail"}`
//	tocUrl / chapterUrl 同理
//
// legado 对这类地址的处理（AnalyzeUrl）：
//  1. getByteArrayIfDataUri()：地址以 data: 开头时本地 base64 解码取字节，不发网络请求；
//  2. getStrResponseAwait()：只要选项里声明了 type（值本身不参与判断），
//     就直接返回 `HexUtil.encodeHexStr(字节)`——即原始字节的十六进制串。
//
// 书源随后用 `java.hexDecodeToString(result)` 把 hex 还原成 JSON 取回参数，
// 再自行发出真正的请求。所以「type」不是内容类型，而是一个「请以 hex 返回
// 原始字节」的开关；MeBox 早期把它当成不支持的能力直接拒绝了，导致这类书源
// 在搜索第一步就报「书源 URL 声明了不支持的 type」。

// dataURIPayloadRe 对应 legado AppPattern.dataUriRegex：`^data:.*?;base64,(.*)`。
// 只认带 `;base64,` 的数据地址（legado 也只处理这一种）。
var dataURIPayloadRe = regexp.MustCompile(`(?s)^data:.*?;base64,(.*)$`)

// DecodeDataURI 解析 `data:…;base64,…` 地址，返回解码后的原始字节。
// ok 为 false 表示这不是一个可解析的 base64 数据地址。
// 对应 legado AnalyzeUrl.getByteArrayIfDataUri()。
func DecodeDataURI(raw string) ([]byte, bool) {
	m := dataURIPayloadRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return nil, false
	}
	// base64 允许换行与空白（书源拼接长载荷时常见），逐种变体尝试。
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		}
		return r
	}, m[1])
	if cleaned == "" {
		return []byte{}, true
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(cleaned); err == nil {
			return b, true
		}
	}
	return nil, false
}

// IsDataURI 判断地址是否是 data: 地址。
func IsDataURI(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), "data:")
}

// EncodeRuleBody 按请求声明的响应形态把原始字节转成规则层看到的 body。
//
//	HexBody（选项里声明了 type）→ 原始字节的十六进制串
//	其他                        → 按 charset 解码的文本
//
// 对应 legado getStrResponseAwait 里的两个分支。
func EncodeRuleBody(req *Request, raw []byte, charset string) string {
	if req != nil && req.HexBody {
		return hex.EncodeToString(raw)
	}
	if body, err := DecodeBytes(raw, charset); err == nil {
		return body
	}
	return string(raw)
}
