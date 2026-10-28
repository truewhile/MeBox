package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newGzipTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := router.Group("/api")
	api.Use(GzipAPI())
	api.GET("/libraries", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"items": strings.Repeat("mebox", 200)})
	})
	api.GET("/stream/:id", func(c *gin.Context) {
		c.String(http.StatusOK, strings.Repeat("video-bytes", 200))
	})
	router.GET("/assets/app.js", GzipStatic(), func(c *gin.Context) {
		c.Data(http.StatusOK, "text/javascript", []byte(strings.Repeat("console.log(1);", 200)))
	})
	return router
}

func TestGzipAPICompressesJSONWhenAccepted(t *testing.T) {
	router := newGzipTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/libraries", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if raw := strings.Repeat("mebox", 200); w.Body.Len() >= len(raw) {
		t.Fatalf("body not compressed: len = %d, raw = %d", w.Body.Len(), len(raw))
	}
}

func TestGzipAPISkipsWhenNotAccepted(t *testing.T) {
	router := newGzipTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/libraries", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Header().Get("Content-Encoding"); got == "gzip" {
		t.Fatal("Content-Encoding should not be gzip without Accept-Encoding")
	}
}

func TestGzipAPIExcludesStreamPath(t *testing.T) {
	router := newGzipTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/stream/abc", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Header().Get("Content-Encoding"); got == "gzip" {
		t.Fatal("stream path must not be gzipped (Range semantics)")
	}
}

// TestGzipAPIExcludesReaderMedia 阅读媒体代理必须排除在 gzip 之外。
//
// 它会透传 CDN 的 206 Partial Content。gin-contrib/gzip 压缩时会删掉
// Content-Length 却保留 Content-Range，客户端看到「声明 N 字节、实收 M 字节」，
// 自己发 Accept-Encoding 的播放器（ExoPlayer）直接判
// ERROR_CODE_PARSING_CONTAINER_UNSUPPORTED，表现为「音频格式不支持」。
func TestGzipAPIExcludesReaderMedia(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := router.Group("/api")
	api.Use(GzipAPI())
	// 用 206 + Content-Range 模拟真实的音频分段响应。
	segment := func(c *gin.Context) {
		c.Header("Content-Range", "bytes 0-99/100")
		c.Data(http.StatusPartialContent, "audio/mp4", []byte(strings.Repeat("x", 4096)))
	}
	for _, path := range []string{
		"/reader/media",
		"/reader/local/audio",
		"/reader/audio/transcode",
	} {
		api.GET(path, segment)
	}

	for _, path := range []string{
		"/reader/media",
		"/reader/local/audio",
		"/reader/audio/transcode",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api"+path+"?b=book&u=x&s=y", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if got := w.Header().Get("Content-Encoding"); got == "gzip" {
			t.Errorf("%s 不该被 gzip（Range 语义会被破坏）", path)
		}
		if got := w.Header().Get("Content-Range"); got != "bytes 0-99/100" {
			t.Errorf("%s 的 Content-Range 被破坏: %q", path, got)
		}
		if w.Body.Len() != 4096 {
			t.Errorf("%s 响应体长度 = %d，期望 4096", path, w.Body.Len())
		}
	}
}

func TestGzipStaticCompressesAssets(t *testing.T) {
	router := newGzipTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip for static assets", got)
	}
}
