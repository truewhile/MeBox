package handler

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
	"github.com/truewhile/MeBox/internal/service/reader"
)

// 媒体代理在「上游签名票被拒」时的行为契约。
//
// 背景（真实故障）：聆阅·懒人听书源的音频直链是第三方中转现签的腾讯 CDN 票，
// 票被 CDN 判掉时上游回的是**空的 401**（无 Content-Type、无 body）。旧行为把它
// 原样透传给播放器，播放器重试的还是同一个 URL，于是 App 日志里连续 4~8 个
// ERROR_CODE_IO_BAD_HTTP_STATUS (2004)，表现为「这本书怎么都播不了」。
//
// 现在的契约：
//   - 能换票 → 用新票重拉，直接返回媒体本体（第一个请求就被治好）；
//   - 换不到 → 502 + 明确文案，而不是把 401 丢给播放器；
//   - 非音轨地址（漫画图片防盗链等）→ 维持原样透传，不改变既有行为。

func TestReaderMediaPayloadRejected(t *testing.T) {
	cases := []struct {
		name        string
		head        string
		contentType string
		want        bool
	}{
		{"HTML 错误页", "<!DOCTYPE html><html><body>403</body></html>", "text/html; charset=utf-8", true},
		{"JSON 报错", `{"code":403,"message":"expired"}`, "application/json", true},
		{"数组形态报错", `[{"error":"bad ticket"}]`, "application/json; charset=utf-8", true},
		{"纯文本报错", "ticket expired", "text/plain", false},
		{"正常 m4a", "\x00\x00\x00\x20ftypmp42", "audio/mp4", false},
		{"正常 mp3", "ID3\x04\x00", "audio/mpeg", false},
		{"正常图片", "\xff\xd8\xff\xe0", "image/jpeg", false},
		{"播放列表", "#EXTM3U\n#EXTINF:10,", "application/vnd.apple.mpegurl", false},
		{"空体", "", "text/html", false},
		{"HTML 类型但二进制体", "\x00\x00\x00\x20ftypmp42", "text/html", false},
	}
	for _, c := range cases {
		got := readerMediaPayloadRejected([]byte(c.head), c.contentType)
		if got != c.want {
			t.Errorf("%s: readerMediaPayloadRejected(%q, %q) = %v，期望 %v",
				c.name, c.head, c.contentType, got, c.want)
		}
	}
}

// TestReaderMediaProxyHealsStaleTicket 票被上游拒绝时代理自己换票重拉，
// 第一个请求就返回媒体本体（不必等播放器重试）。
func TestReaderMediaProxyHealsStaleTicket(t *testing.T) {
	svc, srv, book := prepareMediaTicketBook(t)
	router := mediaRouterForTest(svc)

	signed := primeTicketBook(t, svc, book.ID)
	// 让中转再签一张新票：章节缓存里那张就此变成「旧票」。
	srv.rotate()

	code, body := getReaderMedia(t, router, signed)
	if code != http.StatusOK {
		t.Fatalf("换票重试后 status=%d body=%s", code, body)
	}
	if !strings.Contains(body, "fake-audio-bytes") {
		t.Fatalf("响应体不是媒体: %q", body)
	}
	if got := srv.contentHits(); got < 2 {
		t.Fatalf("应当回源重取章节，实际请求 %d 次", got)
	}
}

// TestReaderMediaProxyFailsClearlyWhenRefreshImpossible 换不到票时给 502 + 明确文案，
// 而不是把 401 透传给播放器（那样播放器只会对着必然失败的地址反复重试）。
func TestReaderMediaProxyFailsClearlyWhenRefreshImpossible(t *testing.T) {
	svc, srv, book := prepareMediaTicketBook(t)
	router := mediaRouterForTest(svc)

	signed := primeTicketBook(t, svc, book.ID)
	srv.rotate()
	srv.stopIssuing()

	code, body := getReaderMedia(t, router, signed)
	if code != http.StatusBadGateway {
		t.Fatalf("换票失败应当返回 502，实际 %d body=%s", code, body)
	}
	if !strings.Contains(body, "签名失效") {
		t.Fatalf("错误文案应说明是音源签名失效: %s", body)
	}
}

