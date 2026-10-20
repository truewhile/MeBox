package rule

import (
	"testing"
)

// 本文件：legado 兼容性回归——`Packages.java.util.*` 容器与 `.[*]` 形式的 JSONPath。
//
// 背景（拷贝系列轻小说源在 MeBox 搜不到/读不了的根因）：
//  1. 搜索列表规则写成 `results.list.[*]`（legado/Jayway 接受），PaesslerAG 直接
//     解析报错返回空；
//  2. 目录规则用 `new Packages.java.util.LinkedHashMap()` / `ArrayList()` 拼装，
//     goja 里没有 Java，整条规则 ReferenceError。

// TestNormalizeJSONPathDotBracket 点后跟方括号应归一化成合法路径。
func TestNormalizeJSONPathDotBracket(t *testing.T) {
	cases := map[string]string{
		"results.list.[*]":   "$.results.list[*]",
		"$.results.list.[*]": "$.results.list[*]",
		"$.a.[0].b":          "$.a[0].b",
		"$.a.['k']":          "$.a['k']",
		"$.results.list":     "$.results.list",
		"$.results..list[*]": "$.results..list[*]",
		"results.list":       "$.results.list",
		"$[0].name":          "$[0].name",
		"..list[*]":          "$..list[*]",
	}
	for in, want := range cases {
		if got := normalizeJSONPath(in); got != want {
			t.Errorf("normalizeJSONPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGetElementsDotBracketStarJsonPath 真实书源的 `results.list.[*]` 必须能取到列表。
func TestGetElementsDotBracketStarJsonPath(t *testing.T) {
	const body = `{"results":{"list":[{"name":"甲"},{"name":"乙"}]}}`
	for _, rule := range []string{"results.list.[*]", "$.results.list", "results.list"} {
		ar := NewAnalyzeRule()
		ar.SetContent(body, "")
		els, err := ar.GetElements(rule)
		if err != nil {
			t.Fatalf("%s: %v", rule, err)
		}
		if len(els) != 2 {
			t.Fatalf("%s: elements=%d, want 2", rule, len(els))
		}
		name, err := ar.GetString("$.name", els[0], false)
		if err != nil || name != "甲" {
			t.Fatalf("%s: first name=%q err=%v", rule, name, err)
		}
	}
}

// TestPackagesUtilCollections Packages.java.util 的 List/Map 应能拼出目录列表，
// 且导出给引擎的是干净的数组/映射（方法不能混进元素里）。
func TestPackagesUtilCollections(t *testing.T) {
	r := NewJSRunner(JSConfig{})
	if err := r.JSLibErr(); err != nil {
		t.Fatalf("Packages 环境初始化失败: %v", err)
	}
	ar := NewAnalyzeRule()
	ar.SetContent(`{"results":{"list":[{"name":"第一卷","id":7}]}}`, "https://api.example.com/volumes")
	ar.SetJSRunner(r.ForAnalyzer(ar))

	js := `<js>
function mk(n, u) { var m = new Packages.java.util.LinkedHashMap(); m.put('name', n); m.put('url', u); return m; }
var base = 'https://api.example.com/volume/';
var out = new Packages.java.util.ArrayList();
var vols = JSON.parse(result).results.list;
for (var i = 0; i < vols.length; i++) { out.add(mk(vols[i].name, base + vols[i].id)); }
out;</js>`
	els, err := ar.GetElements(js)
	if err != nil {
		t.Fatalf("GetElements: %v", err)
	}
	if len(els) != 1 {
		t.Fatalf("elements=%d, want 1", len(els))
	}
	if m, ok := els[0].(map[string]any); ok {
		if _, hasMethod := m["put"]; hasMethod {
			t.Fatalf("容器方法泄漏进了元素: %#v", m)
		}
	}
	name, err := ar.GetString("$.name", els[0], false)
	if err != nil || name != "第一卷" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	url, _ := ar.GetString("$.url", els[0], false)
	if url != "https://api.example.com/volume/7" {
		t.Fatalf("url=%q", url)
	}
}

// TestPackagesHashMapUsableAsHeaders java.get(url, headers) 的 headers 用
// Packages.java.util.HashMap 构造时，导出结果应是干净的键值映射。
func TestPackagesHashMapUsableAsHeaders(t *testing.T) {
	r := NewJSRunner(JSConfig{})
	v, err := r.Run(NewAnalyzeRule(), `(function () {
  var h = new Packages.java.util.HashMap();
  h.put('X-Test', 'v1');
  return JSON.stringify(h);
})()`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if s := anyToString(v); s != `{"X-Test":"v1"}` {
		t.Fatalf("HashMap 导出含额外键: %s", s)
	}
}
