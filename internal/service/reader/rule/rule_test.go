package rule

import (
	"strings"
	"testing"
)

// ─── RuleAnalyzer ──────────────────────────────────────────────────────────

func TestRuleAnalyzerSplitAndOr(t *testing.T) {
	ra := NewRuleAnalyzer("class.a&&tag.b&&id.c", false)
	rules := ra.SplitRule("&&", "||", "%%")
	if len(rules) != 3 || rules[0] != "class.a" || rules[1] != "tag.b" || rules[2] != "id.c" {
		t.Fatalf("unexpected split: %#v", rules)
	}
	if ra.ElementsType() != "&&" {
		t.Fatalf("elementsType = %q, want &&", ra.ElementsType())
	}
	// 与 Kotlin 一致：consumeToAny 取最左侧出现的分隔符，
	// 之后只按该分隔符切分（混合操作符时右侧保留原样，由上层递归处理）。
	ra2 := NewRuleAnalyzer("class.a&&tag.b||id.c", false)
	rules2 := ra2.SplitRule("&&", "||", "%%")
	if len(rules2) != 2 || rules2[0] != "class.a" || rules2[1] != "tag.b||id.c" {
		t.Fatalf("mixed split: %#v", rules2)
	}
}

func TestRuleAnalyzerBalancedGroup(t *testing.T) {
	// && 在选择器平衡组内不应被切分
	ra := NewRuleAnalyzer(`tag.div[class="x&&y"]@text&&class.z`, false)
	rules := ra.SplitRule("&&", "||", "%%")
	if len(rules) != 2 {
		t.Fatalf("unexpected split: %#v", rules)
	}
	if rules[0] != `tag.div[class="x&&y"]@text` {
		t.Fatalf("rule[0] = %q", rules[0])
	}
}

func TestRuleAnalyzerSplitByAt(t *testing.T) {
	ra := NewRuleAnalyzer("class.bookbox@h4@a@text", false)
	ra.Trim()
	rules := ra.SplitRule("@")
	if len(rules) != 4 {
		t.Fatalf("unexpected split: %#v", rules)
	}
}

// ─── SourceRule 模式识别 ────────────────────────────────────────────────────

func TestSourceRuleModeDetection(t *testing.T) {
	cases := []struct {
		rule          string
		contentIsJSON bool
		want          Mode
	}{
		{"class.a@text", false, ModeDefault},
		{"$.data.name", false, ModeJson},
		{"$[0].name", false, ModeJson},
		{"//div[@class='a']/text()", false, ModeXPath},
		{"@XPath://div", false, ModeXPath},
		{"@Json:$.a", false, ModeJson},
		{"@CSS:.a@text", false, ModeDefault},
		{"title", true, ModeJson}, // 内容为 JSON 时默认走 Json 模式
		{"/html/body", false, ModeXPath},
	}
	for _, c := range cases {
		got := newSourceRule(c.rule, ModeDefault, c.contentIsJSON)
		if got.Mode != c.want {
			t.Errorf("rule %q mode = %v, want %v", c.rule, got.Mode, c.want)
		}
	}
}

func TestSourceRuleSplitPut(t *testing.T) {
	sr := newSourceRule(`class.a@text@put:{"key1":"class.b@text"}`, ModeDefault, false)
	if sr.Rule != "class.a@text" {
		t.Fatalf("rule after put split = %q", sr.Rule)
	}
	if sr.putMap["key1"] != "class.b@text" {
		t.Fatalf("putMap = %#v", sr.putMap)
	}
}

