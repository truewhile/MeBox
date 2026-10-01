package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

// newStreamImageProxy 构造一个允许访问指定测试上游的图片代理。
func newStreamImageProxy(t *testing.T, upstreamURL string) *ImageProxy {
	t.Helper()
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	proxy := NewImageProxy(&config.Config{Cache: config.CacheConfig{CacheDir: filepath.Join(t.TempDir(), "cache")}}, zap.NewNop())
	proxy.SetAllowedRemoteHostsProvider(func() []string { return []string{parsed.Host} })
	return proxy
}

// bigTestJPEG 在合法 JPEG 后面补一串数据，用来验证大图是流式落盘而不是
// 整张读进内存（剧照/原图常有数兆字节）。
func bigTestJPEG(t *testing.T, extraBytes int) []byte {
	t.Helper()
	out := make([]byte, 0, len(testJPEG)+extraBytes)
	out = append(out, testJPEG...)
	out = append(out, bytes.Repeat([]byte{0x5a}, extraBytes)...)
	return out
}

// TestImageProxyStreamsRemoteImageIntoCacheFile 远程原图必须直接流式写到
// 缓存文件，后续请求由缓存文件服务（上游只被请求一次）。
func TestImageProxyStreamsRemoteImageIntoCacheFile(t *testing.T) {
	payload := bigTestJPEG(t, 3<<20)

	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	proxy := newStreamImageProxy(t, upstream.URL)
	raw := upstream.URL + "/poster.jpg"

	rec := httptest.NewRecorder()
	if err := proxy.Serve(t.Context(), rec, httptest.NewRequest(http.MethodGet, "/api/img", nil), raw); err != nil {
		t.Fatalf("Serve failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("body length = %d, want %d", rec.Body.Len(), len(payload))
	}

	_, cachePath, failPath := proxy.remoteImageCachePathsForValidated(raw)
	stat, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("expected the image to be streamed to %s: %v", cachePath, err)
	}
	if stat.Size() != int64(len(payload)) {
		t.Fatalf("cached size = %d, want %d", stat.Size(), len(payload))
	}
	if _, err := os.Stat(failPath); err == nil {
		t.Fatalf("unexpected failure marker at %s", failPath)
	}
	if entries, err := os.ReadDir(filepath.Dir(cachePath)); err == nil {
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) == ".tmp" {
				t.Fatalf("temporary file left behind: %s", entry.Name())
			}
		}
	}

	// 第二次请求命中缓存文件，不再回源。
	second := httptest.NewRecorder()
	if err := proxy.Serve(t.Context(), second, httptest.NewRequest(http.MethodGet, "/api/img", nil), raw); err != nil {
		t.Fatalf("second Serve failed: %v", err)
	}
	if !bytes.Equal(second.Body.Bytes(), payload) {
		t.Fatal("cached response body mismatch")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (second request must be served from cache)", got)
	}
}

// TestImageProxyRejectsNonImageUpstreamWithoutCaching 流式写入必须在提交前
// 校验内容，否则错误页会被永久缓存成图片。
func TestImageProxyRejectsNonImageUpstreamWithoutCaching(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>not an image</body></html>"))
	}))
	defer upstream.Close()

	proxy := newStreamImageProxy(t, upstream.URL)
	raw := upstream.URL + "/poster.jpg"

	rec := httptest.NewRecorder()
	if err := proxy.Serve(t.Context(), rec, httptest.NewRequest(http.MethodGet, "/api/img", nil), raw); err != nil {
		t.Fatalf("Serve failed: %v", err)
	}
	if rec.Body.Len() != len(transparent1x1PNG) {
		t.Fatalf("body length = %d, want placeholder %d", rec.Body.Len(), len(transparent1x1PNG))
	}

	_, cachePath, failPath := proxy.remoteImageCachePathsForValidated(raw)
	if _, err := os.Stat(cachePath); err == nil {
		t.Fatal("non-image response must not be cached")
	}
	if _, err := os.Stat(failPath); err != nil {
		t.Fatalf("expected a failure marker next to the cache entry: %v", err)
	}
}

// TestImageProxyServesRemoteImageWhenCacheDirUnwritable 缓存目录不可写时
// （磁盘只读/满）必须退回内存缓冲，不能让所有图片变成占位图。
func TestImageProxyServesRemoteImageWhenCacheDirUnwritable(t *testing.T) {
	payload := testJPEG
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	// 把一个普通文件当作目录的父级，MkdirAll 必然失败。
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	proxy := newStreamImageProxy(t, upstream.URL)
	proxy.cacheDir = filepath.Join(blocker, "cache", "images")

	raw := upstream.URL + "/poster.jpg"
	rec := httptest.NewRecorder()
	if err := proxy.Serve(t.Context(), rec, httptest.NewRequest(http.MethodGet, "/api/img", nil), raw); err != nil {
		t.Fatalf("Serve failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("body length = %d, want %d", len(body), len(payload))
	}
}

// TestImageProxyFetchReturnsCachedBytes Fetch 仍需返回字节（刮削写元数据用），
// 内容来自刚写入的缓存文件。
func TestImageProxyFetchReturnsCachedBytes(t *testing.T) {
	payload := bigTestJPEG(t, 256<<10)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	proxy := newStreamImageProxy(t, upstream.URL)
	raw := upstream.URL + "/poster.jpg"

	data, ctype, err := proxy.Fetch(t.Context(), raw)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("fetched %d bytes, want %d", len(data), len(payload))
	}
	if ctype != "image/jpeg" {
		t.Fatalf("content type = %q, want image/jpeg", ctype)
	}
}
