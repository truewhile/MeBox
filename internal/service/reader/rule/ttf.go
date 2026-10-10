package rule

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// 本文件移植 legado 的 QueryTTF：解析字体（sfnt）的 cmap / glyf / loca / maxp 表，
// 建立「Unicode 码点 → 字形」「字形 → Unicode 码点」两张表，供
// java.queryTTF / java.replaceFont 还原被字体混淆的正文。
//
// 阅读站点的常见套路是：正文用一套打乱过的字体渲染，页面上给出「错误字体」（把
// 每个码点映射到错字形）与「正确字体」（字形到真实码点的映射）。replaceFont 按
// 字形轮廓把错误字体里的字符逐个换成正确字体里同字形的码点，从而还原原文。
//
// 解析器全程做边界检查：字体字节来自书源（不可信），坏字体只应报错，不能 panic。

// queryTTFFont 一个已解析的字体。
type queryTTFFont struct {
	// unicodeToGlyphID 码点 → 字形在 glyf 表里的下标。
	unicodeToGlyphID map[rune]uint16
	// unicodeToGlyph 码点 → 字形轮廓（用于跨字体比较字形）。
	unicodeToGlyph map[rune]string
	// glyphToUnicode 字形轮廓 → 码点（正确字体用来查回真实字符）。
	glyphToUnicode map[string]rune
}

// sfnt 表标签。
var (
	ttfTagCmap = [4]byte{'c', 'm', 'a', 'p'}
	ttfTagGlyf = [4]byte{'g', 'l', 'y', 'f'}
	ttfTagLoca = [4]byte{'l', 'o', 'c', 'a'}
	ttfTagMaxp = [4]byte{'m', 'a', 'x', 'p'}
	ttfTagHead = [4]byte{'h', 'e', 'a', 'd'}
)

// parseQueryTTFFont 解析字体字节。支持 sfnt（TTF/OTF）与 ttc 的第一套字体。
func parseQueryTTFFont(data []byte) (font *queryTTFFont, err error) {
	defer func() {
		if r := recover(); r != nil {
			font, err = nil, fmt.Errorf("字体解析失败: %v", r)
		}
	}()
	if len(data) < 12 {
		return nil, fmt.Errorf("字体数据过短")
	}
	// ttc：取第一套字体的偏移。
	if string(data[:4]) == "ttcf" {
		if len(data) < 16 {
			return nil, fmt.Errorf("ttc 头部不完整")
		}
		off := int(binary.BigEndian.Uint32(data[12:16]))
		if off <= 0 || off >= len(data) {
			return nil, fmt.Errorf("ttc 字体偏移非法")
		}
		data = data[off:]
	}
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	if numTables <= 0 || 12+numTables*16 > len(data) {
		return nil, fmt.Errorf("sfnt 表目录非法")
	}
	tables := map[[4]byte][]byte{}
	for i := 0; i < numTables; i++ {
		rec := data[12+i*16 : 12+i*16+16]
		var tag [4]byte
		copy(tag[:], rec[:4])
		off := int(binary.BigEndian.Uint32(rec[8:12]))
		length := int(binary.BigEndian.Uint32(rec[12:16]))
		if off < 0 || length < 0 || off > len(data) {
			continue
		}
		if off+length > len(data) {
			length = len(data) - off
		}
		tables[tag] = data[off : off+length]
	}

	cmap := tables[ttfTagCmap]
	if cmap == nil {
		return nil, fmt.Errorf("字体缺少 cmap 表")
	}
	mapping, err := parseTTFCmap(cmap)
	if err != nil {
		return nil, err
	}
	f := &queryTTFFont{
		unicodeToGlyphID: mapping,
		unicodeToGlyph:   map[rune]string{},
		glyphToUnicode:   map[string]rune{},
	}

	// 有 glyf + loca + head + maxp 才能算字形轮廓；缺了（如 CFF 字体）只保留码点映射。
	head := tables[ttfTagHead]
	maxp := tables[ttfTagMaxp]
	loca := tables[ttfTagLoca]
	glyf := tables[ttfTagGlyf]
	if head == nil || maxp == nil || loca == nil || glyf == nil || len(head) < 54 {
		return f, nil
	}
	indexToLocFormat := int16(binary.BigEndian.Uint16(head[50:52]))
	numGlyphs := int(binary.BigEndian.Uint16(maxp[4:6]))
	offsets, err := parseTTFLoca(loca, numGlyphs, indexToLocFormat)
	if err != nil {
		return f, nil
	}
	glyphCache := map[uint16]string{}
	for cp, gid := range mapping {
		outline := ttfGlyphOutline(glyf, offsets, gid, glyphCache, 0)
		f.unicodeToGlyph[cp] = outline
		if _, exists := f.glyphToUnicode[outline]; !exists {
			f.glyphToUnicode[outline] = cp
		}
	}
	return f, nil
}