func TestMakeUpRuleGetVariable(t *testing.T) {
	sr := newSourceRule(`@get:{kw}`, ModeDefault, false)
	deps := &RuleDeps{Get: func(key string) string { return "搜索词" }}
	resolved, err := sr.MakeUpRule(nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Rule != "搜索词" {
		t.Fatalf("resolved = %q", resolved.Rule)
	}
}

func TestSplitSourceRuleJSBlocks(t *testing.T) {
	rules := SplitSourceRule(`<js>1+1</js>class.a@text`, false, false, nil)
	if len(rules) != 2 || rules[0].Mode != ModeJs || rules[1].Mode != ModeDefault {
		t.Fatalf("rules = %#v", rules)
	}
	if rules[0].Rule != "1+1" {
		t.Fatalf("js body = %q", rules[0].Rule)
	}
}

// ─── jsoup 分析器 ──────────────────────────────────────────────────────────

const testHTML = `<!DOCTYPE html>
<html><body>
<div class="box" id="main">
  <div class="item"><h3><a href="/book/1">斗破苍穹</a></h3><span class="author">天蚕土豆</span><p class="intro">测试<strong>简介</strong></p></div>
  <div class="item"><h3><a href="/book/2">凡人修仙传</a></h3><span class="author">忘语</span><p class="intro">凡人流</p></div>
  <div class="item"><h3><a href="/book/3">遮天</a></h3><span class="author">辰东</span><p class="intro">九龙拉棺</p></div>
</div>
</body></html>`

func TestJsoupGetElementsAndFields(t *testing.T) {
	a := newJsoupAnalyzer(testHTML)
	els := a.getElements("class.item")
	if len(els) != 3 {
		t.Fatalf("elements = %d, want 3", len(els))
	}
	name := a.getString("class.item.0@tag.h3@tag.a@text")
	if name != "斗破苍穹" {
		t.Fatalf("name = %q", name)
	}
	href := a.getString("class.item.0@tag.h3@tag.a@href")
	if href != "/book/1" {
		t.Fatalf("href = %q", href)
	}
	author := a.getString("class.item.1@class.author@text")
	if author != "忘语" {
		t.Fatalf("author = %q", author)
	}
	// all：拼接所有
	names := a.getStringList("class.item@tag.h3@tag.a@text")
	if len(names) != 3 || names[2] != "遮天" {
		t.Fatalf("names = %#v", names)
	}
}

func TestJsoupTextNodesAndOwnText(t *testing.T) {
	a := newJsoupAnalyzer(testHTML)
	// ownText：不含子元素文本
	intro := a.getString("class.item.0@tag.p@ownText")
	if intro != "测试" {
		t.Fatalf("ownText = %q", intro)
	}
	// text：含子元素文本（jsoup 不在内联元素间补空格）
	full := a.getString("class.item.0@tag.p@text")
	if full != "测试简介" {
		t.Fatalf("text = %q", full)
	}
}

func TestJsoupIndexSyntax(t *testing.T) {
	a := newJsoupAnalyzer(testHTML)
	// 负索引：最后一个
	last := a.getString("class.item.-1@tag.a@text")
	if last != "遮天" {
		t.Fatalf("last = %q", last)
	}
	// 新式区间索引
	firstTwo := a.getStringList("class.item[0:1]@tag.a@text")
	if len(firstTwo) != 2 || firstTwo[0] != "斗破苍穹" || firstTwo[1] != "凡人修仙传" {
		t.Fatalf("range = %#v", firstTwo)
	}
}

func TestJsoupAndOrPercent(t *testing.T) {
	a := newJsoupAnalyzer(testHTML)
	// &&：合并两路结果
	merged := a.getStringList(`class.item.0@tag.a@text&&class.item.1@tag.a@text`)
	if len(merged) != 2 {
		t.Fatalf("&& merged = %#v", merged)
	}
	// ||：第一个非空即停
	or := a.getStringList(`id.notexist@text||class.item.0@tag.a@text`)
	if len(or) != 1 || or[0] != "斗破苍穹" {
		t.Fatalf("|| result = %#v", or)
	}
}

func TestJsoupCSSMode(t *testing.T) {
	a := newJsoupAnalyzer(testHTML)
	// @CSS: 末段为提取规则（与 jsoup 分析器一致的 @ 分离）
	name := a.getString("@CSS:#main .item:nth-child(1) a@text")
	if name != "斗破苍穹" {
		t.Fatalf("@css name = %q", name)
	}
	href := a.getString("@CSS:.item:nth-child(2) a@href")
	if href != "/book/2" {
		t.Fatalf("@css href = %q", href)
	}
}

// ─── JSONPath 分析器 ───────────────────────────────────────────────────────

const testJSON = `{"data":{"list":[{"title":"第一章","url":"/c/1"},{"title":"第二章","url":"/c/2"}],"name":"测试书","page":2}}`

func TestJSONPathGetString(t *testing.T) {
	a := newJSONAnalyzer(testJSON)
	if got := a.getString("$.data.name"); got != "测试书" {
		t.Fatalf("name = %q", got)
	}
	if got := a.getString("$.data.list[*].title"); got != "第一章\n第二章" {
		t.Fatalf("titles = %q", got)
	}
	// || 首个非空
	if got := a.getString("$.data.missing||$.data.name"); got != "测试书" {
		t.Fatalf("|| = %q", got)
	}
}

func TestJSONPathInnerRule(t *testing.T) {
	a := newJSONAnalyzer(testJSON)
	// {$.data.page} 内嵌规则替换
	got := a.getString("/api/list/{$.data.page}/next.json")
	if got != "/api/list/2/next.json" {
		t.Fatalf("inner = %q", got)
	}
}

func TestJSONPathListAndElementContext(t *testing.T) {
	a := newJSONAnalyzer(testJSON)
	list := a.getList("$.data.list[*]")
	if len(list) != 2 {
		t.Fatalf("list = %#v", list)
	}
	// 以列表元素为根继续求值（对应 getString(rule, element)）
	sub := newJSONAnalyzer(list[0])
	if got := sub.getString("$.title"); got != "第一章" {
		t.Fatalf("element title = %q", got)
	}
}

// ─── XPath 分析器 ──────────────────────────────────────────────────────────

func TestXPathAnalyzer(t *testing.T) {
	a := newXPathAnalyzer(testHTML)
	if got := a.getString(`//div[@class="item"][1]//a/text()`); got != "斗破苍穹" {
		t.Fatalf("xpath = %q", got)
	}
	hrefs := a.getStringList(`//div[@class="item"]//a/@href`)
	if len(hrefs) != 3 {
		t.Fatalf("hrefs = %#v", hrefs)
	}
	els := a.getElements(`//div[@class="item"]`)
	if len(els) != 3 {
		t.Fatalf("elements = %d", len(els))
	}
}

// ─── 正则分析器 ────────────────────────────────────────────────────────────

func TestRegexAnalyzer(t *testing.T) {
	content := "第1章 开始 第2章 继续 第3章 结束"
	els := regexGetElements(content, []string{`第(\d+)章 ([^ ]+)`}, 0)
	if len(els) != 3 || els[0][1] != "1" || els[2][2] != "结束" {
		t.Fatalf("regex elements = %#v", els)
	}
}

func TestReplaceRegex(t *testing.T) {
	a := NewAnalyzeRule()
	a.SetContent(testHTML, "http://x.com")
	// ## 目标##替换
	got, err := a.GetString(`class.item.0@tag.a@text##斗破##破斗`, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "破斗苍穹" {
		t.Fatalf("replace = %q", got)
	}
}

// ─── AnalyzeUrl ────────────────────────────────────────────────────────────

func TestParseAnalyzeUrlBasic(t *testing.T) {
	req, err := ParseAnalyzeUrl("https://example.com/search/{{key}}/1.html", "斗罗", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	// 与 legado 一致：路径段不做百分号编码（执行时由 HTTP 客户端转义）
	if !strings.Contains(req.URL, "/search/斗罗/1.html") {
		t.Fatalf("url = %q", req.URL)
	}
}

func TestParseAnalyzeUrlPageList(t *testing.T) {
	// <1,20,40>：page=1 → 1；page=2 → 20；page=5 → 40（取最后一档）
	for page, want := range map[int]string{1: "1", 2: "20", 5: "40"} {
		req, err := ParseAnalyzeUrl("https://e.com/list/<1,20,40>.html", "k", page, "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(req.URL, want+".html") {
			t.Fatalf("page=%d url = %q", page, req.URL)
		}
	}
}

func TestParseAnalyzeUrlGBKQueryEncoding(t *testing.T) {
	req, err := ParseAnalyzeUrl("https://e.com/search.php?keyword={{key}},{\"charset\":\"gbk\"}", "斗罗", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	// "斗罗" 的 GBK 编码 = B6 B7 C2 DE
	if !strings.Contains(req.URL, "%B6%B7%C2%DE") {
		t.Fatalf("gbk url = %q", req.URL)
	}
}

func TestParseAnalyzeUrlPostForm(t *testing.T) {
	req, err := ParseAnalyzeUrl(`https://e.com/search,{"method":"POST","body":"searchkey={{key}}&submit=go"}`, "斗罗", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" {
		t.Fatalf("method = %s", req.Method)
	}
	if !req.IsForm {
		t.Fatalf("body should be form, got %q", req.Body)
	}
	if !strings.Contains(req.Body, "searchkey=") {
		t.Fatalf("body = %q", req.Body)
	}
}

func TestParseAnalyzeUrlWebViewUnsupported(t *testing.T) {
	req, err := ParseAnalyzeUrl(`https://e.com/x,{"webView":true}`, "k", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Unsupported == nil {
		t.Fatal("expected unsupported for webView")
	}
}

// ─── httptest 端到端：书源 JSON → 搜索解析全链路（见 reader 包 reader_test.go） ──
