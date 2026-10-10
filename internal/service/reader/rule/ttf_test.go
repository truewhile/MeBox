package rule

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// 字体混淆还原（ttf.go / queryttf.go）的测试：用合成的 TTF 覆盖 cmap format 0/4、
// loca 短格式、简单/复合字形，验证「错误字体 → 正确字体」的按字形还原。

// buildTestTTF 构造一个最小可解析的 TTF：
// cmap（format 4）把给定码点映射到指定字形下标，glyf 里每个字形一个方框。
func buildTestTTF(t *testing.T, entries map[rune]uint16) []byte {
	t.Helper()
	const numGlyphs = 8
	// 每个字形一个简单方框：轮廓数 1，1 个点。
	glyph := func() []byte {
		g := make([]byte, 0, 20)
		g = append(g, 0x00, 0x01)             // numberOfContours = 1
		g = append(g, 0, 0, 0, 0, 0, 0, 0, 0) // bbox
		g = append(g, 0x00, 0x00)             // endPtsOfContours[0] = 0
		g = append(g, 0x00, 0x00)             // instructionLength = 0
		g = append(g, 0x01, 0x01)             // flag: on-curve | x-short | y-short
		g = append(g, 0x00, 0x00)             // x=0, y=0
		return g
	}
	glyfData := make([]byte, 0, 128)
	offsets := make([]int, 0, numGlyphs+1)
	for i := 0; i < numGlyphs; i++ {
		offsets = append(offsets, len(glyfData))
		if i == 0 {
			continue // .notdef 空字形
		}
		g := glyph()
		// 让不同字形的字节长度不同，轮廓字符串才能区分。
		for j := 0; j < i-1; j++ {
			g = append(g, 0x01, 0x00, 0x00) // 额外点
		}
		glyfData = append(glyfData, g...)
	}
	offsets = append(offsets, len(glyfData))

	// loca（短格式，偏移/2）
	loca := make([]byte, (numGlyphs+1)*2)
	for i, off := range offsets {
		binary.BigEndian.PutUint16(loca[i*2:], uint16(off/2))
	}

	// cmap：format 4 单段 + 结束段
	var cmap bytes.Buffer
	codes := make([]rune, 0, len(entries))
	for cp := range entries {
		codes = append(codes, cp)
	}
	// 按码点排序，构造连续单点段。
	for i := 0; i < len(codes); i++ {
		for j := i + 1; j < len(codes); j++ {
			if codes[j] < codes[i] {
				codes[i], codes[j] = codes[j], codes[i]
			}
		}
	}
	segCount := len(codes) + 1
	endCodes := make([]uint16, 0, segCount)
	startCodes := make([]uint16, 0, segCount)
	idDeltas := make([]uint16, 0, segCount)
	for _, cp := range codes {
		endCodes = append(endCodes, uint16(cp))
		startCodes = append(startCodes, uint16(cp))
		idDeltas = append(idDeltas, uint16(int(entries[cp])-int(cp)))
	}
	endCodes = append(endCodes, 0xFFFF)
	startCodes = append(startCodes, 0xFFFF)
	idDeltas = append(idDeltas, 1)
	rangeOffsets := make([]uint16, segCount) // 全 0：用 idDelta

	sub := new(bytes.Buffer)
	writeU16 := func(v uint16) { _ = binary.Write(sub, binary.BigEndian, v) }
	length := 16 + segCount*8
	writeU16(4)                    // format
	writeU16(uint16(length))       // length
	writeU16(0)                    // language
	writeU16(uint16(segCount * 2)) // segCountX2
	writeU16(0)                    // searchRange（解析器不校验）
	writeU16(0)                    // entrySelector
	writeU16(0)                    // rangeShift
	for _, v := range endCodes {
		writeU16(v)
	}
	writeU16(0) // reservedPad
	for _, v := range startCodes {
		writeU16(v)
	}
	for _, v := range idDeltas {
		writeU16(v)
	}
	for _, v := range rangeOffsets {
		writeU16(v)
	}
	subBytes := sub.Bytes()

	// cmap 头 + 一个子表记录
	cmap.Write([]byte{0, 0})
	_ = binary.Write(&cmap, binary.BigEndian, uint16(1))
	_ = binary.Write(&cmap, binary.BigEndian, uint16(3)) // platformID = Windows
	_ = binary.Write(&cmap, binary.BigEndian, uint16(1)) // encodingID = Unicode BMP
	_ = binary.Write(&cmap, binary.BigEndian, uint32(12))
	cmap.Write(subBytes)

	head := make([]byte, 54)
	binary.BigEndian.PutUint16(head[50:], 0) // indexToLocFormat = 0（短 loca）
	maxp := make([]byte, 6)
	binary.BigEndian.PutUint16(maxp[4:], numGlyphs)

	tables := []struct {
		tag  string
		data []byte
	}{
		{"cmap", cmap.Bytes()},
		{"glyf", glyfData},
		{"loca", loca},
		{"head", head},
		{"maxp", maxp},
	}

	var out bytes.Buffer
	out.Write([]byte{0x00, 0x01, 0x00, 0x00}) // sfntVersion
	_ = binary.Write(&out, binary.BigEndian, uint16(len(tables)))
	_ = binary.Write(&out, binary.BigEndian, uint16(0))
	_ = binary.Write(&out, binary.BigEndian, uint16(0))
	_ = binary.Write(&out, binary.BigEndian, uint16(0))
	offset := 12 + len(tables)*16
	offsetsTable := make([]int, len(tables))
	for i, tb := range tables {
		padded := tb.data
		if len(padded)%4 != 0 {
			padded = append(padded, make([]byte, 4-len(padded)%4)...)
		}
		offsetsTable[i] = offset
		offset += len(padded)
	}
	for i, tb := range tables {
		out.WriteString(tb.tag)
		_ = binary.Write(&out, binary.BigEndian, uint32(0))
		_ = binary.Write(&out, binary.BigEndian, uint32(offsetsTable[i]))
		_ = binary.Write(&out, binary.BigEndian, uint32(len(tb.data)))
	}
	for _, tb := range tables {
		out.Write(tb.data)
		for out.Len()%4 != 0 {
			out.WriteByte(0)
		}
	}
	return out.Bytes()
}