// TestReaderMediaProxyStopsResendingRejectedTicket 上游成批拒绝时（连新签的票
// 也是空 401），不能在播放器那一次请求里把同一张被拒的票打上十几遍：连续两轮
// 换票都是同一个状态码就立刻失败，把明确文案交出去。
func TestReaderMediaProxyStopsResendingRejectedTicket(t *testing.T) {
	svc, srv, book := prepareMediaTicketBook(t)
	router := mediaRouterForTest(svc)

	signed := primeTicketBook(t, svc, book.ID)
	srv.denyAlways()

	code, body := getReaderMedia(t, router, signed)
	if code != http.StatusBadGateway {
		t.Fatalf("上游持续拒绝应返回 502，实际 %d body=%s", code, body)
	}
	if !strings.Contains(body, "401") {
		t.Fatalf("错误文案应带上上游状态码: %s", body)
	}
	// 每轮 fetchReaderMedia = 首拉(401) + 换票后重拉(401)，两轮即收手。
	if got := srv.mediaRequests(); got > 4 {
		t.Fatalf("无进展的拒绝应当两轮就收手，实际上游请求 %d 次", got)
	}
}

// TestReaderAudioCacheResumesTruncatedUpstream 上游中途 RST 后用 Range 续传：
// 播放器最终拿到的仍是完整的一章，而不是从头重下。
func TestReaderAudioCacheResumesTruncatedUpstream(t *testing.T) {
	const bodyBytes = 300 << 10
	payload := bytes.Repeat([]byte("m4a!"), bodyBytes/4)
	var ranges []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		count := len(ranges)
		mu.Unlock()

		w.Header().Set("Content-Type", "audio/mp4")
		w.Header().Set("Accept-Ranges", "bytes")
		start := 0
		if count > 1 {
			// 第二次起按 Range 续传。
			start = bodyBytes / 2
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, bodyBytes-1, bodyBytes))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(bodyBytes))
		}
		chunk := payload[start:]
		if count == 1 {
			// 第一次只写一半就断开。
			chunk = payload[:bodyBytes/2]
		}
		for written := 0; written < len(chunk); written += 8 << 10 {
			end := written + 8<<10
			if end > len(chunk) {
				end = len(chunk)
			}
			if _, err := w.Write(chunk[written:end]); err != nil {
				return
			}
		}
		w.(http.Flusher).Flush()
		if count == 1 {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
				}
			}
		}
	}))
	defer srv.Close()

	svc, _, book := prepareMediaTicketBookWithUpstream(t, srv.URL)
	router := mediaRouterForTest(svc)
	signed := svc.Reader.ProxyURL(book.ID, srv.URL+"/media")

	code, body := getReaderMedia(t, router, signed)
	if code != http.StatusOK {
		t.Fatalf("续传后应当成功，实际 status=%d body=%.120s", code, body)
	}
	if len(body) != bodyBytes {
		t.Fatalf("交付字节 = %d，期望 %d", len(body), bodyBytes)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ranges) < 2 || ranges[0] != "" {
		t.Fatalf("请求序列 = %q，期望先整段再续传", ranges)
	}
	if !strings.HasPrefix(ranges[1], "bytes=") {
		t.Fatalf("第二轮应当带 Range，实际 %q", ranges[1])
	}
}

