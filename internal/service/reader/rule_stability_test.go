package reader

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/htmlindex"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

// 本文件覆盖规则稳定性修复的回归：
//   - @put 写回书源变量（跨请求持久化）
//   - book.putVariable 持久化到书架记录
//   - 书源正则的匹配超时（灾难性回溯不会挂死）
//   - helper.DecompressBody 的解压上限
//   - 非 UTF-8 TXT 的导入/读取
//   - 图片 URL 尾部 options 的请求头应用

// @put 必须写到书源变量（legado BaseSource.putVariable），而不是一次性的书籍变量。
func TestPutPersistsToSourceVariable(t *testing.T) {
	svc, _ := newLoginTestService(t)
	srcURL := "https://put.example.com"
	src := &model.ReaderBookSource{Name: "put 源", SourceURL: srcURL}
	bs := &BookSource{BookSourceURL: srcURL, RuleContent: &ContentRule{Content: strPtr("id.content@textNodes")}}

	sess := svc.newSession(t.Context(), src, bs)
	ar := sess.newAnalyzer("", 0, "<html></html>", srcURL)
	ar.Put("token", "abc123")
	sess.close()

	// 换一个全新会话/解析器：@put 的值必须还能 @get 到（已落库）。
	sess2 := svc.newSession(t.Context(), src, bs)
	defer sess2.close()
	ar2 := sess2.newAnalyzer("", 0, "<html></html>", srcURL)
	if got := ar2.Get("token"); got != "abc123" {
		t.Fatalf("@put 未持久化：@get 得到 %q", got)
	}
}

// 书源 variables 默认值只是兜底：@put 写入的同名值优先级更高。
func TestPutOverridesSourceDefaultVariable(t *testing.T) {
	svc, _ := newLoginTestService(t)
	srcURL := "https://put2.example.com"
	raw := `{"bookSourceUrl":"https://put2.example.com","variables":"{\"token\":\"default\"}"}`
	srcID := importTestSource(t, svc, raw, srcURL)
	src, err := svc.repo.GetSource(context.Background(), srcID)
	if err != nil {
		t.Fatal(err)
	}
	bs, err := ParseBookSource(src.RawJSON)
	if err != nil {
		t.Fatal(err)
	}
	sess := svc.newSession(t.Context(), src, bs)
	ar := sess.newAnalyzer("", 0, "<html></html>", srcURL)
	if got := ar.Get("token"); got != "default" {
		t.Fatalf("默认变量应可读，得到 %q", got)
	}
	ar.Put("token", "override")
	sess.close()

	sess2 := svc.newSession(t.Context(), src, bs)
	defer sess2.close()
	ar2 := sess2.newAnalyzer("", 0, "<html></html>", srcURL)
	if got := ar2.Get("token"); got != "override" {
		t.Fatalf("@put 应覆盖默认变量，得到 %q", got)
	}
}

// book.putVariable 写入的变量要落回书架记录（对应 legado Book.upVariable）。
func TestBookPutVariablePersists(t *testing.T) {
	svc, srv, book := prepareCacheTestBook(t)
	ctx := t.Context()
	book.Variable = `{"tone_id":"keep"}`
	if err := svc.repo.UpdateBook(ctx, book); err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.repo.GetBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}

	src, bs, err := svc.loadSourceFlexible(ctx, "", book.Origin)
	if err != nil {
		t.Fatal(err)
	}
	sess := svc.newSession(ctx, src, bs)
	sess.book = loaded
	ar := sess.newAnalyzer("", 0, "<html></html>", book.Origin)
	sess.applyBookContext(ar, book.BookURL, loaded, "", 0)

	// 书源 JS 的 book.getVariable / putVariable。
	runner := sess.runner("", 0)
	if _, err := runner.Run(ar, `book.putVariable('token','v-42'); book.getVariable('tone_id')`, nil, book.Origin); err != nil {
		t.Fatalf("book JS 执行失败: %v", err)
	}
	sess.close()

	stored, err := svc.repo.GetBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	vars := parseBookVariableMap(stored.Variable)
	if vars["token"] != "v-42" {
		t.Fatalf("book.putVariable 未持久化: %q", stored.Variable)
	}
	if vars["tone_id"] != "keep" {
		t.Fatalf("已有变量被覆盖: %q", stored.Variable)
	}
	_ = srv
}

