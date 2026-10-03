package reader

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// 段评解析单测：覆盖 legado 的两种正文形态（<comment /> 与「图片地址 + JSON 配置」），
// 以及「绝不能吞掉或破坏正文文字」这条硬约束。

// svgCommentSrc 按光遇聚合 createSvg 的写法拼一张带评论数气泡图：
// data:image/svg+xml;base64,<svg>,{"click":"showCmt('地址','平台','段评')","style":"text"}
//
// 注意第二个逗号后面直接跟 JSON，且 JSON 自身用双引号——它嵌在双引号属性里，
// 正是会打断普通正则的「非法 HTML」，解析必须扛得住。
func svgCommentSrc(t *testing.T, count int, click string, style string) string {
	t.Helper()
	svg := fmt.Sprintf(
		`<svg xmlns='http://www.w3.org/2000/svg'><text x='1' y='2'>%d</text></svg>`, count)
	b64 := base64.StdEncoding.EncodeToString([]byte(svg))
	return fmt.Sprintf(`data:image/svg+xml;base64,%s,{"click":%q,"style":%q}`, b64, click, style)
}

func TestExtractContentCommentsLegacyTag(t *testing.T) {
	content := `<p>第一段<comment ident="https://cdn.example.com/c/1" count="12" /></p>
<p>第二段</p>`
	text, comments := extractContentComments(content)
	if text != "<p>第一段</p>\n<p>第二段</p>" {
		t.Fatalf("正文被破坏：%q", text)
	}
	if len(comments) != 1 {
		t.Fatalf("评论数 = %d，期望 1", len(comments))
	}
	c := comments[0]
	if c.Line != 0 || c.Count != 12 || c.URL != "https://cdn.example.com/c/1" || c.Label != "段评" {
		t.Fatalf("解析结果不符：%+v", c)
	}
	if c.Block {
		t.Fatalf("段落内的段评不应标记为整块：%+v", c)
	}
}

func TestExtractContentCommentsIOSOnPress(t *testing.T) {
	content := `<p>正文<comment count="7" onPress="java.showReadingBrowser('https://cdn.example.com/c/2?a=1&amp;b=2','番茄段评')"></comment></p>`
	text, comments := extractContentComments(content)
	if text != "<p>正文</p>" {
		t.Fatalf("正文被破坏：%q", text)
	}
	if len(comments) != 1 {
		t.Fatalf("评论数 = %d，期望 1", len(comments))
	}
	// &amp; 必须还原成 &，否则请求评论接口会 404
	if comments[0].URL != "https://cdn.example.com/c/2?a=1&b=2" || comments[0].Count != 7 {
		t.Fatalf("解析结果不符：%+v", comments[0])
	}
}

func TestExtractContentCommentsInlineImage(t *testing.T) {
	src := svgCommentSrc(t, 3, "showCmt('https://v1.example.com/get_review?book_id=1&item_id=2','番茄','段评')", "text")
	content := `<p>正文内容<img src="` + src + `"></p>
<p>下一段</p>`
	text, comments := extractContentComments(content)
	if text != "<p>正文内容</p>\n<p>下一段</p>" {
		t.Fatalf("正文被破坏：%q", text)
	}
	if len(comments) != 1 {
		t.Fatalf("评论数 = %d，期望 1", len(comments))
	}
	c := comments[0]
	if c.Line != 0 || c.Count != 3 || c.Label != "段评" {
		t.Fatalf("解析结果不符：%+v", c)
	}
	// 地址里的 & 原样保留（JSON 里的 & 不是实体）
	if c.URL != "https://v1.example.com/get_review?book_id=1&item_id=2" {
		t.Fatalf("评论地址 = %q", c.URL)
	}
}

func TestExtractContentCommentsChapterEndBlock(t *testing.T) {
	// 章末「本章说」：单独一行，配置用单引号键、双引号值（合法 JS 对象字面量，非法 JSON）
	line := `<img src="https://v1.example.com/chapter_review_svg?book_id=1&amp;img=1,` +
		`{'type':'qtbzs',` +
		`"click":"showCmt('https://v1.example.com/get_review?book_id=1','番茄','本章说' )",` +
		`'style':'FULL'}">`
	content := "<p>正文最后一段</p>\n" + line
	text, comments := extractContentComments(content)
	if text != "<p>正文最后一段</p>\n" {
		t.Fatalf("正文被破坏：%q", text)
	}
	if len(comments) != 1 {
		t.Fatalf("评论数 = %d，期望 1", len(comments))
	}
	c := comments[0]
	if c.Line != 1 || c.Label != "本章说" || !c.Block {
		t.Fatalf("解析结果不符：%+v", c)
	}
	if c.URL != "https://v1.example.com/get_review?book_id=1" {
		t.Fatalf("评论地址 = %q", c.URL)
	}
}

func TestExtractContentCommentsCountOver99(t *testing.T) {
	// 书源把 >99 的评论数显示成 "99+"，解析要取到 99
	src := svgCommentSrc(t, 99, "showCmt('https://x.example.com/c','番茄','段评')", "text")
	// 手工把文本改成 99+
	src = strings.Replace(src, ">99<", ">99+<", 1)
	_, comments := extractContentComments(`<p>x<img src="` + src + `"></p>`)
	if len(comments) != 1 || comments[0].Count != 99 {
		t.Fatalf("期望解析出 99，得到 %+v", comments)
	}
}