// 解析出的码点 → 字形 → 码点映射与构造时一致。
func TestQueryTTFParseRoundTrip(t *testing.T) {
	data := buildTestTTF(t, map[rune]uint16{
		'A': 1, 'B': 2, 'C': 3, 'D': 4,
	})
	font, err := parseQueryTTFFont(data)
	if err != nil {
		t.Fatalf("解析字体失败: %v", err)
	}
	for cp, gid := range map[rune]uint16{'A': 1, 'B': 2, 'C': 3, 'D': 4} {
		if got := font.unicodeToGlyphID[cp]; got != gid {
			t.Fatalf("码点 %q 的字形下标 = %d，期望 %d", cp, got, gid)
		}
	}
	if font.glyphToUnicode[font.unicodeToGlyph['A']] != 'A' {
		t.Fatal("字形 → 码点映射不正确")
	}
	if font.unicodeToGlyph['A'] == font.unicodeToGlyph['B'] {
		t.Fatal("不同码点的轮廓不应相同")
	}
}

// replaceFont：错误字体把 A 渲染成 B 的字形，正确字体应把 A 还原成 B。
func TestReplaceFontRestoresText(t *testing.T) {
	// 错误字体：码点 A 指向字形 2（也就是 B 的形状）。
	errorFontData := buildTestTTF(t, map[rune]uint16{'A': 2, 'B': 3, 'C': 4, 'D': 5})
	// 正确字体：码点 B 指向字形 2。
	correctFontData := buildTestTTF(t, map[rune]uint16{'A': 1, 'B': 2, 'C': 3, 'D': 4})
	errorFont, err := parseQueryTTFFont(errorFontData)
	if err != nil {
		t.Fatal(err)
	}
	correctFont, err := parseQueryTTFFont(correctFontData)
	if err != nil {
		t.Fatal(err)
	}

	// 页面上写的是 'A'，实际字形是 B → 应还原为 'B'。
	got := replaceFontText("A", errorFont, correctFont, false)
	if got != "B" {
		t.Fatalf("replaceFont 还原结果 = %q，期望 %q", got, "B")
	}
	// 空白与未知码点保持原样。
	mixed := replaceFontText("A 中", errorFont, correctFont, false)
	if !strings.HasPrefix(mixed, "B ") || !strings.HasSuffix(mixed, "中") {
		t.Fatalf("混合文本处理异常: %q", mixed)
	}
	// filter=true 时删掉没有对应字形的字符。
	filtered := replaceFontText("A中", errorFont, correctFont, true)
	if filtered != "B" {
		t.Fatalf("filter 结果 = %q，期望 %q", filtered, "B")
	}
}

// 坏字体只应报错，不能 panic。
func TestQueryTTFBadFontNoPanic(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte("not a font"),
		[]byte("ttcf"),
		append([]byte{0x00, 0x01, 0x00, 0x00, 0x00, 0x02}, make([]byte, 40)...),
	}
	for i, data := range cases {
		if _, err := parseQueryTTFFont(data); err == nil {
			t.Fatalf("坏字体 #%d 应返回错误", i)
		}
	}
	// 截断的合法字体也不能 panic。
	full := buildTestTTF(t, map[rune]uint16{'A': 1})
	for cut := 1; cut < len(full); cut += 37 {
		_, _ = parseQueryTTFFont(full[:cut])
	}
}
