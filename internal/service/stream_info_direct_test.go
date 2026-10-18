package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/cloud"
)

// /api/playback/{id}/info 预置换链：能换到直链时返回 true，并把客户端 UA
// 透传给 115（直链绑定换取时的 UA，播放器必须用同一个）。
func TestResolveDirectPlayURLForPlaybackInfo(t *testing.T) {
	const direct = "https://cdnfhnfile.115cdn.net/637b/Movie.mkv?t=1&k=sig"
	var gotUA string
	svc := NewStreamService(&config.Config{}, zap.NewNop(), nil, nil).
		SetStrmPlayTargetResolver(func(_ context.Context, _, userAgent string) (*StrmPlayResult, error) {
			gotUA = userAgent
			return &StrmPlayResult{
				RedirectURL: direct,
				Link:        &cloud.DirectLink{URL: direct, Headers: map[string]string{"User-Agent": userAgent}},
			}, nil
		})
	m := &model.Media{
		Base:      model.Base{ID: "cloud-info"},
		Path:      "cloud://cloud115/Movie.mkv",
		Container: "strm",
		STRMURL:   "https://mebox.example/api/strm/play/cloud115/video.mkv?acct=a1&pickcode=pc1",
	}
	req := httptest.NewRequest(http.MethodGet, "/api/playback/cloud-info/info", nil)
	req.Header.Set("User-Agent", "MeBox-Android/0.1.0")

	got, ok := svc.ResolveDirectPlayURL(req, m)
	if !ok || got != direct {
		t.Fatalf("ResolveDirectPlayURL() = (%q, %v), want (%q, true)", got, ok, direct)
	}
	if gotUA != "MeBox-Android/0.1.0" {
		t.Fatalf("resolver UA = %q, want the requesting client UA", gotUA)
	}
}

// 本地文件没有可直连的 CDN 地址，信息接口不能凭空给出 direct_url。
func TestResolveDirectPlayURLSkipsLocalMedia(t *testing.T) {
	svc := NewStreamService(&config.Config{}, zap.NewNop(), nil, nil).
		SetStrmPlayTargetResolver(func(context.Context, string, string) (*StrmPlayResult, error) {
			t.Fatal("本地文件不应该触发换链")
			return nil, nil
		})
	m := &model.Media{
		Base:      model.Base{ID: "local-1"},
		Path:      "/media/movie.mkv",
		Container: "mkv",
	}
	req := httptest.NewRequest(http.MethodGet, "/api/playback/local-1/info", nil)
	if got, ok := svc.ResolveDirectPlayURL(req, m); ok || got != "" {
		t.Fatalf("ResolveDirectPlayURL() = (%q, %v), want (\"\", false)", got, ok)
	}
}

// 需要服务端附加其它请求头（Referer 等）的直链不能短路给客户端，否则播放端
// 少了鉴权头会 403；这类媒体继续走 /api/stream 由服务端转发。
func TestResolveDirectPlayURLSkipsHeaderBoundLinks(t *testing.T) {
	const direct = "https://cdn.example.com/Movie.mkv"
	svc := NewStreamService(&config.Config{}, zap.NewNop(), nil, nil).
		SetStrmPlayTargetResolver(func(_ context.Context, _, userAgent string) (*StrmPlayResult, error) {
			return &StrmPlayResult{
				RedirectURL: direct,
				Link: &cloud.DirectLink{
					URL:     direct,
					Headers: map[string]string{"User-Agent": userAgent, "Referer": "https://pan.example"},
				},
			}, nil
		})
	m := &model.Media{
		Base:      model.Base{ID: "cloud-header"},
		Path:      "cloud://cloud115/Movie.mkv",
		Container: "strm",
		STRMURL:   "/api/strm/play/cloud115/video.mkv?acct=a1&pickcode=pc1",
	}
	req := httptest.NewRequest(http.MethodGet, "/api/playback/cloud-header/info", nil)
	if got, ok := svc.ResolveDirectPlayURL(req, m); ok || got != "" {
		t.Fatalf("ResolveDirectPlayURL() = (%q, %v), want (\"\", false)", got, ok)
	}
}