// TestReaderMediaProxyPassesThroughNonTrackError 漫画图片这类不可换票的地址，
// 上游 4xx 仍按原状态透传（既有行为不变）。
func TestReaderMediaProxyPassesThroughNonTrackError(t *testing.T) {
	svc, srv, book := prepareMediaTicketBook(t)
	router := mediaRouterForTest(svc)

	// 书源域名下的图片地址：不在任何音轨缓存里。
	imgURL := srv.URL + "/img/forbidden.jpg"
	signed := svc.Reader.ProxyURL(book.ID, imgURL)

	code, body := getReaderMedia(t, router, signed)
	if code == http.StatusBadGateway || code == http.StatusUnauthorized {
		t.Fatalf("非音轨地址不该走换票分支，实际 %d body=%s", code, body)
	}
	if code != http.StatusForbidden {
		t.Fatalf("图片 403 应原样透传，实际 %d body=%s", code, body)
	}
	if got := srv.contentHits(); got != 0 {
		t.Fatalf("图片失败不该触发回源取章节，实际 %d 次", got)
	}
}

// TestReaderMediaProxyRejectsNonMediaPayload 上游用 200 回一段 HTML/JSON 错误页时，
// 代理直接判失败，不把网页喂给播放器（否则报「格式不支持」）。
func TestReaderMediaProxyRejectsNonMediaPayload(t *testing.T) {
	svc, srv, book := prepareMediaTicketBook(t)
	router := mediaRouterForTest(svc)

	signed := primeTicketBook(t, svc, book.ID)
	srv.serveErrorPage()

	code, body := getReaderMedia(t, router, signed)
	if code != http.StatusBadGateway {
		t.Fatalf("非媒体内容应当返回 502，实际 %d body=%.160s", code, body)
	}
	if !strings.Contains(body, "非媒体内容") {
		t.Fatalf("错误文案应说明上游返回了非媒体内容: %s", body)
	}
}

// ─── 上游抖动：连接被 reset 时的重试与截断可见化 ─────────────────────────

// TestReaderMediaProbeRetriesConnectionReset 上游在响应体中途 RST 连接时，
// 代理要退避重试，而不是第一次读失败就回 502。
//
// 真实故障：腾讯 CDN 的跨境边缘在并发/长传输时会直接 RST，同一章重试一次
// 往往就好。以前这里读 body 失败即判死，把一次可恢复的抖动变成「这本书播不了」。
func TestReaderMediaProbeRetriesConnectionReset(t *testing.T) {
	var attempts int32
	const bodyBytes = 200 << 10
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			// 第一次：声明长度，写一小段后直接断开连接（模拟 RST/提前 EOF）。
			w.Header().Set("Content-Type", "audio/mp4")
			w.Header().Set("Content-Length", strconv.Itoa(bodyBytes))
			_, _ = w.Write([]byte("\x00\x00\x00\x20ftypmp42"))
			w.(http.Flusher).Flush()
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		w.Header().Set("Content-Type", "audio/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(bodyBytes))
		payload := make([]byte, 8<<10)
		for written := 0; written < bodyBytes; written += len(payload) {
			if _, err := w.Write(payload); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	svc, _, book := prepareMediaTicketBookWithUpstream(t, srv.URL)
	router := mediaRouterForTest(svc)
	signed := svc.Reader.ProxyURL(book.ID, srv.URL+"/media")

	code, body := getReaderMedia(t, router, signed)
	if code != http.StatusOK {
		t.Fatalf("连接被 reset 后应当重试成功，实际 status=%d body=%.200s", code, body)
	}
	if int32(len(body)) != bodyBytes {
		t.Fatalf("交付字节 = %d，期望 %d", len(body), bodyBytes)
	}
	if got := atomic.LoadInt32(&attempts); got < 2 {
		t.Fatalf("应当重试过，实际上游请求 %d 次", got)
	}
}

// TestReaderMediaAbortsWhenUpstreamTruncates 非音轨的流式透传路径：上游中途
// 断开且重试都失败时，必须让客户端看到「传输不完整」，不能返回一个长度正确
// 却内容缺失的响应。
//
// 以前这里是 `_, _ = io.Copy(...)`，错误被丢掉，客户端拿到一个正常结束的
// 半截文件 —— 播放器只能报「格式不支持」（3003），根因被彻底掩盖。
//
// 音轨走的是整段落盘缓存（见 TestReaderAudioCacheRejectsTruncatedUpstream）：
// 拉不完整就重试，全部失败回 502，绝不会把半截音频交给播放器。
func TestReaderMediaAbortsWhenUpstreamTruncates(t *testing.T) {
	const declared = 400 << 10
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(declared))
		// 先写够探测用的前缀，再断开：探测能过，透传阶段才断。
		payload := make([]byte, 8<<10)
		for written := 0; written < 128<<10; written += len(payload) {
			if _, err := w.Write(payload); err != nil {
				return
			}
		}
		w.(http.Flusher).Flush()
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer srv.Close()

	svc, _, book := prepareMediaTicketBookWithUpstream(t, srv.URL)
	router := mediaRouterForTest(svc)
	// .mp4 不是音频后缀：走流式透传分支（音轨会被整段落盘缓存）。
	signed := svc.Reader.ProxyURL(book.ID, srv.URL+"/media.mp4")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, signed, nil)
	// panic(http.ErrAbortHandler) 由 net/http 捕获；httptest 下会向上冒泡，
	// 这里断言它确实发生了（即「没有静默地把半截文件当成功」）。
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("上游截断时必须中止响应（panic(http.ErrAbortHandler)），实际正常返回了 %d 字节",
				w.Body.Len())
		}
		if recovered != http.ErrAbortHandler {
			t.Fatalf("中止响应时应使用 http.ErrAbortHandler，实际 %v", recovered)
		}
	}()
	router.ServeHTTP(w, req)
	if w.Body.Len() >= declared {
		t.Fatalf("不应交付完整长度的响应体（上游只写了 128KB）")
	}
}

