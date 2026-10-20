package reader

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 本文件：书籍类型兜底的回归测试。
//
// 背景：书籍详情页的「加入书架/开始阅读」曾把 origin_type 写死成 0（文本），
// 于是听书源的音频书以文本类型落库。正文类型按「规则 JS 声明 > 书架类型 >
// 书源类型」取值，书架里的 0 压过书源的 1，播放直链就被当成正文排出来，
// 整个阅读页是一屏 URL（用户截图里的现象）。
//
// 兜底规则：书架类型还停在默认的「文本」时用书源类型；规则 JS 显式声明的
// 类型仍然最优先，避免改坏「文本型聚合源提供听书内容」那类源。

// audioTestSourceJSON 一个最小听书源：正文规则给 JSON 里的播放直链。
func audioTestSourceJSON(t *testing.T, base string) string {
	t.Helper()
	return `{
  "bookSourceName": "听书测试源",
  "bookSourceType": 1,
  "bookSourceUrl": "` + base + `",
  "ruleContent": {"content": "$.data.url"}
}`
}

// TestAudioBookTypeFallbackBySource 书架类型是默认的文本时按书源类型（音频）渲染，
// 并把纠正后的类型写回书架记录。
func TestAudioBookTypeFallbackBySource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"data":{"url":"https://cdn.example.com/a.m4a"}}`))
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	sourceID := prepareLoginSource(t, svc, audioTestSourceJSON(t, srv.URL))

	// 复刻详情页写死的 origin_type=0
	book, err := svc.AddBook(ctx, readerTestUserID, SearchOrigin{
		SourceID:   sourceID,
		Origin:     srv.URL,
		OriginName: "听书测试源",
		OriginType: 0,
		BookURL:    srv.URL + "/book/1",
	}, "测试听书", "某作者", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}
	if err := svc.SaveChapters(ctx, book.ID, []ChapterInput{
		{Index: 0, Title: "第一章", URL: srv.URL + "/content"},
	}); err != nil {
		t.Fatalf("保存目录失败: %v", err)
	}

	out, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0)
	if err != nil {
		t.Fatalf("取正文失败: %v", err)
	}
	if out.Type != "audio" {
		t.Fatalf("正文类型 = %q（期望 audio：书架类型是默认文本时应当按书源类型兜底），正文 = %.120q",
			out.Type, out.Content)
	}
	if len(out.Tracks) != 1 {
		t.Fatalf("音轨数 = %d，期望 1", len(out.Tracks))
	}
	// 音轨要落到签名代理上（浏览器播放带不上书源的防盗链头）
	if !strings.HasPrefix(out.Tracks[0], "/api/reader/media?") {
		t.Fatalf("音轨 = %q，期望签名代理地址", out.Tracks[0])
	}
	raw, err := svc.VerifyProxyURL(book.ID, trackQueryValue(t, out.Tracks[0], "u"), trackQueryValue(t, out.Tracks[0], "s"))
	if err != nil {
		t.Fatalf("代理地址校验失败: %v", err)
	}
	if raw != "https://cdn.example.com/a.m4a" {
		t.Fatalf("代理还原地址 = %q", raw)
	}

	// 纠正后的类型要写回书架：否则每读一章都要再兜底一次，注入规则 JS 的
	// book.type 也一直是错的（书源 JS 会读它分支）。
	reloaded, err := svc.GetBook(ctx, book.ID)
	if err != nil {
		t.Fatalf("重读书籍失败: %v", err)
	}
	if reloaded.Type != 1 {
		t.Fatalf("书架记录类型 = %d，期望 1（音频）", reloaded.Type)
	}
}

// TestDeclaredTextTypeWinsOverAudioSource 规则 JS 显式声明类型时不套用书源类型兜底：
// 文本型聚合源（bookSourceType=0，但听书内容靠 JS 声明）反过来也不会被覆盖成文本。
func TestDeclaredTextTypeWinsOverAudioSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"data":{"url":"https://cdn.example.com/a.m4a"}}`))
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	// 音频书源，但正文规则用 JS 把 book.type 声明成文本（legado 的 BookType 位掩码 8）
	src := `{
  "bookSourceName": "声明文本的音频源",
  "bookSourceType": 1,
  "bookSourceUrl": "` + srv.URL + `",
  "ruleContent": {"content": "@js:book.type = 8; result"}
}`
	sourceID := prepareLoginSource(t, svc, src)

	book, err := svc.AddBook(ctx, readerTestUserID, SearchOrigin{
		SourceID: sourceID, Origin: srv.URL, OriginName: "声明文本的音频源",
		OriginType: 0, BookURL: srv.URL + "/book/1",
	}, "声明文本", "某作者", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}
	if err := svc.SaveChapters(ctx, book.ID, []ChapterInput{
		{Index: 0, Title: "第一章", URL: srv.URL + "/content"},
	}); err != nil {
		t.Fatalf("保存目录失败: %v", err)
	}

	out, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0)
	if err != nil {
		t.Fatalf("取正文失败: %v", err)
	}
	if out.Type != "text" {
		t.Fatalf("正文类型 = %q，期望 text（规则 JS 声明的类型优先于书源类型）", out.Type)
	}
}

// trackQueryValue 取出代理地址里的查询参数值（测试用）。
func trackQueryValue(t *testing.T, raw, key string) string {
	t.Helper()
	idx := strings.Index(raw, key+"=")
	if idx < 0 {
		t.Fatalf("地址 %q 里没有参数 %s", raw, key)
	}
	rest := raw[idx+len(key)+1:]
	if end := strings.Index(rest, "&"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}
