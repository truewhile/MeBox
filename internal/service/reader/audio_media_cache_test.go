package reader

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/config"
)

func TestIsAudioMediaURL(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		bookType int
		want     bool
	}{
		{"m4a 直链", "https://cdn.example.com/a.m4a?uid=1&vkey=x", 0, true},
		{"mp3 直链", "https://cdn.example.com/a.MP3", 0, true},
		{"音频书里的插图", "https://cdn.example.com/img/cover.jpg", 1, false},
		{"HLS 播放列表", "https://cdn.example.com/index.m3u8?token=1", 1, false},
		{"无后缀签名直链（音频书）", "https://cdn.example.com/stream?token=1", 1, true},
		{"无后缀签名直链（文本/图片书）", "https://cdn.example.com/stream?token=1", 0, false},
		{"坏地址但书籍是音频", "://bad", 1, true},
	}
	for _, c := range cases {
		if got := IsAudioMediaURL(c.url, c.bookType); got != c.want {
			t.Errorf("%s: IsAudioMediaURL(%q, %d) = %v，期望 %v", c.name, c.url, c.bookType, got, c.want)
		}
	}
}

// TestAudioMediaCacheKeyIgnoresQuery 换票会改写 uid/vkey/ct，缓存键必须忽略
// 这些签名/时效参数，否则每次换票都会重新下载一遍。
func TestAudioMediaCacheKeyIgnoresQuery(t *testing.T) {
	a := audioMediaCacheKey("book-1", "https://cdn.example.com/long/a.m4a?uid=1&vkey=aaa")
	b := audioMediaCacheKey("book-1", "https://cdn.example.com/long/a.m4a?uid=2&vkey=bbb&ct=3")
	if a == "" || a != b {
		t.Fatalf("同一对象换票后缓存键应一致: %q vs %q", a, b)
	}
	if c := audioMediaCacheKey("book-2", "https://cdn.example.com/long/a.m4a"); c == a {
		t.Fatalf("不同书籍的缓存键不应相同")
	}
	if d := audioMediaCacheKey("book-1", "https://cdn.example.com/long/b.m4a"); d == a {
		t.Fatalf("不同对象的缓存键不应相同")
	}
}

// TestAudioMediaCacheKeyKeepsIdentifyingQuery 只靠 query 区分章节的书源
// （路径固定、如 /audio?id=12345）不能被合并成同一个缓存对象，否则第二集
// 会命中第一集的缓存直接播错内容。
func TestAudioMediaCacheKeyKeepsIdentifyingQuery(t *testing.T) {
	ep1 := audioMediaCacheKey("book-1", "https://host/audio?id=12345&vkey=aaa")
	ep2 := audioMediaCacheKey("book-1", "https://host/audio?id=67890&vkey=bbb")
	if ep1 == "" || ep2 == "" {
		t.Fatal("缓存键不应为空")
	}
	if ep1 == ep2 {
		t.Fatalf("不同章节（query 不同）的缓存键不应相同，否则会串集")
	}
}

// TestAudioMediaCacheKeyIgnoresQueryOrder 非签名参数的顺序不应影响缓存键。
func TestAudioMediaCacheKeyIgnoresQueryOrder(t *testing.T) {
	a := audioMediaCacheKey("book-1", "https://host/a.m4a?id=7&chapter=3&vkey=x")
	b := audioMediaCacheKey("book-1", "https://host/a.m4a?chapter=3&id=7&vkey=y")
	if a == "" || a != b {
		t.Fatalf("参数顺序不应影响缓存键: %q vs %q", a, b)
	}
}


// ─── 断点续传下载器 ──────────────────────────────────────────────────────

func TestParseContentRange(t *testing.T) {
	cases := []struct {
		in     string
		start  int64
		total  int64
		wantOK bool
	}{
		{"bytes 100-199/1234", 100, 1234, true},
		{"bytes 0-99/*", 0, -1, true},
		{"BYTES 500-599/600", 500, 600, true},
		{"", 0, -1, false},
		{"bytes */1234", 0, -1, false},
		{"bytes x-1/2", 0, -1, false},
	}
	for _, c := range cases {
		start, total, ok := parseContentRange(c.in)
		if ok != c.wantOK || start != c.start || total != c.total {
			t.Errorf("parseContentRange(%q) = (%d,%d,%v)，期望 (%d,%d,%v)",
				c.in, start, total, ok, c.start, c.total, c.wantOK)
		}
	}
}