// TestReaderAudioCacheServesFromDisk 音轨首次拉取后落盘；后续请求即使带着一张
// 新签的票（query 全变了）也不再回源——这是让播放不受跨境链路抖动影响的关键。
func TestReaderAudioCacheServesFromDisk(t *testing.T) {
	svc, srv, book := prepareMediaTicketBook(t)
	router := mediaRouterForTest(svc)
	signed := primeTicketBook(t, svc, book.ID)

	code, body := getReaderMedia(t, router, signed)
	if code != http.StatusOK || !strings.Contains(body, "fake-audio-bytes") {
		t.Fatalf("首次拉取失败 status=%d body=%q", code, body)
	}
	if got := srv.mediaRequests(); got != 1 {
		t.Fatalf("首次拉取应对上游请求 1 次，实际 %d", got)
	}

	// 同一段音频的另一张票：URL 的 query 不同，缓存键必须一致。
	rotated := svc.Reader.ProxyURL(book.ID, srv.URL+"/media?t=ticket-rotated")
	code, body = getReaderMedia(t, router, rotated)
	if code != http.StatusOK || !strings.Contains(body, "fake-audio-bytes") {
		t.Fatalf("命中缓存失败 status=%d body=%q", code, body)
	}
	if got := srv.mediaRequests(); got != 1 {
		t.Fatalf("命中缓存后不该再回源，实际上游请求 %d 次", got)
	}
}

// TestReaderAudioCacheRejectsTruncatedUpstream 音轨拉不完整时重试；全部失败回 502，
// 且不能留下半截缓存文件（否则后续会把截断的音频当完整文件下发）。
func TestReaderAudioCacheRejectsTruncatedUpstream(t *testing.T) {
	const declared = 400 << 10
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(declared))
		payload := make([]byte, 8<<10)
		for written := 0; written < 128<<10; written += len(payload) {
			if _, err := w.Write(payload); err != nil {
				return
			}
		}
		w.(http.Flusher).Flush()
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer srv.Close()

	svc, _, book := prepareMediaTicketBookWithUpstream(t, srv.URL)
	router := mediaRouterForTest(svc)
	// 无后缀地址 + 音频书籍 → 走音轨落盘缓存。
	signed := svc.Reader.ProxyURL(book.ID, srv.URL+"/media")

	code, body := getReaderMedia(t, router, signed)
	if code != http.StatusBadGateway {
		t.Fatalf("上游持续截断应当返回 502，实际 %d body=%.120s", code, body)
	}
	if strings.Contains(body, "ftyp") {
		t.Fatalf("不能把半截音频交给客户端: %q", body)
	}
	if _, _, ok := svc.Reader.OpenCachedRemoteAudio(book.ID, srv.URL+"/media"); ok {
		t.Fatalf("失败的下载不应留下缓存文件")
	}
}

