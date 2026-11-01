package handler

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
	"github.com/truewhile/MeBox/internal/service/reader"
)

// 音轨「区间直通」的行为契约。
//
// 背景（真实故障）：播放器（media3）取一条 m4a 会开两次请求——先整段打开嗅探
// 容器，再带 Range 从目标位置重开。旧实现只允许「无 Range + 无分片」的请求
// 边下边发，于是带 Range 的那次（真正的起播请求）必须把整章下完再按区间下发。
// 线上日志：每章固定 200（探针，被播放器掐断）+ 206（整章下完才吐第一个字节），
// 42 章里「媒体已装载 → 实际起播」平均 4.79 秒，最坏 35 秒。
//
// 现在的契约：
//   - 带 Range 的冷缓存请求 → 上游一到字节就往下发，不等整章；
//   - 分片已有的前缀从本地发，上游只续下剩余字节；
//   - 整章落满时把分片提升为正式缓存，之后走本地；
//   - 上游不认 Range 时也要给出正确的 206（跳过前缀），且不写带洞的分片；
//   - 拿不到写锁时降级成纯透传，绝不排队等前一个下载。

// rangeUpstream 支持 Range 的上游：/content 给出音轨地址，/media 回音频本体。
type rangeUpstream struct {
	*httptest.Server
	payload    []byte
	honorRange bool
	// firstChunk > 0 且 release 非 nil 时：先写这么多字节再等放行，
	// 用来证明「客户端不必等整章下完」。
	firstChunk int
	release    chan struct{}

	mu     sync.Mutex
	ranges []string
	hits   int
}

func newRangeUpstream(t *testing.T, payload []byte, honorRange bool) *rangeUpstream {
	t.Helper()
	u := &rangeUpstream{payload: payload, honorRange: honorRange}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/book/1"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><body><h1>区间之书</h1></body></html>`))
		case r.URL.Path == "/content":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"data":{"url":"%s/media"}}`, u.URL)))
		case r.URL.Path == "/media":
			u.serveMedia(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *rangeUpstream) serveMedia(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.hits++
	u.ranges = append(u.ranges, r.Header.Get("Range"))
	u.mu.Unlock()

	total := len(u.payload)
	start, end := 0, total-1
	partial := false
	if u.honorRange {
		if s, e, ok := parseReaderAudioRange(r.Header.Get("Range")); ok {
			start = int(s)
			if e >= 0 && int(e) < end {
				end = int(e)
			}
			partial = true
		}
	}
	w.Header().Set("Content-Type", "audio/mp4")
	w.Header().Set("Accept-Ranges", "bytes")
	if start >= total {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	body := u.payload[start : end+1]
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if u.release != nil && u.firstChunk > 0 && len(body) > u.firstChunk {
		_, _ = w.Write(body[:u.firstChunk])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-u.release:
		case <-r.Context().Done():
			return
		}
		body = body[u.firstChunk:]
	}
	_, _ = w.Write(body)
}

func (u *rangeUpstream) rangeHeaders() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.ranges...)
}

func (u *rangeUpstream) requestCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

// prepareRangeBook 建一本「书源直连上游」的音频书，并取回该音轨的签名代理路径。
func prepareRangeBook(t *testing.T, upstream string) (*service.Container, *model.ReaderBook, string) {
	t.Helper()
	svc := newReaderHandlerContainer(t)
	svc.Cfg.Cache.CacheDir = t.TempDir()
	srcJSON := fmt.Sprintf(`{
  "bookSourceName": "区间直通测试",
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
		OriginName: "区间直通测试",
		OriginType: 1,
		BookURL:    upstream + "/book/1",
	}, "区间之书", "某作者", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}
	if err := svc.Reader.SaveChapters(t.Context(), book.ID, []reader.ChapterInput{
		{Index: 0, Title: "第一章", URL: upstream + "/content"},
	}); err != nil {
		t.Fatalf("保存目录失败: %v", err)
	}
	out, err := svc.Reader.GetContentForBook(t.Context(), "u1", book.ID, 0, reader.ContentOptions{})
	if err != nil {
		t.Fatalf("取章节失败: %v", err)
	}
	if len(out.Tracks) != 1 {
		t.Fatalf("音轨数 = %d，期望 1", len(out.Tracks))
	}
	return svc, book, out.Tracks[0]
}

func audioPayload(n int) []byte {
	payload := make([]byte, n)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	return payload
}

