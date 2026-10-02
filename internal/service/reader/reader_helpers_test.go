package reader

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// TestRewriteContentImageMarkers 文本型漫画源正文里的 <img> 要转成前端认识的 [img] 标记，
// 且地址要绝对化、过签名代理（有些图需要书源防盗链头才取得到）。
func TestRewriteContentImageMarkers(t *testing.T) {
	proxy := func(u string) string { return "/proxy?u=" + url.QueryEscape(u) }
	content := "第一章开始\n" +
		"<img src=\"https://img.example.com/1.webp\">\n" +
		"<img src='/img/2.webp'/>\n" +
		"中间夹了图片 <img src=\"https://img.example.com/3.webp\"> 的这一行不动\n" +
		"结束"
	got := rewriteContentImageMarkers(content, "https://site.example.com/ch/1", proxy)
	lines := strings.Split(got, "\n")
	if len(lines) != 5 {
		t.Fatalf("行数 = %d，期望 5\n%s", len(lines), got)
	}
	if lines[0] != "第一章开始" || lines[4] != "结束" {
		t.Fatalf("普通文本行被改动: %q / %q", lines[0], lines[4])
	}
	if !strings.HasPrefix(lines[1], imgMarkerPrefix) {
		t.Fatalf("第 2 行没有变成 [img] 标记: %q", lines[1])
	}
	if !strings.Contains(lines[1], url.QueryEscape("https://img.example.com/1.webp")) {
		t.Fatalf("第 2 行地址没走代理: %q", lines[1])
	}
	// 相对地址按章节地址绝对化
	if !strings.Contains(lines[2], url.QueryEscape("https://site.example.com/img/2.webp")) {
		t.Fatalf("相对图片地址没有绝对化: %q", lines[2])
	}
	// 段落中间的内嵌图片保持原样，避免破坏文本源语义
	if !strings.Contains(lines[3], "<img src=") {
		t.Fatalf("段落中间的 <img> 不该被改写: %q", lines[3])
	}
}

// TestRewriteContentImageMarkersWithoutProxy 不需要代理时（如预览链路）直接落到绝对地址。
func TestRewriteContentImageMarkersWithoutProxy(t *testing.T) {
	got := rewriteContentImageMarkers(`<img src="https://img.example.com/1.webp">`, "https://site.example.com/ch/1", nil)
	if got != imgMarkerPrefix+"https://img.example.com/1.webp" {
		t.Fatalf("got %q", got)
	}
	// 没有 <img> 的内容原样返回（含 null 字节之外的普通文本）
	const plain = "纯文本\n第二行"
	if got := rewriteContentImageMarkers(plain, "", nil); got != plain {
		t.Fatalf("无图内容被改动: %q", got)
	}
}

// TestSearchCheckKeyWord 校验关键字的取值规则（对应 legado getCheckKeyword）：
// 含 http/::/++/-- 的值是地址或扩展标记，不当作关键字。
func TestSearchCheckKeyWord(t *testing.T) {
	mk := func(kw string) *BookSource {
		return &BookSource{RuleSearch: &SearchRule{CheckKeyWord: &kw}}
	}
	cases := []struct {
		in   string
		want string
	}{
		{"登录武林系统", "登录武林系统"},
		{"  空格清理  ", "空格清理"},
		{"", ""},
		{"http://example.com", ""},
		{"a::b", ""},
		{"a++b", ""},
		{"a--b", ""},
	}
	for _, c := range cases {
		if got := mk(c.in).SearchCheckKeyWord(); got != c.want {
			t.Errorf("SearchCheckKeyWord(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
	if (&BookSource{}).SearchCheckKeyWord() != "" {
		t.Fatal("没有 ruleSearch 时应返回空")
	}
}

// TestSearchReportsCheckKeyWordMiss 搜索 0 条且响应里没有书源声明的校验关键字时，
// 必须给出「疑似风控」的原因，而不是静默返回 0 条 —— 用户才知道是源的问题。
func TestSearchReportsCheckKeyWordMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 风控/失效时站点返回的就是这种没有结果的响应
		_, _ = fmt.Fprint(w, `{"code":200,"results":{"list":[]}}`)
	}))
	defer srv.Close()

	kw := "登录武林系统"
	header := ""
	src := &model.ReaderBookSource{Name: "校验关键字测试源", SourceURL: srv.URL}
	bs := &BookSource{
		BookSourceURL: srv.URL,
		Header:        &header,
		SearchURL:     strPtr(srv.URL + "/search?q={{key}}"),
		RuleSearch: &SearchRule{
			BookList:     strPtr("$.results.list[*]"),
			Name:         strPtr("$.name"),
			BookURL:      strPtr("$.url"),
			CheckKeyWord: &kw,
		},
	}

	svc, _ := newLoginTestService(t)
	_, err := svc.searchInSource(t.Context(), src, bs, "火影", 1)
	if err == nil {
		t.Fatal("0 结果且校验关键字缺失时应返回错误")
	}
	if !strings.Contains(err.Error(), kw) {
		t.Fatalf("错误信息里应带上校验关键字，实际: %v", err)
	}
}

// TestSearchKeepsEmptyResultWhenKeywordPresent 响应里有关键字（站点正常，只是没有匹配的书）
// 时不该报错，只是结果为空。
func TestSearchKeepsEmptyResultWhenKeywordPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":200,"hint":"登录武林系统","results":{"list":[]}}`)
	}))
	defer srv.Close()

	kw := "登录武林系统"
	header := ""
	src := &model.ReaderBookSource{Name: "校验关键字测试源", SourceURL: srv.URL}
	bs := &BookSource{
		BookSourceURL: srv.URL,
		Header:        &header,
		SearchURL:     strPtr(srv.URL + "/search?q={{key}}"),
		RuleSearch: &SearchRule{
			BookList:     strPtr("$.results.list[*]"),
			Name:         strPtr("$.name"),
			BookURL:      strPtr("$.url"),
			CheckKeyWord: &kw,
		},
	}

	svc, _ := newLoginTestService(t)
	books, err := svc.searchInSource(t.Context(), src, bs, "火影", 1)
	if err != nil {
		t.Fatalf("站点正常时不该报错: %v", err)
	}
	if len(books) != 0 {
		t.Fatalf("结果数 = %d，期望 0", len(books))
	}
}
