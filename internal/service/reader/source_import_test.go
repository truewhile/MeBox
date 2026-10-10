package reader

import (
	"strings"
	"testing"
)

// messySourceJSON 复刻真实「拷贝漫画（优++）」源文件的形状：
// lastUpdateTime 是字符串、respondTime 是数字、ruleExplore 是空数组、
// ruleBookInfo/ruleContent 里同时有数字与 JS 字符串。
// 这类文件在阅读 App 里能导入，在这里曾经整体解析失败。
const messySourceJSON = `[{
  "bookSourceComment": "//By情无羁25.04.09 使用需要魔法",
  "bookSourceGroup": "",
  "bookSourceName": "拷贝漫画（优++）",
  "bookSourceType": 0,
  "bookSourceUrl": "https://www.mangacopy.com/",
  "customOrder": 208,
  "enabled": true,
  "enabledCookieJar": false,
  "enabledExplore": true,
  "exploreUrl": "@js:\nsort = [];\nJSON.stringify(sort)",
  "header": "@js:\nJSON.stringify({\"platform\":\"1\",\"referer\":baseUrl})",
  "lastUpdateTime": "1788449879889",
  "respondTime": 181130,
  "ruleBookInfo": {"init": "$..comic", "intro": "$..brief"},
  "ruleContent": {"content": "$..url\n<js>result</js>", "imageStyle": "FULL"},
  "ruleExplore": [],
  "ruleSearch": {"bookList": "$..list[*]", "name": "$.name"},
  "ruleToc": {"chapterList": "$..list[*]", "chapterName": "name"},
  "searchUrl": "https://api.mangacopy.com/api/v3/search/comic?q={{key}}",
  "weight": 0
}]`

// TestImportSourcesAcceptsMessySource 覆盖用户的真实场景：数字写法的字符串字段
// （lastUpdateTime）与空数组写法的规则对象（ruleExplore）不该让整条书源导入失败。
func TestImportSourcesAcceptsMessySource(t *testing.T) {
	svc, _ := newLoginTestService(t)
	ctx := t.Context()

	imported, err := svc.ImportSources(ctx, "u1", messySourceJSON)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if imported != 1 {
		t.Fatalf("导入数量 = %d，期望 1", imported)
	}

	bs, err := svc.repo.GetSourceByURL(ctx, "u1", "https://www.mangacopy.com/")
	if err != nil || bs == nil {
		t.Fatalf("按 URL 查不到导入的书源: %v", err)
	}
	if bs.Name != "拷贝漫画（优++）" {
		t.Fatalf("书源名 = %q", bs.Name)
	}
	// 关键字段要真的解出来，而不是「解成功了但字段全空」
	parsed, err := ParseBookSource(bs.RawJSON)
	if err != nil {
		t.Fatalf("重新解析失败: %v", err)
	}
	if parsed.LastUpdateTime == nil || *parsed.LastUpdateTime != 1788449879889 {
		t.Fatalf("lastUpdateTime = %v，期望 1788449879889", parsed.LastUpdateTime)
	}
	if parsed.RuleExplore == nil {
		t.Fatal("ruleExplore 应当被当成空规则对象，而不是解析失败")
	}
	if SPtr(parsed.RuleSearch.BookList) != "$..list[*]" {
		t.Fatalf("ruleSearch.bookList = %q", SPtr(parsed.RuleSearch.BookList))
	}
	if SPtr(parsed.RuleContent.ImageStyle) != "FULL" {
		t.Fatalf("ruleContent.imageStyle = %q", SPtr(parsed.RuleContent.ImageStyle))
	}
	// 落库的 raw 必须是原文（源 JS 可能依赖原样字段），不能被改写。
	// 这里特意按原文里的「冒号后有空格」写法比对：一旦被重新序列化就对不上了。
	if !strings.Contains(bs.RawJSON, `"lastUpdateTime": "1788449879889"`) {
		t.Fatalf("RawJSON 被改写了，应当保留原始文本；实际: %s", bs.RawJSON)
	}
}

// TestNormalizeSourceJSON 单独锁定宽容规则本身。
func TestNormalizeSourceJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // 期望解析后可用的字段值 / 是否报错
	}{
		{"数字字符串", `{"weight":"12"}`, "12"},
		{"已经是数字", `{"weight":12}`, "12"},
		{"空字符串当缺省", `{"weight":""}`, "<nil>"},
		{"非整数不猜", `{"weight":"1.5"}`, "<err>"},
		{"空数组规则", `{"ruleExplore":[]}`, "obj"},
		{"正常规则不受影响", `{"ruleExplore":{"bookList":"a"}}`, "obj"},
	}
	for _, c := range cases {
		got := normalizeSourceJSON(c.in)
		switch c.want {
		case "obj":
			if !strings.Contains(got, "{}") && !strings.Contains(got, `"bookList":"a"`) {
				t.Errorf("%s: normalizeSourceJSON(%s) = %s", c.name, c.in, got)
			}
		case "<nil>":
			bs, err := ParseBookSource(got)
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
				continue
			}
			if bs.Weight != nil {
				t.Errorf("%s: weight 应当为 nil，实际 %v", c.name, *bs.Weight)
			}
		case "<err>":
			if _, err := ParseBookSource(got); err == nil {
				t.Errorf("%s: 期望解析报错（不做无根据的猜测）", c.name)
			}
		default:
			bs, err := ParseBookSource(got)
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
				continue
			}
			if bs.Weight == nil || *bs.Weight != 12 {
				t.Errorf("%s: weight = %v，期望 12", c.name, bs.Weight)
			}
		}
	}
}

// TestParseBookSourceRejectsGarbage 真的坏掉的 JSON 仍要报错，不能因为宽容而静默吞掉。
func TestParseBookSourceRejectsGarbage(t *testing.T) {
	if _, err := ParseBookSource(`{"bookSourceUrl":123}`); err == nil {
		t.Fatal("bookSourceUrl 是数字时应当报错")
	}
	if _, err := ParseBookSource(`{not json`); err == nil {
		t.Fatal("非法 JSON 应当报错")
	}
}

// TestImportSourcesReportsReasonWhenNothingImported 全部失败时必须把原因带回前端，
// 否则界面只会弹「成功导入 0 个书源」，用户无从下手。
func TestImportSourcesReportsReasonWhenNothingImported(t *testing.T) {
	svc, _ := newLoginTestService(t)

	_, err := svc.ImportSources(t.Context(), "u1", `[{"bookSourceUrl":123,"bookSourceName":"坏源"}]`)
	if err == nil {
		t.Fatal("一条都没导入时应当返回错误")
	}
	if !strings.Contains(err.Error(), "解析失败") {
		t.Fatalf("错误信息应说明是解析失败，实际: %v", err)
	}
}