// parseTTFCmap 解析 cmap 表，返回码点 → 字形下标。
// 支持 format 0 / 4 / 6（legado QueryTTF 同样只支持这三种）。
func parseTTFCmap(cmap []byte) (map[rune]uint16, error) {
	if len(cmap) < 4 {
		return nil, fmt.Errorf("cmap 表过短")
	}
	numTables := int(binary.BigEndian.Uint16(cmap[2:4]))
	if 4+numTables*8 > len(cmap) {
		return nil, fmt.Errorf("cmap 子表目录非法")
	}
	out := map[rune]uint16{}
	for i := 0; i < numTables; i++ {
		rec := cmap[4+i*8 : 4+i*8+8]
		off := int(binary.BigEndian.Uint32(rec[4:8]))
		if off < 0 || off+2 > len(cmap) {
			continue
		}
		sub := cmap[off:]
		switch binary.BigEndian.Uint16(sub[0:2]) {
		case 0:
			parseCmapFormat0(sub, out)
		case 4:
			parseCmapFormat4(sub, out)
		case 6:
			parseCmapFormat6(sub, out)
		}
		// 优先保留第一个子表解析到的映射；后续子表只补缺失项。
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cmap 没有可用的 format 0/4/6 子表")
	}
	return out, nil
}

func parseCmapFormat0(sub []byte, out map[rune]uint16) {
	if len(sub) < 262 {
		return
	}
	length := int(binary.BigEndian.Uint16(sub[2:4]))
	if length > len(sub) {
		length = len(sub)
	}
	glyphs := sub[6:min(6+256, length)]
	for i, gid := range glyphs {
		if gid != 0 {
			if _, exists := out[rune(i)]; !exists {
				out[rune(i)] = uint16(gid)
			}
		}
	}
}

func parseCmapFormat4(sub []byte, out map[rune]uint16) {
	if len(sub) < 14 {
		return
	}
	segCountX2 := int(binary.BigEndian.Uint16(sub[6:8]))
	segCount := segCountX2 / 2
	if segCount == 0 {
		return
	}
	endBase := 14
	startBase := endBase + segCountX2 + 2
	deltaBase := startBase + segCountX2
	rangeBase := deltaBase + segCountX2
	if rangeBase+segCountX2 > len(sub) {
		return
	}
	for i := 0; i < segCount; i++ {
		end := int(binary.BigEndian.Uint16(sub[endBase+i*2:]))
		start := int(binary.BigEndian.Uint16(sub[startBase+i*2:]))
		delta := int16(binary.BigEndian.Uint16(sub[deltaBase+i*2:]))
		rangeOffset := int(binary.BigEndian.Uint16(sub[rangeBase+i*2:]))
		if start > end {
			continue
		}
		for cp := start; cp <= end && cp <= 0xFFFF; cp++ {
			if cp == 0xFFFF {
				continue
			}
			var gid uint16
			if rangeOffset == 0 {
				gid = uint16(int(cp) + int(delta))
			} else {
				idx := rangeBase + i*2 + rangeOffset + (cp-start)*2
				if idx+2 > len(sub) {
					continue
				}
				g := binary.BigEndian.Uint16(sub[idx : idx+2])
				if g == 0 {
					continue
				}
				gid = uint16(int(g) + int(delta))
			}
			if gid != 0 {
				if _, exists := out[rune(cp)]; !exists {
					out[rune(cp)] = gid
				}
			}
		}
	}
}

func parseCmapFormat6(sub []byte, out map[rune]uint16) {
	if len(sub) < 10 {
		return
	}
	first := int(binary.BigEndian.Uint16(sub[6:8]))
	count := int(binary.BigEndian.Uint16(sub[8:10]))
	for i := 0; i < count; i++ {
		idx := 10 + i*2
		if idx+2 > len(sub) {
			return
		}
		gid := binary.BigEndian.Uint16(sub[idx : idx+2])
		if gid == 0 {
			continue
		}
		cp := rune(first + i)
		if _, exists := out[cp]; !exists {
			out[cp] = gid
		}
	}
}