func mediaRouterForTest(svc *service.Container) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// /api/reader/media 挂在公共 api 组（签名鉴权，不需要 JWT），单独注册即可。
	r.GET("/api/reader/media", readerMediaProxyHandler(svc))
	return r
}

func getReaderMedia(t *testing.T, router *gin.Engine, signedPath string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, signedPath, nil)
	router.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// primeTicketBook 先读一次章节（签出第一张票并落缓存），返回该音轨的签名代理路径。
func primeTicketBook(t *testing.T, svc *service.Container, bookID string) string {
	t.Helper()
	out, err := svc.Reader.GetContentForBook(t.Context(), "u1", bookID, 0)
	if err != nil {
		t.Fatalf("首次取章节失败: %v", err)
	}
	if len(out.Tracks) != 1 {
		t.Fatalf("音轨数 = %d，期望 1", len(out.Tracks))
	}
	return out.Tracks[0]
}

// ─── 模拟「中转现签 CDN 直链」的上游 ───────────────────────────────────────

// mediaTicketServer：
//   - /book/1  详情页
//   - /content 章节正文，每次请求签一张新票
//   - /media   媒体本体，只认最新签出的票；旧票回**空的 401**（与腾讯 CDN 一致）
//   - /img/*   图片资源，固定 403（模拟防盗链）
type mediaTicketServer struct {
	*httptest.Server
	mu         sync.Mutex
	issued     []string
	hits       int
	mediaHits  int
	noToken    bool
	errorPage  bool
	alwaysDeny bool
}

func newMediaTicketServer(t *testing.T) *mediaTicketServer {
	t.Helper()
	s := &mediaTicketServer{}
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
			token := s.newTokenLocked(fmt.Sprintf("ticket-%d", n))
			_, _ = w.Write([]byte(fmt.Sprintf(`{"data":{"url":"%s/media?t=%s"}}`, s.URL, token)))
		case r.URL.Path == "/media":
			s.mu.Lock()
			s.mediaHits++
			deny := s.alwaysDeny
			s.mu.Unlock()
			if deny {
				// 与腾讯 CDN 一致：连新签的票也直接空 401（成批拒绝的那几分钟）。
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if s.errorPageNow() {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(`<html><body>ticket expired</body></html>`))
				return
			}
			token := r.URL.Query().Get("t")
			s.mu.Lock()
			valid := len(s.issued) > 0 && s.issued[len(s.issued)-1] == token
			s.mu.Unlock()
			if !valid {
				// 与上游一致：空 body、无 Content-Type。
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "audio/mp4")
			_, _ = w.Write([]byte("\x00\x00\x00\x20ftypmp42fake-audio-bytes"))
		case strings.HasPrefix(r.URL.Path, "/img/"):
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// newTokenLocked 签一张新票（调用方需持有或随后获取锁；这里内部加锁）。
func (s *mediaTicketServer) newTokenLocked(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issued = append(s.issued, token)
	return token
}

// rotate 模拟「中转又签了一张新票」：章节缓存里那张就此失效。
func (s *mediaTicketServer) rotate() {
	s.mu.Lock()
	s.issued = append(s.issued, fmt.Sprintf("rotated-%d", len(s.issued)))
	s.mu.Unlock()
}

// stopIssuing 让书源此后给不出播放地址（模拟中转本身挂了）。
func (s *mediaTicketServer) stopIssuing() {
	s.mu.Lock()
	s.noToken = true
	s.mu.Unlock()
}

// serveErrorPage 让媒体端点改用 200 + HTML 错误页（直链失效的另一种形态）。
func (s *mediaTicketServer) serveErrorPage() {
	s.mu.Lock()
	s.errorPage = true
	s.mu.Unlock()
}

// denyAlways 让媒体端点连新签的票也一律空 401（模拟上游成批拒绝的那几分钟）。
func (s *mediaTicketServer) denyAlways() {
	s.mu.Lock()
	s.alwaysDeny = true
	s.mu.Unlock()
}

func (s *mediaTicketServer) errorPageNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errorPage
}

