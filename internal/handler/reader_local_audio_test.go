package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestReaderLocalAudioStream 本地有声书音频流端点：
// 签名鉴权 + Range 透传（播放器拖进度靠它）。
func TestReaderLocalAudioStream(t *testing.T) {
	container := newReaderHandlerContainer(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/reader/local/audio", readerLocalAudioHandler(container))
	router.HEAD("/api/reader/local/audio", readerLocalAudioHandler(container))

	payload := []byte("0123456789abcdefghij")
	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	signed := container.Reader.LocalAudioURL("book-1", path)

	// 全量拉取
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signed, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("全量拉取 status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != string(payload) {
		t.Fatalf("全量拉取 body=%q", w.Body.String())
	}

	// Range 请求要能拖进度
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, signed, nil)
	req.Header.Set("Range", "bytes=2-5")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusPartialContent {
		t.Fatalf("Range 请求 status=%d", w.Code)
	}
	if w.Body.String() != "2345" {
		t.Fatalf("Range 请求 body=%q", w.Body.String())
	}

	// 篡改签名应被拒绝
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signed+"x", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("坏签名 status=%d，期望 403", w.Code)
	}

	// 文件不存在 → 404（而不是 500）
	missing := container.Reader.LocalAudioURL("book-1", filepath.Join(t.TempDir(), "nope.mp3"))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, missing, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("缺失文件 status=%d，期望 404", w.Code)
	}
}

// TestReaderAudioTranscodeRoute 转码端点：签名鉴权，命中缓存后按 Range 下发。
// 用预置的缓存文件代替真实转码，避免测试依赖 ffmpeg。
func TestReaderAudioTranscodeRoute(t *testing.T) {
	container := newReaderHandlerContainer(t)
	container.Cfg.App.DataDir = t.TempDir() // 转码缓存落在临时目录，别写进仓库
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/reader/audio/transcode", readerAudioTranscodeHandler(container))
	router.HEAD("/api/reader/audio/transcode", readerAudioTranscodeHandler(container))

	const bookID = "book-1"
	source := `D:\media\斗破苍穹\001.wma`
	payload := []byte("ID3FAKEMP3PAYLOAD0123456789")

	cachePath, err := container.Reader.AudioTranscodeCachePath(bookID, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, payload, 0o640); err != nil {
		t.Fatal(err)
	}

	signed := container.Reader.AudioTranscodeURL(bookID, source)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signed, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("全量拉取 status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != string(payload) {
		t.Fatalf("全量拉取 body=%q", w.Body.String())
	}

	// 拖进度靠 Range
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, signed, nil)
	req.Header.Set("Range", "bytes=3-7")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusPartialContent {
		t.Fatalf("Range 请求 status=%d", w.Code)
	}
	if w.Body.String() != "FAKEM" {
		t.Fatalf("Range 请求 body=%q", w.Body.String())
	}

	// 篡改签名应被拒绝
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signed+"x", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("坏签名 status=%d，期望 403", w.Code)
	}

	// 换一本书（签名不匹配）同样拒绝
	forged := strings.Replace(signed, "b=book-1", "b=book-2", 1)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, forged, nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("换书后 status=%d，期望 403", w.Code)
	}
}

// TestReaderAudioTranscodeRouteWithoutFFmpeg 没装 ffmpeg 且无缓存时返回明确错误，
// 而不是空响应（前端据此提示用户去装 ffmpeg）。
func TestReaderAudioTranscodeRouteWithoutFFmpeg(t *testing.T) {
	container := newReaderHandlerContainer(t)
	container.Cfg.App.DataDir = t.TempDir()
	container.Cfg.App.FFmpegPath = filepath.Join(t.TempDir(), "definitely-missing-ffmpeg")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/reader/audio/transcode", readerAudioTranscodeHandler(container))

	signed := container.Reader.AudioTranscodeURL("book-1", `D:\media\001.wma`)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signed, nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status=%d，期望 502", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ffmpeg") {
		t.Fatalf("错误正文应提到 ffmpeg: %q", w.Body.String())
	}
}

// TestReaderLocalAudioRejectsOtherBook 换一本书的签名不通用：防止拿到别人的音频地址。
func TestReaderLocalAudioRejectsOtherBook(t *testing.T) {
	container := newReaderHandlerContainer(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/reader/local/audio", readerLocalAudioHandler(container))

	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	signed := container.Reader.LocalAudioURL("book-1", path)
	forged := strings.Replace(signed, "b=book-1", "b=book-2", 1)
	if forged == signed {
		t.Fatal("测试用例未改写 book 参数")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, forged, nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("换书后签名应失效，status=%d", w.Code)
	}
}
