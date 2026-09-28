package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
	"github.com/truewhile/MeBox/internal/service/cloud"
)

// newEmbyStreamResolveTestContainer 造一个最小的 Emby 播放容器：一条云盘 strm 媒体
// 加一个必定被调用的换链解析器，用于观察服务端是否替客户端换链。
func newEmbyStreamResolveTestContainer(t *testing.T, resolver *countingStrmResolver) *service.Container {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Media{}, &model.Setting{}, &model.StrmAccount{}, &model.Library{}, &model.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&model.Media{
		Base:      model.Base{ID: "cloud-1"},
		Title:     "Cloud",
		Path:      "cloud://cloud115/Movie.mkv",
		Container: "strm",
		STRMURL:   "/api/strm/play/cloud115/video.mkv?acct=a1&pickcode=pc1",
	}).Error; err != nil {
		t.Fatalf("seed media: %v", err)
	}
	repos := repository.New(db)
	cfg := &config.Config{}
	log := zap.NewNop()
	return &service.Container{
		Cfg:    cfg,
		Log:    log,
		Repo:   repos,
		Emby:   service.NewEmbyService(cfg, log, repos),
		Stream: service.NewStreamService(cfg, log, repos, nil).SetStrmPlayTargetResolver(resolver.Resolve),
	}
}

type countingStrmResolver struct {
	calls int
}

func (r *countingStrmResolver) Resolve(context.Context, string, string) (*service.StrmPlayResult, error) {
	r.calls++
	link := "https://cdnfhnfile.115cdn.net/637b/Movie.mkv?t=1&k=sig"
	return &service.StrmPlayResult{
		RedirectURL: link,
		Link:        &cloud.DirectLink{URL: link, Headers: map[string]string{"User-Agent": "ua"}},
	}, nil
}

func serveEmbyVideoStream(t *testing.T, svc *service.Container, userAgent string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/emby/Videos/cloud-1/stream?api_key=jwt123", nil)
	c.Request.Header.Set("User-Agent", userAgent)
	c.Params = gin.Params{{Key: "id", Value: "cloud-1"}}
	embyVideoStreamHandler(svc, service.CloudPlaybackModeRedirectProxy)(c)
	return rec
}

// 115 直链与换取时的 User-Agent 严格绑定（换错一个字符 CDN 就 403），而原生
// 播放器（小幻影视 / RodelPlayer）的 API 层与播放器层 UA 并不一致，因此对非
// 浏览器客户端绝不能在服务端替它换链，必须让客户端自己去同源端点换链。
func TestEmbyVideoStreamDoesNotResolveDirectLinkForNativePlayer(t *testing.T) {
	resolver := &countingStrmResolver{}
	svc := newEmbyStreamResolveTestContainer(t, resolver)

	w := serveEmbyVideoStream(t, svc, "RodelPlayer/2.2610.2.0 (Windows NT 10.0.26200; x64)")

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d body=%s, want 302", w.Code, w.Body.String())
	}
	if resolver.calls != 0 {
		t.Fatalf("server-side resolve called %d times, want 0", resolver.calls)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "/api/strm/play/cloud115/video.mkv") {
		t.Fatalf("Location = %q, want the same-origin strm endpoint", loc)
	}
	if strings.Contains(loc, "115cdn.net") {
		t.Fatalf("Location = %q, must not point straight at the CDN for a native player", loc)
	}
}

// 浏览器的 XHR 与 <video> 拉流必然是同一个 UA，可以继续享受服务端换链少一跳。
func TestEmbyVideoStreamResolvesDirectLinkForBrowser(t *testing.T) {
	resolver := &countingStrmResolver{}
	svc := newEmbyStreamResolveTestContainer(t, resolver)

	w := serveEmbyVideoStream(t, svc, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36")

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d body=%s, want 302", w.Code, w.Body.String())
	}
	if resolver.calls != 1 {
		t.Fatalf("server-side resolve called %d times, want 1", resolver.calls)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "115cdn.net") {
		t.Fatalf("Location = %q, want the resolved direct link", loc)
	}
}

func TestIsBrowserLikeUserAgent(t *testing.T) {
	cases := []struct {
		ua   string
		want bool
	}{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36", true},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15", true},
		{"RodelPlayer/2.2610.2.0 (Windows NT 10.0.26200; x64)", false},
		{"Yamby/2.0.5.5(Android", false},
		{"Infuse-Direct/8.0", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isBrowserLikeUserAgent(tc.ua); got != tc.want {
			t.Fatalf("isBrowserLikeUserAgent(%q) = %v, want %v", tc.ua, got, tc.want)
		}
	}
}
