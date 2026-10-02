package rule

import (
	"strings"
	"testing"
)

// TestNumChapter 章节标题里的中文/阿拉伯数字要规整成阿拉伯数字。
// 书源用 java.toNumChapter 统一标题；缺这个函数时整条规则会抛异常（源直接不可用）。
func TestNumChapter(t *testing.T) {
	cases := map[string]string{
		"第一百零八章":  "第108章",
		"第十二话":    "第12话",
		"第3话":      "第3话",
		"第一话 冒险的序幕": "第1话 冒险的序幕",
		"一〇二":      "102",
		"两千零一":     "2001",
		"航海王":      "航海王", // 没有数字：原样返回
		"":         "",
	}
	for in, want := range cases {
		if got := numChapter(in); got != want {
			t.Errorf("numChapter(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestJavaExtraHelpers 书源 JS 里会直接调用的几个函数必须存在且可用。
func TestJavaExtraHelpers(t *testing.T) {
	r := NewJSRunner(JSConfig{})

	v, err := r.Run(NewAnalyzeRule(), `java.randomUUID()`, nil, "")
	if err != nil {
		t.Fatalf("java.randomUUID() 失败: %v", err)
	}
	id := anyToString(v)
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Fatalf("randomUUID 返回值不像 UUID: %q", id)
	}

	v, err = r.Run(NewAnalyzeRule(), `java.toNumChapter('第一百零八章')`, nil, "")
	if err != nil {
		t.Fatalf("java.toNumChapter() 失败: %v", err)
	}
	if got := anyToString(v); got != "第108章" {
		t.Fatalf("toNumChapter = %q，期望 第108章", got)
	}

	// 未声明设备能力时 java.deviceID 仍应抛异常（保持「不谎报运行环境」的语义）
	if _, err := r.Run(NewAnalyzeRule(), `java.deviceID()`, nil, ""); err == nil {
		t.Fatal("java.deviceID() 应当抛异常")
	}
}
