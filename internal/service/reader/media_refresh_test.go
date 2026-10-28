package reader

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// 本文件：「音频直链签名票被 CDN 拒掉」后的换票回归测试。
//
// 背景：听书源（聆阅·懒人听书）的播放地址是第三方中转现签的 CDN 直链，票会被
// 上游判掉，此时 CDN 回的是**空的 401**（无 Content-Type、无 body）。旧行为把它
// 原样透传给播放器，播放器重试的又是同一个 URL，于是连续 4~8 个 401，
// App 日志表现为 ERROR_CODE_IO_BAD_HTTP_STATUS (2004)「怎么都播不了」。
//
// 现在的约定（RefreshMediaURL）：
//   - 能定位到归属章节 → 换票（先作废缓存，再重新走书源取正文）；
//   - 地址不是音轨（漫画图片、封面等）→ refreshable=false，调用方原样透传；
//   - 是音轨但换不到 → refreshable=true + err，调用方给出明确错误不再让播放器重试。

// ticketServer 模拟「中转现签 CDN 直链」的上游：
//   - /book/1   详情页（含目录链接）
//   - /toc      目录
//   - /content  章节正文，每次请求签一张新票
//   - /media    媒体本体，只认最新签出的那张票，其余回空的 401
type ticketServer struct {
	*httptest.Server
	mu      sync.Mutex
	issued  []string
	hits    int
	noToken bool // true 时正文里不带票（模拟书源不再给新地址）
}