func (s *mediaTicketServer) contentHits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *mediaTicketServer) mediaRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mediaHits
}

// prepareMediaTicketBookWithUpstream 用指定的上游地址建一本书（上游由调用方提供）。
func prepareMediaTicketBookWithUpstream(t *testing.T, upstream string) (*service.Container, *mediaTicketServer, *model.ReaderBook) {
	t.Helper()
	svc := newReaderHandlerContainer(t)
	svc.Cfg.Cache.CacheDir = t.TempDir()
	// 占位 ticket server：只用来满足清理钩子，不参与请求。
	srv := newMediaTicketServer(t)
	srcJSON := fmt.Sprintf(`{
  "bookSourceName": "上游直连测试",
  "bookSourceType": 1,
  "bookSourceUrl": %q,
  "ruleContent": {"content": "$.data.url"}
}`, upstream)
	if _, err := svc.Reader.ImportSources(t.Context(), "u1", srcJSON); err != nil {
		t.Fatalf("导入书源失败: %v", err)
	}
	srcs, err := svc.Reader.ListSources(t.Context(), "u1")
	if err != nil || len(srcs) == 0 {
		t.Fatalf("读不到书源: %v", err)
	}
	book, err := svc.Reader.AddBook(t.Context(), "u1", reader.SearchOrigin{
		SourceID:   srcs[0].ID,
		Origin:     upstream,
		OriginName: "上游直连测试",
		OriginType: 1,
		BookURL:    upstream + "/book/1",
	}, "上游直连之书", "某作者", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}
	return svc, srv, book
}

func prepareMediaTicketBook(t *testing.T) (*service.Container, *mediaTicketServer, *model.ReaderBook) {
	t.Helper()
	svc := newReaderHandlerContainer(t)
	// 章节缓存必须落在磁盘上：换票要凭落盘载荷找回「这个地址属于哪一章」。
	// newReaderHandlerContainer 只给空 cfg，这里补一个临时缓存目录。
	svc.Cfg.Cache.CacheDir = t.TempDir()
	srv := newMediaTicketServer(t)
	srcJSON := fmt.Sprintf(`{
  "bookSourceName": "票源测试",
  "bookSourceType": 1,
  "bookSourceUrl": %q,
  "ruleContent": {"content": "$.data.url"}
}`, srv.URL)
	if _, err := svc.Reader.ImportSources(t.Context(), "u1", srcJSON); err != nil {
		t.Fatalf("导入书源失败: %v", err)
	}
	srcs, err := svc.Reader.ListSources(t.Context(), "u1")
	if err != nil || len(srcs) == 0 {
		t.Fatalf("读不到书源: %v", err)
	}
	book, err := svc.Reader.AddBook(t.Context(), "u1", reader.SearchOrigin{
		SourceID:   srcs[0].ID,
		Origin:     srv.URL,
		OriginName: "票源测试",
		OriginType: 1,
		BookURL:    srv.URL + "/book/1",
	}, "票源之书", "某作者", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}
	if err := svc.Reader.SaveChapters(t.Context(), book.ID, []reader.ChapterInput{
		{Index: 0, Title: "第一章", URL: srv.URL + "/content"},
	}); err != nil {
		t.Fatalf("保存目录失败: %v", err)
	}
	return svc, srv, book
}
