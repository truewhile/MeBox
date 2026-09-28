package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service/cloud"
)

// directRedirectTestRepo 建一个带网盘账号表的库：normalizeCloudPlayTarget 需要
// StrmAccount 才能判断 strm 目标是不是本机账号生成的。
func directRedirectTestRepo(t *testing.T) *repository.Container {
	t.Helper()
	return repository.New(newServiceTestDB(t, &model.Media{}, &model.Setting{}, &model.StrmAccount{}))
}

func seedCloudSTRMMedia(t *testing.T, repos *repository.Container, id, strmURL string) {
	t.Helper()
	if err := repos.DB.Create(&model.Media{
		Base:      model.Base{ID: id},
		Title:     "Cloud",
		Path:      "cloud://cloud115/Movie.mkv",
		Container: "strm",
		STRMURL:   strmURL,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

// 服务端能换到最终直链时必须直接 302 过去：客户端原本要跟着
// /Videos/{id}/stream → /api/strm/play 两次 302，现在缩成一跳。
func TestServeFileRedirectsStraightToResolvedDirectURL(t *testing.T) {
	repos := directRedirectTestRepo(t)
	seedCloudSTRMMedia(t, repos, "cloud-direct", "/api/strm/play/cloud115/video.mkv?acct=a1&pickcode=pc1")
	direct := "https://cdnfhnfile.115cdn.net/637b/Movie.mkv?t=1&k=sig"

	var gotRaw, gotUA string
	svc := NewStreamService(&config.Config{}, zap.NewNop(), repos, nil).
		SetStrmPlayTargetResolver(func(_ context.Context, raw, userAgent string) (*StrmPlayResult, error) {
			gotRaw, gotUA = raw, userAgent
			// 生产环境 115 直链就是这样返回的：绑定 UA、Proxy=false。
			return &StrmPlayResult{
				RedirectURL: direct,
				Link:        &cloud.DirectLink{URL: direct, Headers: map[string]string{"User-Agent": userAgent}},
			}, nil
		})

	req := httptest.NewRequest(http.MethodGet, "http://nas.local:18080/api/stream/cloud-direct?token=jwt123", nil)
	req.Header.Set("User-Agent", "RodelPlayer/2.2607.7.0")
	w := httptest.NewRecorder()

	if err := svc.ServeFile(w, req, "cloud-direct"); err != nil {
		t.Fatalf("ServeFile: %v", err)
	}
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != direct {
		t.Fatalf("Location = %q, want the resolved direct link %q", loc, direct)
	}
	if strings.Contains(loc, "jwt123") || strings.Contains(loc, "media_id=") {
		t.Fatalf("internal auth query must not leak to the CDN link: %q", loc)
	}
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("direct redirect must stay uncacheable, got %q", got)
	}
	// 换链必须带播放器 UA（115 直链绑定换取时的 UA，且按 UA 分键缓存）。
	if gotUA != "RodelPlayer/2.2607.7.0" {
		t.Fatalf("resolver UA = %q, want the player UA", gotUA)
	}
	if !strings.Contains(gotRaw, "pickcode=pc1") {
		t.Fatalf("resolver raw = %q, want the strm target", gotRaw)
	}
}

// 只要拿不到「客户端自己能直接拉取的直链」，就必须回退到改动前的 strm 端点跳转，
// 保证行为不会比改动前更差。
func TestServeFileFallsBackWhenDirectResolveUnavailable(t *testing.T) {
	strmURL := "/api/strm/play/cloud115/video.mkv?acct=a1&pickcode=pc1"
	cases := []struct {
		name    string
		result  *StrmPlayResult
		wantErr error
	}{
		{name: "换链失败", wantErr: errors.New("115 换链失败")},
		{
			name: "需要服务端反向代理",
			result: &StrmPlayResult{
				Proxy: true,
				Link:  &cloud.DirectLink{URL: "https://cdn.example/x", Headers: map[string]string{"Authorization": "Basic x"}},
			},
		},
		{name: "没有直链（别的 MeBox 实例）", result: &StrmPlayResult{RedirectURL: "https://other.example/api/strm/play/cloud115/video.mkv?acct=o&pickcode=p"}},
		{name: "解析到本地文件", result: &StrmPlayResult{LocalPath: "/media/Movie.mkv"}},
		{name: "返回 nil", result: nil},
		{
			name: "链接要求额外请求头",
			result: &StrmPlayResult{
				RedirectURL: "https://cdn.example/x",
				Link: &cloud.DirectLink{
					URL:     "https://cdn.example/x",
					Headers: map[string]string{"User-Agent": "ua", "Authorization": "Bearer t"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repos := directRedirectTestRepo(t)
			seedCloudSTRMMedia(t, repos, "cloud-fallback", strmURL)
			svc := NewStreamService(&config.Config{}, zap.NewNop(), repos, nil).
				SetStrmPlayTargetResolver(func(context.Context, string, string) (*StrmPlayResult, error) {
					return tc.result, tc.wantErr
				})

			req := httptest.NewRequest(http.MethodGet, "http://nas.local:18080/api/stream/cloud-fallback?token=jwt123", nil)
			w := httptest.NewRecorder()

			if err := svc.ServeFile(w, req, "cloud-fallback"); err != nil {
				t.Fatalf("ServeFile: %v", err)
			}
			if w.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302", w.Code)
			}
			loc := w.Header().Get("Location")
			if !strings.Contains(loc, "/api/strm/play/cloud115/video.mkv") {
				t.Fatalf("Location = %q, want the strm endpoint fallback", loc)
			}
			if strings.Contains(loc, "cdn.example") || strings.Contains(loc, "other.example") {
				t.Fatalf("Location = %q, must not point at an unusable direct link", loc)
			}
		})
	}
}

// 没有注入解析器（测试/精简部署）时保持原有跳转，不受本次优化影响。
func TestServeFileKeepsSTRMEndpointWithoutResolver(t *testing.T) {
	repos := directRedirectTestRepo(t)
	seedCloudSTRMMedia(t, repos, "cloud-plain", "/api/strm/play/cloud115/video.mkv?acct=a1&pickcode=pc1")
	svc := NewStreamService(&config.Config{}, zap.NewNop(), repos, nil)

	req := httptest.NewRequest(http.MethodGet, "http://nas.local:18080/api/stream/cloud-plain?token=jwt123", nil)
	w := httptest.NewRecorder()
	if err := svc.ServeFile(w, req, "cloud-plain"); err != nil {
		t.Fatalf("ServeFile: %v", err)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "/api/strm/play/cloud115/video.mkv") {
		t.Fatalf("Location = %q, want the strm endpoint", loc)
	}
}

// playbackQueryWithUA 负责把播放器 UA 透传给换链方，同时不能改动原 URL。
func TestPlaybackQueryWithUAInjectsUserAgent(t *testing.T) {
	u, err := url.Parse("/api/strm/play/cloud115/video.mkv?acct=a1&pickcode=pc1")
	if err != nil {
		t.Fatal(err)
	}
	q := playbackQueryWithUA(u, "  RodelPlayer/2.2607.7.0  ")
	if got := q.Get("__ua"); got != "RodelPlayer/2.2607.7.0" {
		t.Fatalf("__ua = %q, want the trimmed player UA", got)
	}
	if q.Get("pickcode") != "pc1" || q.Get("acct") != "a1" {
		t.Fatalf("original query lost: %v", q)
	}
	if strings.Contains(u.RawQuery, "__ua") {
		t.Fatalf("source URL must not be mutated: %q", u.RawQuery)
	}
	if got := playbackQueryWithUA(u, "   ").Get("__ua"); got != "" {
		t.Fatalf("blank UA must not be injected, got %q", got)
	}
	if got := playbackQueryWithUA(nil, "ua").Get("__ua"); got != "ua" {
		t.Fatalf("nil URL must still accept the UA, got %q", got)
	}
}

// ResolvePlayTargetWithUA 是 ResolvePlayTarget 的 UA 版本：空 UA 时行为必须与
// 原方法完全一致（外部直链透传）。
func TestResolvePlayTargetWithUAKeepsPassthroughBehaviour(t *testing.T) {
	svc := &StrmService{}
	raw := "https://cdn.example.test/Movie.mkv?sign=1"
	got, err := svc.ResolvePlayTargetWithUA(context.Background(), raw, "RodelPlayer/1.0")
	if err != nil {
		t.Fatalf("ResolvePlayTargetWithUA: %v", err)
	}
	if got == nil || got.RedirectURL != raw {
		t.Fatalf("result = %+v, want passthrough of %q", got, raw)
	}
}