func newTicketServer() *ticketServer {
	s := &ticketServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/book/1"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><body><h1>票源之书</h1></body></html>`))
		case r.URL.Path == "/content":
			s.mu.Lock()
			s.hits++
			n := s.hits
			noToken := s.noToken
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if noToken {
				_, _ = w.Write([]byte(`{"data":{"url":""}}`))
				return
			}
			token := fmt.Sprintf("ticket-%d", n)
			s.mu.Lock()
			s.issued = append(s.issued, token)
			s.mu.Unlock()
			_, _ = w.Write([]byte(fmt.Sprintf(`{"data":{"url":"%s/media?t=%s"}}`, s.URL, token)))
		case r.URL.Path == "/media":
			// 只认最近签出的那张票；旧票回空的 401（与腾讯 CDN 行为一致）。
			token := r.URL.Query().Get("t")
			s.mu.Lock()
			valid := len(s.issued) > 0 && s.issued[len(s.issued)-1] == token
			s.mu.Unlock()
			if !valid {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "audio/mp4")
			_, _ = w.Write([]byte("\x00\x00\x00\x20ftypmp42fake-audio-bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	return s
}

func (s *ticketServer) contentHits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

// ticketSourceJSON 一个最小听书源：正文规则取 JSON 里的播放直链。
func ticketSourceJSON(t *testing.T, base string) string {
	t.Helper()
	return `{
  "bookSourceName": "票源测试",
  "bookSourceType": 1,
  "bookSourceUrl": "` + base + `",
  "ruleContent": {"content": "$.data.url"}
}`
}

// prepareTicketBook 建好「听书源 + 书架书籍 + 一章目录」，并开独立缓存目录。
func prepareTicketBook(t *testing.T) (*ReaderService, *ticketServer, *model.ReaderBook) {
	t.Helper()
	srv := newTicketServer()
	t.Cleanup(srv.Close)

	svc, _ := newLoginTestService(t)
	svc.cfg.Cache.CacheDir = t.TempDir()
	ctx := t.Context()
	sourceID := prepareLoginSource(t, svc, ticketSourceJSON(t, srv.URL))
	book, err := svc.AddBook(ctx, readerTestUserID, SearchOrigin{
		SourceID:   sourceID,
		Origin:     srv.URL,
		OriginName: "票源测试",
		OriginType: 1,
		BookURL:    srv.URL + "/book/1",
	}, "票源之书", "某作者", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}
	if err := svc.SaveChapters(ctx, book.ID, []ChapterInput{
		{Index: 0, Title: "第一章", URL: srv.URL + "/content"},
	}); err != nil {
		t.Fatalf("保存目录失败: %v", err)
	}
	return svc, srv, book
}

// TestRefreshMediaURLIssuesNewTicket 票被拒后换票：拿到新地址、缓存被刷新。
func TestRefreshMediaURLIssuesNewTicket(t *testing.T) {
	svc, srv, book := prepareTicketBook(t)
	ctx := t.Context()

	// 首读：拿到第一张票并落缓存
	out, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0)
	if err != nil {
		t.Fatalf("首次读取失败: %v", err)
	}
	if len(out.Tracks) != 1 {
		t.Fatalf("音轨数 = %d", len(out.Tracks))
	}
	stale, err := svc.VerifyProxyURL(book.ID, trackQueryValue(t, out.Tracks[0], "u"), trackQueryValue(t, out.Tracks[0], "s"))
	if err != nil {
		t.Fatalf("还原直链失败: %v", err)
	}

	fresh, refreshable, err := svc.RefreshMediaURL(ctx, book, stale)
	if err != nil {
		t.Fatalf("换票失败: %v", err)
	}
	if !refreshable {
		t.Fatal("音轨地址应当可换票")
	}
	if fresh == stale {
		t.Fatalf("换票后地址没变: %q", fresh)
	}
	if got := srv.contentHits(); got != 2 {
		t.Fatalf("换票应回源重取一次章节，实际请求 %d 次", got)
	}

	// 新票必须能取到媒体本体（旧票是空的 401）
	resp, err := http.Get(fresh)
	if err != nil {
		t.Fatalf("新票请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("新票状态码 = %d，期望 200", resp.StatusCode)
	}

	// 缓存要被刷新成新票：再次读章节不应再回源
	out2, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0)
	if err != nil {
		t.Fatalf("换票后读章节失败: %v", err)
	}
	refreshed, err := svc.VerifyProxyURL(book.ID, trackQueryValue(t, out2.Tracks[0], "u"), trackQueryValue(t, out2.Tracks[0], "s"))
	if err != nil {
		t.Fatalf("还原刷新后的直链失败: %v", err)
	}
	if refreshed != fresh {
		t.Fatalf("章节缓存应刷新为新票 %q，实际 %q", fresh, refreshed)
	}
	if got := srv.contentHits(); got != 2 {
		t.Fatalf("缓存已刷新，不应再回源，实际请求 %d 次", got)
	}
}

// TestRefreshMediaURLMemoryReusesTicket 同一张旧票的多次换票只回源一次：
// 播放器一次失败会立刻用同一 URL 重试多次，重复回源会把书源打成限流。
func TestRefreshMediaURLMemoryReusesTicket(t *testing.T) {
	svc, srv, book := prepareTicketBook(t)
	ctx := t.Context()

	out, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0)
	if err != nil {
		t.Fatalf("首次读取失败: %v", err)
	}
	stale, _ := svc.VerifyProxyURL(book.ID, trackQueryValue(t, out.Tracks[0], "u"), trackQueryValue(t, out.Tracks[0], "s"))

	first, _, err := svc.RefreshMediaURL(ctx, book, stale)
	if err != nil {
		t.Fatalf("首次换票失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		again, refreshable, err := svc.RefreshMediaURL(ctx, book, stale)
		if err != nil || !refreshable {
			t.Fatalf("第 %d 次换票失败: refreshable=%v err=%v", i+2, refreshable, err)
		}
		if again != first {
			t.Fatalf("同一旧票应复用同一个新票：%q vs %q", again, first)
		}
	}
	if got := srv.contentHits(); got != 2 {
		t.Fatalf("重复换票只该回源一次，实际 %d 次", got)
	}
}

// TestRefreshMediaURLSkipsNonTrackURL 漫画图片/防盗链图片这类地址不属于音轨，
// 调用方应原样透传上游状态（不能被换票逻辑吃掉响应体）。
func TestRefreshMediaURLSkipsNonTrackURL(t *testing.T) {
	svc, _, book := prepareTicketBook(t)
	ctx := t.Context()

	if _, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0); err != nil {
		t.Fatalf("首次读取失败: %v", err)
	}
	_, refreshable, err := svc.RefreshMediaURL(ctx, book, "https://img.example.com/cover.jpg")
	if err != nil {
		t.Fatalf("非音轨地址不该报错: %v", err)
	}
	if refreshable {
		t.Fatal("非音轨地址不该被判为可换票")
	}
}

// TestRefreshMediaURLErrorsWhenSourceRepeatsTicket 书源重新签出同一张票时，
// 说明中转本身没换票，应当明确失败而不是让播放器继续对着死票重试。
func TestRefreshMediaURLErrorsWhenSourceRepeatsTicket(t *testing.T) {
	svc, srv, book := prepareTicketBook(t)
	ctx := t.Context()

	out, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0)
	if err != nil {
		t.Fatalf("首次读取失败: %v", err)
	}
	stale, _ := svc.VerifyProxyURL(book.ID, trackQueryValue(t, out.Tracks[0], "u"), trackQueryValue(t, out.Tracks[0], "s"))

	// 让书源此后只回空地址：换票必然失败。
	srv.mu.Lock()
	srv.noToken = true
	srv.mu.Unlock()

	_, refreshable, err := svc.RefreshMediaURL(ctx, book, stale)
	if !refreshable {
		t.Fatal("音轨地址应当被判为可换票")
	}
	if err == nil {
		t.Fatal("书源给不出新票时应当报错")
	}
	// 作废过的缓存不该把死票留在库里
	if _, ok := svc.loadCachedContent(ctx, nil, nil, book, model.ReaderChapter{
		Index: 0, Title: "第一章", URL: srv.URL + "/content",
	}, "audio"); ok {
		t.Fatal("换票失败后旧缓存应已被作废，不应继续下发死票")
	}
}