// 没有任何段评标记时，正文必须逐字节不变（不能因为「看起来像 HTML」就动手）。
func TestExtractContentCommentsPreservesPlainContent(t *testing.T) {
	content := `<p>普通段落</p>
<p>带插图的段落<img src="https://cdn.example.com/a.jpg"></p>
<p>带 onclick 图但非段评<img src="https://cdn.example.com/b.jpg,{'click':'openPhoto(1)'}"></p>`
	text, comments := extractContentComments(content)
	if text != content {
		t.Fatalf("正文被改动：\n got %q\nwant %q", text, content)
	}
	if len(comments) != 0 {
		t.Fatalf("不应解析出段评：%+v", comments)
	}
}

func TestExtractContentCommentsEmpty(t *testing.T) {
	if text, comments := extractContentComments(""); text != "" || comments != nil {
		t.Fatalf("空正文应原样返回，得到 %q / %+v", text, comments)
	}
	if text, comments := extractContentComments("<p>只有普通正文</p>"); text != "<p>只有普通正文</p>" || comments != nil {
		t.Fatalf("无标记正文应原样返回，得到 %q / %+v", text, comments)
	}
}

func TestSplitImgSrcParams(t *testing.T) {
	base, params := splitImgSrcParams(`data:image/svg+xml;base64,QUJD,{"click":"showCmt('u','x')","style":"text"}`)
	if base != "data:image/svg+xml;base64,QUJD" {
		t.Fatalf("base = %q", base)
	}
	if params["click"] != "showCmt('u','x')" || params["style"] != "text" {
		t.Fatalf("params = %+v", params)
	}
	if base, params := splitImgSrcParams("https://x.example.com/a.jpg"); params != nil || base != "https://x.example.com/a.jpg" {
		t.Fatalf("无配置时不应拆出参数：%q / %+v", base, params)
	}
}

func TestValidateCommentURL(t *testing.T) {
	bad := []string{
		"", "not-a-url", "file:///etc/passwd", "javascript:alert(1)",
		"http://localhost/c", "http://127.0.0.1/c", "http://192.168.1.1/c", "http://[::1]/c",
	}
	for _, u := range bad {
		if _, err := validateCommentURL(u); err == nil {
			t.Fatalf("%q 应被拒绝", u)
		}
	}
	good := "https://v1.qingtian618.com/get_review?book_id=1&item_id=2"
	if got, err := validateCommentURL(good); err != nil || got != good {
		t.Fatalf("合法地址被拒：%q / %v", got, err)
	}
}

// 端到端：书源正文里带段评标记时，GetContentForBook 应清掉标记、
// 下发结构化 Comments，且正文文字完好。
func TestGetContentForBookExtractsComments(t *testing.T) {
	src := svgCommentSrc(t, 5, "showCmt('https://v1.example.com/get_review?book_id=1','番茄','段评')", "text")
	// 服务端按 hex 还原规则返回值里的正文；这里让正文规则直接返回一段放进 data 信封的 JSON。
	pageJSON := fmt.Sprintf(`{"content":%q}`, "<p>第一段<img src=\""+src+"\"></p>\n<p>第二段</p>")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(pageJSON))
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	srcJSON := fmt.Sprintf(`{
  "bookSourceUrl": %q,
  "bookSourceName": "段评测试源",
  "bookSourceType": 0,
  "ruleContent": { "content": "$.content" }
}`, srv.URL)
	importTestSource(t, svc, srcJSON, srv.URL)

	book := &model.ReaderBook{
		UserID:     "u1",
		Origin:     srv.URL,
		OriginName: "段评测试源",
		BookURL:    srv.URL + "/book/1",
		Name:       "测试书",
		Type:       0,
	}
	if err := svc.repo.CreateBook(ctx, book); err != nil {
		t.Fatalf("创建书籍失败: %v", err)
	}
	if err := svc.SaveChapters(ctx, book.ID, []ChapterInput{
		{Index: 0, Title: "第一章", URL: srv.URL + "/book/1/c1.html"},
	}); err != nil {
		t.Fatalf("写入章节失败: %v", err)
	}

	out, err := svc.GetContentForBook(ctx, "u1", book.ID, 0)
	if err != nil {
		t.Fatalf("取正文失败: %v", err)
	}
	if out.Type != "text" {
		t.Fatalf("类型 = %q，期望 text", out.Type)
	}
	if strings.Contains(out.Content, "<img") || strings.Contains(out.Content, "showCmt") {
		t.Fatalf("段评标记没有清干净：%q", out.Content)
	}
	if out.Content != "<p>第一段</p>\n<p>第二段</p>" {
		t.Fatalf("正文被破坏：%q", out.Content)
	}
	if len(out.Comments) != 1 {
		t.Fatalf("段评数 = %d，期望 1", len(out.Comments))
	}
	c := out.Comments[0]
	if c.Line != 0 || c.Count != 5 || c.URL != "https://v1.example.com/get_review?book_id=1" {
		t.Fatalf("段评内容不符：%+v", c)
	}
}
