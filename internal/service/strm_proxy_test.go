package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// 同源转发必须原样保留 Range 语义：把 206 改写成 200 会让浏览器误判响应长度，
// 拖动进度条时反复重新拉流。
func TestProxyMediaDirectForwardsRangeAndStatus(t *testing.T) {
	var gotRange string
	var gotUA string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", "bytes 10-19/100")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer upstream.Close()

	svc := testStrmService(t)
	media := &model.Media{STRMURL: upstream.URL + "/video.mp4"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream/media-1?proxy=1", nil)
	req.Header.Set("Range", "bytes=10-19")
	req.Header.Set("User-Agent", "MeBoxTest/1.0")

	if err := svc.ProxyMediaDirect(context.Background(), rec, req, media); err != nil {
		t.Fatalf("ProxyMediaDirect: %v", err)
	}
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if gotRange != "bytes=10-19" {
		t.Fatalf("upstream Range = %q, want bytes=10-19", gotRange)
	}
	if gotUA != "MeBoxTest/1.0" {
		t.Fatalf("upstream User-Agent = %q, want MeBoxTest/1.0", gotUA)
	}
	if rec.Header().Get("Content-Range") != "bytes 10-19/100" {
		t.Fatalf("Content-Range = %q", rec.Header().Get("Content-Range"))
	}
	if rec.Body.String() != "0123456789" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

// 本地文件本身就是同源资源，不应该被代理（调用方按原静态文件逻辑处理）。
func TestProxyMediaDirectSkipsLocalFile(t *testing.T) {
	svc := testStrmService(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	writeFile(t, path, "not-a-video")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream/media-1?proxy=1", nil)
	err := svc.ProxyMediaDirect(context.Background(), rec, req, &model.Media{Path: path})
	if !errors.Is(err, ErrStrmProxyNotApplicable) {
		t.Fatalf("err = %v, want ErrStrmProxyNotApplicable", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("local file must not be proxied, body = %q", rec.Body.String())
	}
}