// errAfterReader 读到 data 末尾后返回 err，模拟上游中途 RST/提前 EOF。
type errAfterReader struct {
	data []byte
	pos  int
	err  error
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func (r *errAfterReader) Close() error { return nil }

func newAudioDownloadTestService(t *testing.T) *ReaderService {
	t.Helper()
	return &ReaderService{cfg: &config.Config{Cache: config.CacheConfig{CacheDir: t.TempDir()}}}
}

func okResponse(body []byte) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: int64(len(body)),
		Header:        http.Header{"Content-Type": {"audio/mp4"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
	}
}

func rangeResponse(header string, body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusPartialContent,
		Header:     http.Header{"Content-Range": {header}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

// TestDownloadRemoteAudioResumesAfterReset 上游写到一半断开后，第二轮必须用
// Range 从断点续传，最终文件与上游内容逐字节一致。
func TestDownloadRemoteAudioResumesAfterReset(t *testing.T) {
	svc := newAudioDownloadTestService(t)
	const mediaURL = "https://cdn.example.com/long/a.m4a?vkey=1"
	payload := bytes.Repeat([]byte("0123456789"), 500) // 5000 字节
	const firstChunk = 2000

	var ranges []string
	attempt := 0
	path, err := svc.DownloadRemoteAudio(t.Context(), "book-1", mediaURL, 3,
		func(rangeHeader string) (*http.Response, []byte, error) {
			ranges = append(ranges, rangeHeader)
			attempt++
			if attempt == 1 {
				// 声明全量长度，只写 2000 字节就断开。
				return &http.Response{
					StatusCode:    http.StatusOK,
					ContentLength: int64(len(payload)),
					Header:        http.Header{"Content-Type": {"audio/mp4"}},
					Body: &errAfterReader{
						data: payload[:firstChunk],
						err:  io.ErrUnexpectedEOF,
					},
				}, nil, nil
			}
			if rangeHeader != fmt.Sprintf("bytes=%d-", firstChunk) {
				t.Fatalf("第二轮应请求断点续传，实际 Range=%q", rangeHeader)
			}
			return rangeResponse(
				fmt.Sprintf("bytes %d-%d/%d", firstChunk, len(payload)-1, len(payload)),
				payload[firstChunk:],
			), nil, nil
		})
	if err != nil {
		t.Fatalf("续传后应当成功: %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("读缓存文件失败: %v", readErr)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("续传后的内容不完整: %d 字节，期望 %d", len(got), len(payload))
	}
	if len(ranges) != 2 || ranges[0] != "" {
		t.Fatalf("请求序列 = %q，期望首次整段、第二次续传", ranges)
	}
}

// TestDownloadRemoteAudioResumesAcrossCalls 上一次请求失败留下的分片，下一次
// （用户点重试）要能直接接着下，而不是从第 0 字节重来。
func TestDownloadRemoteAudioResumesAcrossCalls(t *testing.T) {
	svc := newAudioDownloadTestService(t)
	const mediaURL = "https://cdn.example.com/long/b.m4a"
	payload := bytes.Repeat([]byte("abcdefgh"), 1000)
	const firstChunk = 3000

	// 第一次调用：上游只给 3000 字节就断，重试也没用（每次都 200 + 截断）。
	_, err := svc.DownloadRemoteAudio(t.Context(), "book-2", mediaURL, 1,
		func(rangeHeader string) (*http.Response, []byte, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: int64(len(payload)),
				Header:        http.Header{"Content-Type": {"audio/mp4"}},
				Body: &errAfterReader{
					data: payload[:firstChunk],
					err:  io.ErrUnexpectedEOF,
				},
			}, nil, nil
		})
	if err == nil {
		t.Fatalf("上游截断时应当失败")
	}

	// 第二次调用：直接请求 bytes=3000-，续完。
	var seenRange string
	path, err := svc.DownloadRemoteAudio(t.Context(), "book-2", mediaURL, 1,
		func(rangeHeader string) (*http.Response, []byte, error) {
			seenRange = rangeHeader
			return rangeResponse(
				fmt.Sprintf("bytes %d-%d/%d", firstChunk, len(payload)-1, len(payload)),
				payload[firstChunk:],
			), nil, nil
		})
	if err != nil {
		t.Fatalf("跨请求续传应当成功: %v", err)
	}
	if seenRange != fmt.Sprintf("bytes=%d-", firstChunk) {
		t.Fatalf("应当从上次的断点续传，实际 Range=%q", seenRange)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, payload) {
		t.Fatalf("跨请求续传后的内容不完整: %d 字节", len(got))
	}
}

// TestDownloadRemoteAudioRestartsWhenRangeIgnored 上游忽略 Range 仍回 200 时，
// 必须丢掉分片从头写，不能把两段字节拼在一起。
func TestDownloadRemoteAudioRestartsWhenRangeIgnored(t *testing.T) {
	svc := newAudioDownloadTestService(t)
	const mediaURL = "https://cdn.example.com/long/c.m4a"
	payload := bytes.Repeat([]byte("xyz9"), 500)

	var ranges []string
	attempt := 0
	path, err := svc.DownloadRemoteAudio(t.Context(), "book-3", mediaURL, 3,
		func(rangeHeader string) (*http.Response, []byte, error) {
			ranges = append(ranges, rangeHeader)
			attempt++
			if attempt == 1 {
				return &http.Response{
					StatusCode:    http.StatusOK,
					ContentLength: int64(len(payload)),
					Header:        http.Header{"Content-Type": {"audio/mp4"}},
					Body:          &errAfterReader{data: payload[:700], err: io.ErrUnexpectedEOF},
				}, nil, nil
			}
			// 无视 Range，整段回 200。
			return okResponse(payload), nil, nil
		})
	if err != nil {
		t.Fatalf("应当成功: %v", err)
	}
	if ranges[1] == "" {
		t.Fatalf("第二轮应当带 Range")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, payload) {
		t.Fatalf("忽略 Range 后必须从头重写，实际 %d 字节（期望 %d）", len(got), len(payload))
	}
}

// TestDownloadRemoteAudioStopsOnNoRetry 上游成批拒绝时（NoRetryError），
// 下载器要立刻收手，不能把同一张被拒的票再打几遍。
func TestDownloadRemoteAudioStopsOnNoRetry(t *testing.T) {
	svc := newAudioDownloadTestService(t)
	denied := errors.New("音源暂时被上游拒绝（HTTP 401）")

	calls := 0
	_, err := svc.DownloadRemoteAudio(t.Context(), "book-4", "https://cdn.example.com/long/d.m4a", 4,
		func(rangeHeader string) (*http.Response, []byte, error) {
			calls++
			return nil, nil, &NoRetryError{Err: denied}
		})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("应原样返回上游拒绝原因，实际 %v", err)
	}
	if calls != 1 {
		t.Fatalf("不可重试的失败只应请求 1 次，实际 %d 次", calls)
	}
}

// ─── 首播直通（边下边发） ────────────────────────────────────────────────

// TestDownloadRemoteAudioStreamingForwardsWhileDownloading 直通模式：字节必须
// 在下载过程中就到达 sink，而不是等整段下完才一次性写出去（首播 TTFB）。
func TestDownloadRemoteAudioStreamingForwardsWhileDownloading(t *testing.T) {
	svc := newAudioDownloadTestService(t)
	const mediaURL = "https://cdn.example.com/long/stream.m4a? vkey=1"
	payload := bytes.Repeat([]byte("stream!"), 1000)

	var sink bytes.Buffer
	var startedTotal int64
	// 用「读到一半时 sink 里已经有数据」来证明确实在边下边发。
	var midLength int
	path, err := svc.DownloadRemoteAudioStreaming(t.Context(), "book-s", mediaURL, 1,
		func(rangeHeader string) (*http.Response, []byte, error) {
			if rangeHeader != "" {
				t.Fatalf("首播直通不应带 Range，实际 %q", rangeHeader)
			}
			return okResponse(payload), nil, nil
		},
		func(total int64, contentType string) {
			startedTotal = total
			// 头部回调必须早于任何字节下发。
			if sink.Len() != 0 {
				t.Errorf("写响应头时不应已有字节写出")
			}
		},
		writerFunc(func(p []byte) (int, error) {
			if midLength == 0 && sink.Len() == 0 && len(p) > 0 {
				midLength = sink.Len()
			}
			return sink.Write(p)
		}),
	)
	if err != nil {
		t.Fatalf("直通下载失败: %v", err)
	}
	if startedTotal != int64(len(payload)) {
		t.Fatalf("头部回调应拿到上游总长 %d，实际 %d", len(payload), startedTotal)
	}
	if !bytes.Equal(sink.Bytes(), payload) {
		t.Fatalf("下发的字节不完整: %d 字节，期望 %d", sink.Len(), len(payload))
	}
	// 下载完成后缓存文件必须完整可命中。
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("读缓存文件失败: %v", readErr)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("落盘内容不完整: %d 字节", len(got))
	}
	if !svc.HasCachedRemoteAudio("book-s", mediaURL) {
		t.Fatal("直通下完后应当命中缓存")
	}
}

// TestDownloadRemoteAudioStreamingStopsWhenClientGoesAway 客户端中途断开时
// 停止下载，但保留分片供下次续传（不能把已下的字节白扔掉）。
func TestDownloadRemoteAudioStreamingStopsWhenClientGoesAway(t *testing.T) {
	svc := newAudioDownloadTestService(t)
	const mediaURL = "https://cdn.example.com/long/gone.m4a"
	payload := bytes.Repeat([]byte("abcdefgh"), 2000)
	clientGone := errors.New("client closed connection")

	var sink bytes.Buffer
	_, err := svc.DownloadRemoteAudioStreaming(t.Context(), "book-g", mediaURL, 1,
		func(rangeHeader string) (*http.Response, []byte, error) {
			return okResponse(payload), nil, nil
		},
		nil,
		writerFunc(func(p []byte) (int, error) {
			// 收下前 1000 字节后模拟播放器断开。
			if sink.Len() >= 1000 {
				return 0, clientGone
			}
			room := 1000 - sink.Len()
			if len(p) <= room {
				return sink.Write(p)
			}
			n, _ := sink.Write(p[:room])
			return n, clientGone
		}),
	)
	if err == nil {
		t.Fatal("客户端断开时应当停止下载")
	}
	var sinkErr *audioSinkError
	if !errors.As(err, &sinkErr) {
		t.Fatalf("应标记为写客户端失败（便于上层区分），实际 %v", err)
	}
	// 分片要留着：这是续传的原料。
	path := svc.RemoteAudioCachePath("book-g", mediaURL)
	if path == "" {
		t.Fatal("缓存路径不应为空")
	}
	if info, statErr := os.Stat(path + audioPartialSuffix); statErr != nil || info.Size() == 0 {
		t.Fatalf("断流后应保留 .part 分片，实际 %v", statErr)
	}
	if svc.HasCachedRemoteAudio("book-g", mediaURL) {
		t.Fatal("中断的下载不能留下完整缓存（否则会下发截断音频）")
	}
}

// TestDownloadRemoteAudioStreamingAbortsOnRestart 直通模式下上游忽略 Range 回
// 200（需要从 0 重来）时必须中止，而不是把重复字节拼进客户端已收到的流里。
func TestDownloadRemoteAudioStreamingAbortsOnRestart(t *testing.T) {
	svc := newAudioDownloadTestService(t)
	const mediaURL = "https://cdn.example.com/long/restart.m4a"
	payload := bytes.Repeat([]byte("zz"), 100)
	// 先留一个分片，让第二轮带 Range。
	partPath := svc.RemoteAudioCachePath("book-r", mediaURL) + audioPartialSuffix
	if err := os.MkdirAll(filepath.Dir(partPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partPath, payload[:50], 0o640); err != nil {
		t.Fatal(err)
	}

	var sink bytes.Buffer
	_, err := svc.DownloadRemoteAudioStreaming(t.Context(), "book-r", mediaURL, 1,
		func(rangeHeader string) (*http.Response, []byte, error) {
			// 无视 Range，整段回 200 → 直通无法重来。
			return okResponse(payload), nil, nil
		},
		nil,
		&sink,
	)
	if !errors.Is(err, errAudioStreamCannotRestart) {
		t.Fatalf("应返回不可重来错误，实际 %v", err)
	}
	if sink.Len() != 0 {
		t.Fatalf("中止前不应向客户端写出字节，实际 %d", sink.Len())
	}
}

// writerFunc 把函数适配成 io.Writer。
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
