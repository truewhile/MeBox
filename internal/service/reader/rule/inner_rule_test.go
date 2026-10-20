package rule

import "testing"

// 书源普遍用 legado 的内嵌模板拼字段（聚合源的 kind / 最新章节就是这种写法）：
//
//	kind        = "{{$.status}},{{$.score}},{{$.tags}}"
//	lastChapter = "{{$.source}} {{$.last_chapter_title}}"
//
// 模板必须在**当前元素**上求值。修复前这里拿整份响应求值，模板一律取空，
// 表现为 kind=",,,"、最新章节为空——搜索结果和换源列表里所有条目长得一模一样。
func TestInnerRuleTemplateEvaluatesAgainstElement(t *testing.T) {
	const body = `{"data":[{"book_name":"全球高武","status":"连载","score":"9.2","tags":"都市","source":"svip_酷我","last_chapter_title":"第694章"}]}`
	a := NewAnalyzeRule()
	a.SetContent(body, "")
	els, err := a.GetElements("$.data")
	if err != nil {
		t.Fatalf("GetElements: %v", err)
	}
	if len(els) != 1 {
		t.Fatalf("want 1 element, got %d", len(els))
	}
	el := els[0]

	got, err := a.GetString("{{$.status}},{{$.score}},{{$.tags}}", el, false)
	if err != nil {
		t.Fatalf("GetString: %v", err)
	}
	if got != "连载,9.2,都市" {
		t.Fatalf("kind 模板 = %q, want 连载,9.2,都市", got)
	}

	got, err = a.GetString("{{$.source}} {{$.last_chapter_title}}", el, false)
	if err != nil {
		t.Fatalf("GetString: %v", err)
	}
	if got != "svip_酷我 第694章" {
		t.Fatalf("最新章节模板 = %q, want svip_酷我 第694章", got)
	}

	// 列表规则（GetStringList）走的是同一套 MakeUpRule，必须同样生效
	list, err := a.GetStringList("{{$.status}},{{$.score}}", el, false)
	if err != nil {
		t.Fatalf("GetStringList: %v", err)
	}
	if len(list) != 1 || list[0] != "连载,9.2" {
		t.Fatalf("列表模板 = %v, want [连载,9.2]", list)
	}

	// 模板里取不存在的字段应得到空串，而不是报错
	got, err = a.GetString("[{{$.not_exist}}]", el, false)
	if err != nil {
		t.Fatalf("GetString: %v", err)
	}
	if got != "[]" {
		t.Fatalf("缺失字段 = %q, want []", got)
	}
}
