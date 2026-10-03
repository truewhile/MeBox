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

// TestImageMarkersOnly 整章都是 [img] 标记时判定为图片章（前端才能走漫画阅读器、
// 双页铺开）；只要掺了能读的正文就必须保持文本，不能把文字吃掉。
func TestImageMarkersOnly(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantOK   bool
		wantURLs []string
	}{
		{
			name:     "整章都是标记",
			content:  imgMarkerPrefix + "/a.jpg\n" + imgMarkerPrefix + "/b.jpg\n",
			wantOK:   true,
			wantURLs: []string{"/a.jpg", "/b.jpg"},
		},
		{
			name: "夹着空行 / 孤立 html 标签 / 首尾空白",
			content: "\n" + imgMarkerPrefix + "/a.jpg\n \n<div>\n</div>\n< br >\n" +
				"  " + imgMarkerPrefix + " /b.jpg  \n",
			wantOK:   true,
			wantURLs: []string{"/a.jpg", "/b.jpg"},
		},
		{
			name:    "掺了正文就保持文本（图 + 长段落）",
			content: imgMarkerPrefix + "/a.jpg\n" + "第一句话。\n" + imgMarkerPrefix + "/b.jpg\n",
			wantOK:  false,
		},
		{
			name:    "纯文本",
			content: "第一章\n正文正文\n",
			wantOK:  false,
		},
		{
			name:    "空正文",
			content: "\n\n   \n",
			wantOK:  false,
		},
		{
			name:    "标记地址为空",
			content: imgMarkerPrefix + "/a.jpg\n" + imgMarkerPrefix + "   \n",
			wantOK:  false,
		},
		{
			// 「<3」这类不是标签，是有内容的一行，不能当成空白忽略
			name:    "非标签的尖括号行",
			content: imgMarkerPrefix + "/a.jpg\n3 < 5\n",
			wantOK:  false,
		},
		{
			// 回归：<p>正文</p> 是「标签里裹着正文」，绝不能当成孤立标签跳过，
			// 否则整章正文会被误判成图片章清空。
			name:    "标签里裹着正文的行",
			content: imgMarkerPrefix + "/a.jpg\n<p>第一章正文</p>\n<p>第二章正文</p>\n",
			wantOK:  false,
		},
	}
	for _, c := range cases {
		got, ok := imageMarkersOnly(c.content)
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v，期望 %v（urls=%v）", c.name, ok, c.wantOK, got)
			continue
		}
		if !ok {
			continue
		}
		if len(got) != len(c.wantURLs) {
			t.Errorf("%s: urls = %v，期望 %v", c.name, got, c.wantURLs)
			continue
		}
		for i := range got {
			if got[i] != c.wantURLs[i] {
				t.Errorf("%s: urls[%d] = %q，期望 %q", c.name, i, got[i], c.wantURLs[i])
			}
		}
	}
}

// TestIsHTMLTagOnly 「没有可读文字的标签行」才算空行：标签里裹着正文的行必须放行，
// 否则 imageMarkersOnly 会把整章正文当图片丢掉。
func TestIsHTMLTagOnly(t *testing.T) {
	blank := []string{"<div>", "</div>", "< br >", "<br/>", "<p></p>", "<hr />", "</p >", "<a> </a>", "<p>"}
	for _, s := range blank {
		if !isHTMLTagOnly(s) {
			t.Errorf("%q 应视为没有可读文字的标签行", s)
		}
	}
	text := []string{
		"<p>正文</p>", "<p>正文</p></p>", "<div>文字</div>", "<p>第一段</p><p>第二段</p>",
		"正文 <b>粗</b>", "3 < 5", "<3>", "a", "", "   ",
	}
	for _, s := range text {
		if isHTMLTagOnly(s) {
			t.Errorf("%q 含可读文字/不是标签，不能当空行", s)
		}
	}
}

// TestNormalizeContentBlocks 块级标签折成换行、行内标签与文字原样保留。
func TestNormalizeContentBlocks(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<p>第一段</p>\n<p>第二段</p>", "第一段\n第二段"},
		{"<p>第一段<br>折行文字</p>", "第一段\n折行文字"},
		{"<P CLASS='x'>大写带属性</P>", "大写带属性"},
		{"<div>块</div><br/><br />", "块"},
		// 行内插图原样保留（不能吃掉普通 <img>）
		{`<p>正文<img src="https://cdn.example.com/a.jpg"></p>`, `正文<img src="https://cdn.example.com/a.jpg">`},
		// 纯文本原样返回
		{"第一段。\n第二段。", "第一段。\n第二段。"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeContentBlocks(c.in); got != c.want {
			t.Errorf("normalizeContentBlocks(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestChapterContentEmpty 判断「什么都没取到」的正文。
//
// 聚合类书源把所有线路试完会返回空串（光遇聚合的 request() 就是这么写的），
// 这种空结果不能再当成功下发（前端会渲染成白页并缓存下来）。
func TestChapterContentEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   ChapterContent
		want bool
	}{
		{"空正文", ChapterContent{Type: "text"}, true},
		{"只有空白字符", ChapterContent{Type: "text", Content: "  \n\t "}, true},
		{"正常正文", ChapterContent{Type: "text", Content: "第一章 世界大变"}, false},
		// 整章正文只有一个本章说气泡时正文文字为空，但不能算抓取失败。
		{"只有段评气泡", ChapterContent{Type: "text", Comments: []ContentComment{{Line: 0, Count: 3}}}, false},
		{"音频无音轨", ChapterContent{Type: "audio"}, true},
		{"音频有音轨", ChapterContent{Type: "audio", Tracks: []string{"https://cdn.example.com/a.m4a"}}, false},
		{"漫画无图", ChapterContent{Type: "image"}, true},
		{"漫画有图", ChapterContent{Type: "image", Images: []string{"https://cdn.example.com/1.jpg"}}, false},
	}
	for _, c := range cases {
		if got := chapterContentEmpty(&c.in); got != c.want {
			t.Errorf("%s: chapterContentEmpty = %v，期望 %v", c.name, got, c.want)
		}
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