// waitCached 等待分片被提升为正式缓存（提升发生在响应收尾，与客户端读完成之间有微小竞态）。
func waitCached(t *testing.T, svc *service.Container, bookID, mediaURL string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if svc.Reader.HasCachedRemoteAudio(bookID, mediaURL) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("整章下完后应提升为正式缓存")
}

// TestReaderAudioRangeStreamsWithoutWaitingForWholeChapter 冷缓存 + Range：
// 上游只吐了第一段，客户端就必须已经拿到字节。
func TestReaderAudioRangeStreamsWithoutWaitingForWholeChapter(t *testing.T) {
	payload := audioPayload(512 << 10)
	upstream := newRangeUpstream(t, payload, true)
	upstream.firstChunk = 64 << 10
	upstream.release = make(chan struct{})

	svc, book, track := prepareRangeBook(t, upstream.URL)
	mediaURL := upstream.URL + "/media"
	srv := httptest.NewServer(mediaRouterForTest(svc))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+track, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Range", "bytes=0-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("状态码 = %d，期望 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)) {
		t.Fatalf("Content-Range = %q", got)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(payload)) {
		t.Fatalf("Content-Length = %q，期望 %d", got, len(payload))
	}

	// 上游此时还被 release 卡着：能在超时内读到第一段就说明没有等整章。
	head := make([]byte, upstream.firstChunk)
	readDone := make(chan error, 1)
	go func() {
		_, readErr := io.ReadFull(resp.Body, head)
		readDone <- readErr
	}()
	select {
	case readErr := <-readDone:
		if readErr != nil {
			t.Fatalf("读首段失败: %v", readErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("上游还没给完就卡住了：客户端没能在整章下完前拿到第一个字节")
	}
	if !bytes.Equal(head, payload[:upstream.firstChunk]) {
		t.Fatalf("首段字节不一致")
	}

	close(upstream.release)
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读剩余字节失败: %v", err)
	}
	if got := append(head, rest...); !bytes.Equal(got, payload) {
		t.Fatalf("整体字节不一致: got %d bytes", len(got))
	}
	if got := upstream.rangeHeaders(); len(got) != 1 || got[0] != "bytes=0-" {
		t.Fatalf("上游收到的 Range = %v，期望且仅一次 bytes=0-", got)
	}
	waitCached(t, svc, book.ID, mediaURL)
}

// TestReaderAudioRangeResumesFromPartialFragment 分片里已有前缀时，前缀从本地发，
// 上游只续下剩余字节；整章落满后提升为正式缓存。
func TestReaderAudioRangeResumesFromPartialFragment(t *testing.T) {
	payload := audioPayload(256 << 10)
	upstream := newRangeUpstream(t, payload, true)
	svc, book, track := prepareRangeBook(t, upstream.URL)
	mediaURL := upstream.URL + "/media"

	// 模拟「播放器嗅探完就断开」留下的分片：前 96KB 已经在盘上。
	const partLen = 96 << 10
	cachePath := svc.Reader.RemoteAudioCachePath(book.ID, mediaURL)
	if cachePath == "" {
		t.Fatalf("缓存路径为空")
	}
	if mkErr := os.MkdirAll(cachePath[:strings.LastIndex(cachePath, string(os.PathSeparator))], 0o750); mkErr != nil {
		t.Fatalf("建缓存目录失败: %v", mkErr)
	}
	if writeErr := os.WriteFile(cachePath+".part", payload[:partLen], 0o640); writeErr != nil {
		t.Fatalf("写分片失败: %v", writeErr)
	}

	code, body := getReaderMediaWithRange(t, mediaRouterForTest(svc), track, "bytes=0-")
	if code != http.StatusPartialContent {
		t.Fatalf("状态码 = %d，期望 206 body=%.120s", code, body)
	}
	if !bytes.Equal([]byte(body), payload) {
		t.Fatalf("拼接后的字节不一致: got %d bytes want %d", len(body), len(payload))
	}
	headers := upstream.rangeHeaders()
	if len(headers) != 1 || headers[0] != fmt.Sprintf("bytes=%d-", partLen) {
		t.Fatalf("上游收到的 Range = %v，期望从分片长度续下 bytes=%d-", headers, partLen)
	}
	waitCached(t, svc, book.ID, mediaURL)

	// 提升之后同一首歌不再回源。
	before := upstream.requestCount()
	code, body = getReaderMedia(t, mediaRouterForTest(svc), track)
	if code != http.StatusOK || !bytes.Equal([]byte(body), payload) {
		t.Fatalf("命中缓存失败 status=%d len=%d", code, len(body))
	}
	if after := upstream.requestCount(); after != before {
		t.Fatalf("命中缓存后不应再回源，多了 %d 次", after-before)
	}
}

// TestReaderAudioRangeHandlesUpstreamThatIgnoresRange 上游不认 Range（回 200 整段）时，
// 仍要按客户端要的起点给出正确的 206，并且不写带洞的分片。
func TestReaderAudioRangeHandlesUpstreamThatIgnoresRange(t *testing.T) {
	payload := audioPayload(300 << 10)
	upstream := newRangeUpstream(t, payload, false)
	svc, book, track := prepareRangeBook(t, upstream.URL)
	mediaURL := upstream.URL + "/media"

	const start = 1000
	code, body := getReaderMediaWithRange(t, mediaRouterForTest(svc), track, fmt.Sprintf("bytes=%d-", start))
	if code != http.StatusPartialContent {
		t.Fatalf("状态码 = %d，期望 206 body=%.120s", code, body)
	}
	if !bytes.Equal([]byte(body), payload[start:]) {
		t.Fatalf("区间字节不一致: got %d want %d", len(body), len(payload)-start)
	}
	// 起点不在分片内 → 不能落盘（否则分片开头会是空洞）。
	if svc.Reader.HasPartialRemoteAudio(book.ID, mediaURL) {
		t.Fatalf("起点靠后的区间请求不应生成分片")
	}
	if svc.Reader.HasCachedRemoteAudio(book.ID, mediaURL) {
		t.Fatalf("只下了一段不应生成完整缓存")
	}
}

// TestReaderAudioRangeFallsBackToPassthroughWhenWriterBusy 拿不到写锁时不排队：
// 仍然立刻把区间发出去，只是这一轮不落盘。
func TestReaderAudioRangeFallsBackToPassthroughWhenWriterBusy(t *testing.T) {
	payload := audioPayload(128 << 10)
	upstream := newRangeUpstream(t, payload, true)
	svc, book, track := prepareRangeBook(t, upstream.URL)
	mediaURL := upstream.URL + "/media"

	cacheKey := svc.Reader.RemoteAudioCachePath(book.ID, mediaURL)
	if cacheKey == "" {
		t.Fatalf("缓存路径为空")
	}
	unlock := readerAudioStreamLocks.Lock(cacheKey)
	defer unlock()

	done := make(chan struct{})
	var code int
	var body string
	go func() {
		code, body = getReaderMediaWithRange(t, mediaRouterForTest(svc), track, "bytes=0-")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("写锁被占用时不该让请求排队等待")
	}
	if code != http.StatusPartialContent {
		t.Fatalf("状态码 = %d，期望 206 body=%.120s", code, body)
	}
	if !bytes.Equal([]byte(body), payload) {
		t.Fatalf("区间字节不一致: got %d want %d", len(body), len(payload))
	}
	if svc.Reader.HasCachedRemoteAudio(book.ID, mediaURL) {
		t.Fatalf("降级透传不应写缓存")
	}
}

func TestParseReaderAudioRangeCases(t *testing.T) {
	cases := []struct {
		in        string
		start     int64
		end       int64
		want      bool
	}{
		{"bytes=0-", 0, -1, true},
		{"bytes=100-199", 100, 199, true},
		{"Bytes=1024-", 1024, -1, true},
		{" bytes=5-5 ", 5, 5, true},
		{"bytes=-500", 0, 0, false},
		{"bytes=0-99,200-299", 0, 0, false},
		{"items=0-", 0, 0, false},
		{"bytes=abc-", 0, 0, false},
		{"bytes=200-100", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		start, end, ok := parseReaderAudioRange(c.in)
		if ok != c.want || start != c.start || end != c.end {
			t.Errorf("parseReaderAudioRange(%q) = (%d,%d,%v)，期望 (%d,%d,%v)",
				c.in, start, end, ok, c.start, c.end, c.want)
		}
	}
}

// getReaderMediaWithRange 发一次带 Range 头的媒体代理请求。
func getReaderMediaWithRange(t *testing.T, router *gin.Engine, signedPath, rangeHeader string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, signedPath, nil)
	req.Header.Set("Range", rangeHeader)
	router.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}