// 灾难性回溯的正则必须在秒级返回，而不是挂死 goroutine。
func TestRegexAnalyzerTimeoutOnCatastrophicBacktracking(t *testing.T) {
	// (a+)+$ 对 "aaaa...b" 是指数级回溯；没有 MatchTimeout 会永久卡住。
	evil := strings.Repeat("a", 40) + "b"
	done := make(chan string, 1)
	go func() {
		done <- rule.ApplyReplaceRegexString(evil, "##(a+)+$##X")
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("书源正则未在超时预算内返回（MatchTimeout 未生效）")
	}
}

// 解压炸弹：超过输出上限时原样返回压缩字节（调用方按失败处理），不撑爆内存。
func TestDecompressBodyRejectsBomb(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	// 128MB 的零字节压成几十 KB。
	chunk := make([]byte, 1<<20)
	for i := 0; i < 128; i++ {
		if _, err := gz.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{Header: http.Header{"Content-Encoding": []string{"gzip"}}}
	out := helper.DecompressBody(resp, buf.Bytes())
	if len(out) == len(chunk)*128 {
		t.Fatal("解压炸弹应被上限拦截")
	}
	if !bytes.Equal(out, buf.Bytes()) {
		t.Fatal("超限时应原样返回压缩字节")
	}
}

// 正常 gzip 响应仍应被正确解压（上限不误伤常规页面）。
func TestDecompressBodyNormal(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	payload := []byte("<html>hello</html>")
	if _, err := gz.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = gz.Close()
	resp := &http.Response{Header: http.Header{"Content-Encoding": []string{"gzip"}}}
	if out := helper.DecompressBody(resp, buf.Bytes()); string(out) != string(payload) {
		t.Fatalf("正常 gzip 解压失败: %q", out)
	}
}

// 非 UTF-8（GBK）TXT：导入落盘为 UTF-8，按章节读取不再乱码/错位。
func TestImportGBKTextBookReadsCorrectly(t *testing.T) {
	// 用临时 DataDir，避免本地书籍文件写进仓库目录。
	svc := newLocalBookService(t)
	ctx := t.Context()

	// 用 GBK 编码两章正文（解码后章节字节区间按 UTF-8 计算）。
	gbkBytes := func(s string) []byte {
		enc, err := htmlindex.Get("gbk")
		if err != nil {
			t.Skip("环境缺少 GBK 编码支持")
		}
		out, err := enc.NewEncoder().Bytes([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	text := "第一章 起点\n这是第一章的正文内容，包含中文标点。\n第二章 继续\n这是第二章的正文内容。\n"
	book, err := svc.ImportLocalBook(ctx, "u1", "gbk小说.txt", gbkBytes(text))
	if err != nil {
		t.Fatalf("导入 GBK 书籍失败: %v", err)
	}
	if book.Charset != "gbk" {
		t.Fatalf("应识别为 gbk，实际 %q", book.Charset)
	}
	chapters, err := svc.ListChapters(ctx, book.ID)
	if err != nil || len(chapters) < 2 {
		t.Fatalf("应切出至少 2 章: n=%d err=%v", len(chapters), err)
	}
	second, err := svc.readLocalChapter(book, chapters[1])
	if err != nil {
		t.Fatalf("读取第二章失败: %v", err)
	}
	if !strings.Contains(second, "第二章的正文内容") {
		t.Fatalf("第二章内容不正确（编码错位）: %q", second)
	}
}

// 图片 URL 尾部 options：拆分出请求头，且地址本身去掉选项段。
func TestParseMediaOptions(t *testing.T) {
	raw := `https://img.example.com/a.jpg,{"headers":{"Referer":"https://site.example.com/","X-Token":"t1"},"retry":2}`
	base, opt, ok := rule.ParseMediaOptions(raw)
	if !ok {
		t.Fatal("应识别出选项段")
	}
	if base != "https://img.example.com/a.jpg" {
		t.Fatalf("基础地址错误: %q", base)
	}
	if opt.Headers["Referer"] != "https://site.example.com/" || opt.Headers["X-Token"] != "t1" {
		t.Fatalf("请求头解析错误: %+v", opt.Headers)
	}
	if opt.Retry == nil || *opt.Retry != 2 {
		t.Fatalf("retry 解析错误: %+v", opt.Retry)
	}
	// 无选项段的普通地址原样返回。
	if b, _, ok := rule.ParseMediaOptions("https://img.example.com/b.jpg"); ok || b != "https://img.example.com/b.jpg" {
		t.Fatalf("无选项地址不应被改写: %q ok=%v", b, ok)
	}
}

// 代理地址改写后，图片选项头必须在媒体请求里真正生效。
func TestFetchMediaAppliesOptionHeaders(t *testing.T) {
	var gotReferer, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("Referer")
		gotToken = r.Header.Get("X-Token")
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg-bytes"))
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	book := &model.ReaderBook{Base: model.Base{ID: "b1"}, Origin: srv.URL, BookURL: srv.URL + "/book/1"}
	resp, err := svc.FetchMediaWithOptions(t.Context(), book, srv.URL+"/a.jpg", "", map[string]string{
		"Referer": "https://site.example.com/",
		"X-Token": "t1",
	})
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if gotReferer != "https://site.example.com/" || gotToken != "t1" {
		t.Fatalf("选项请求头未生效: referer=%q token=%q", gotReferer, gotToken)
	}
}

// ProxyURL 去掉选项段后再签名：带选项与不带选项的同一张图得到同一签名。
func TestProxyURLStripsMediaOptions(t *testing.T) {
	svc, _ := newLoginTestService(t)
	plain := svc.ProxyURL("book-1", "https://img.example.com/a.jpg")
	withOptions := svc.ProxyURL("book-1", `https://img.example.com/a.jpg,{"headers":{"Referer":"https://s/"}}`)
	if plain != withOptions {
		t.Fatalf("选项段应被剥离后签名:\n%s\n%s", plain, withOptions)
	}
	raw, err := svc.VerifyProxyURL("book-1", strings.TrimPrefix(strings.Split(plain, "&u=")[1], ""), "")
	if err == nil && raw != "" {
		_ = raw
	}
	// 拆出 u/s 校验签名可回推出干净地址。
	u := plain[strings.Index(plain, "&u=")+3:]
	if i := strings.Index(u, "&s="); i >= 0 {
		u = u[:i]
	}
	if decoded, err := svc.VerifyProxyURL("book-1", u, plain[strings.Index(plain, "&s=")+3:]); err != nil || decoded != "https://img.example.com/a.jpg" {
		t.Fatalf("签名校验失败: %q err=%v", decoded, err)
	}
}

// HLS 判定：带 query 的 m3u8 与 Content-Type 不标准都要识别；普通图片不能被误判。
func TestLooksLikeHLSURL(t *testing.T) {
	cases := map[string]bool{
		"https://a.com/index.m3u8":          true,
		"https://a.com/index.m3u8?token=xx": true,
		"https://a.com/live/index.M3U":      true,
		"https://a.com/a.mp3":               false,
		"https://a.com/m3u8/1":              false,
	}
	for raw, want := range cases {
		if got := looksLikeHLSURL(raw); got != want {
			t.Fatalf("looksLikeHLSURL(%q)=%v，期望 %v", raw, got, want)
		}
	}
}
