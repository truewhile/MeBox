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
