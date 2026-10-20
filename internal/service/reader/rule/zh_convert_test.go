package rule

import (
	"strings"
	"testing"
)

// TestZhToSimplified 常用繁体字要真的转成简体。
// 这是书源里 java.t2s(key) / java.t2s(result) 的语义：阅读 App 会转换，
// 不实现时繁体站点既搜不到（关键字没转）也显示不对（标题保持繁体）。
func TestZhToSimplified(t *testing.T) {
	cases := map[string]string{
		"島忍者神龜": "岛忍者神龟",
		"航海王":   "航海王",
		"我們的書":  "我们的书",
		"學習園地":  "学习园地",
		"":      "",
		"abc123": "abc123", // 非中文原样返回
	}
	for in, want := range cases {
		if got := zhToSimplified(in); got != want {
			t.Errorf("zhToSimplified(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestZhToTraditional 反向转换。
func TestZhToTraditional(t *testing.T) {
	cases := map[string]string{
		"岛忍者神龟": "島忍者神龜",
		"我们的书":  "我們的書",
		"学习园地":  "學習園地",
		"":      "",
	}
	for in, want := range cases {
		if got := zhToTraditional(in); got != want {
			t.Errorf("zhToTraditional(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestZhConvertLeavesPlainTextAlone 不需要转换的文本必须原样返回（含表情/符号）。
func TestZhConvertLeavesPlainTextAlone(t *testing.T) {
	const s = "第1话 ROMANCE DAWN 冒险的序幕 🏴‍☠️"
	if got := zhToSimplified(s); got != s {
		t.Fatalf("zhToSimplified 改动了无需转换的文本: %q", got)
	}
}

// TestZhDictParsed 词典要真的解析出来了（防止生成文件被截断）。
func TestZhDictParsed(t *testing.T) {
	zhInit()
	if len(zhT2S) < 3000 {
		t.Fatalf("t2s 词典条目 %d，明显偏少", len(zhT2S))
	}
	if len(zhS2T) < 3000 {
		t.Fatalf("s2t 词典条目 %d，明显偏少", len(zhS2T))
	}
	// 空格/竖线不属于任何键
	if _, ok := zhT2S[' ']; ok {
		t.Fatal("词典里混进了分隔符")
	}
	if strings.ContainsRune(zhT2SData, '\uFFFD') {
		t.Fatal("词典数据含替换符，生成过程有损")
	}
}