// parseTTFLoca 解析 loca 表，返回每个字形的字节区间起止。
func parseTTFLoca(loca []byte, numGlyphs int, indexToLocFormat int16) ([]int, error) {
	if indexToLocFormat == 0 {
		need := (numGlyphs + 1) * 2
		if len(loca) < need {
			return nil, fmt.Errorf("loca 表过短")
		}
		out := make([]int, numGlyphs+1)
		for i := 0; i <= numGlyphs; i++ {
			out[i] = int(binary.BigEndian.Uint16(loca[i*2:])) * 2
		}
		return out, nil
	}
	need := (numGlyphs + 1) * 4
	if len(loca) < need {
		return nil, fmt.Errorf("loca 表过短")
	}
	out := make([]int, numGlyphs+1)
	for i := 0; i <= numGlyphs; i++ {
		out[i] = int(binary.BigEndian.Uint32(loca[i*4:]))
	}
	return out, nil
}

// ttfGlyphOutline 把字形转成轮廓字符串（对应 legado QueryTTF.Glyf.toString）。
// 复合字形递归展开组件，深度上限防自引用。
func ttfGlyphOutline(glyf []byte, offsets []int, gid uint16, cache map[uint16]string, depth int) string {
	if depth > 8 {
		return fmt.Sprintf("glyph%d", gid)
	}
	if v, ok := cache[gid]; ok {
		return v
	}
	if int(gid)+1 >= len(offsets) {
		return fmt.Sprintf("glyph%d", gid)
	}
	start, end := offsets[gid], offsets[gid+1]
	if start < 0 || end > len(glyf) || end <= start {
		// 空字形（如空格）：用下标本身当轮廓，保证不同码点不会互相误判。
		out := fmt.Sprintf("glyph%d", gid)
		cache[gid] = out
		return out
	}
	numberOfContours := int16(binary.BigEndian.Uint16(glyf[start : start+2]))
	if numberOfContours >= 0 {
		out := fmt.Sprintf("simple:%d:%s", numberOfContours, ttfSimpleGlyphPoints(glyf[start:end]))
		cache[gid] = out
		return out
	}
	// 复合字形：逐组件展开。
	var sb strings.Builder
	sb.WriteString("composite")
	pos := start + 10
	for pos+4 <= end {
		flags := binary.BigEndian.Uint16(glyf[pos : pos+2])
		component := binary.BigEndian.Uint16(glyf[pos+2 : pos+4])
		pos += 4
		if flags&0x0001 != 0 { // ARG_1_AND_2_ARE_WORDS
			pos += 4
		} else {
			pos += 2
		}
		switch {
		case flags&0x0008 != 0: // WE_HAVE_A_SCALE
			pos += 2
		case flags&0x0040 != 0: // WE_HAVE_AN_X_AND_Y_SCALE
			pos += 4
		case flags&0x0080 != 0: // WE_HAVE_A_TWO_BY_TWO
			pos += 8
		}
		sb.WriteString("+")
		sb.WriteString(ttfGlyphOutline(glyf, offsets, component, cache, depth+1))
		if flags&0x0020 == 0 { // MORE_COMPONENTS
			break
		}
	}
	out := sb.String()
	cache[gid] = out
	return out
}

// ttfSimpleGlyphPoints 取简单字形的轮廓点（标志与坐标的紧凑编码）。
func ttfSimpleGlyphPoints(data []byte) string {
	if len(data) < 10 {
		return ""
	}
	numberOfContours := int(binary.BigEndian.Uint16(data[0:2]))
	if numberOfContours <= 0 {
		return ""
	}
	endPtsPos := 10
	if endPtsPos+numberOfContours*2+2 > len(data) {
		return ""
	}
	numPoints := int(binary.BigEndian.Uint16(data[endPtsPos+(numberOfContours-1)*2:])) + 1
	if numPoints <= 0 {
		return ""
	}
	pos := endPtsPos + numberOfContours*2 + 2 // + instructionLength
	if pos > len(data) {
		return ""
	}
	instrLen := int(binary.BigEndian.Uint16(data[pos-2 : pos]))
	pos += instrLen
	flags := make([]byte, 0, numPoints)
	for len(flags) < numPoints && pos < len(data) {
		flag := data[pos]
		pos++
		flags = append(flags, flag)
		if flag&0x08 != 0 { // REPEAT
			if pos >= len(data) {
				break
			}
			repeat := int(data[pos])
			pos++
			for i := 0; i < repeat && len(flags) < numPoints; i++ {
				flags = append(flags, flag)
			}
		}
	}
	// 解析 x / y 坐标（与点数等长的增量序列，这里只用于区分字形）。
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("n=%d;", numPoints))
	for _, flag := range flags {
		sb.WriteByte('0' + flag&0x0F)
	}
	sb.WriteString(";")
	// 跳过坐标数据不影响「同名轮廓一致性」的判断：同字形的字体坐标编码一致。
	if pos < len(data) {
		sb.WriteString(fmt.Sprintf("d=%d", len(data)-pos))
	}
	return sb.String()
}
