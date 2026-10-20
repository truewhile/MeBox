package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/cloud115"
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

// 115 直链只有 30 分钟寿命，缓存命中也可能拿到已经失效的地址。同源转发遇到
// CDN 403 时必须丢掉缓存重新换链再试一次，而不是把 403 直接透给客户端。
func TestProxyMediaDirectRetriesWithFreshLinkWhenCDNRejects(t *testing.T) {
	svc := testStrmService(t)
	acct := &model.StrmAccount{Name: "fake115", Provider: model.StrmProvider115, Config: "{}", Enabled: true}
	if err := svc.repo.StrmAccount.Create(context.Background(), acct); err != nil {
		t.Fatal(err)
	}

	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fresh.mkv" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "video/x-matroska")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("fresh-bytes"))
	}))
	defer cdn.Close()

	// 第一次换链给出「已过期」的旧地址，重新换链才给新地址。
	var mu sync.Mutex
	var calls int
	expiry := time.Now().Add(30 * time.Minute).Unix()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		attempt := calls
		mu.Unlock()
		link := cdn.URL + "/old.mkv"
		if attempt > 1 {
			link = cdn.URL + "/fresh.mkv"
		}
		_, _ = fmt.Fprintf(w, `{"state":true,"data":{"1":{"pick_code":"pc-retry","url":{"url":%q}}}}`,
			fmt.Sprintf("%s?t=%d&u=1&k=abc", link, expiry))
	}))
	defer api.Close()
	oldPro := cloud115.ProAPIBase
	cloud115.ProAPIBase = api.URL
	defer func() { cloud115.ProAPIBase = oldPro }()
	defer cloud115.ClearDownloadURLCache("pc-retry")
	oldMinAge := directLinkRefreshMinAge
	directLinkRefreshMinAge = 0
	defer func() { directLinkRefreshMinAge = oldMinAge }()

	media := &model.Media{
		STRMURL: "/api/strm/play/cloud115/video.mkv?acct=" + acct.ID + "&pickcode=pc-retry",
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream/media-1?proxy=1", nil)
	req.Header.Set("User-Agent", "MeBoxTest/1.0")

	if err := svc.ProxyMediaDirect(context.Background(), rec, req, media); err != nil {
		t.Fatalf("ProxyMediaDirect: %v", err)
	}
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 (body=%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "fresh-bytes" {
		t.Fatalf("body = %q, want the fresh link payload", rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("downurl calls = %d, want 2 (one retry after the CDN rejected the stale link)", calls)
	}
}

// 换链后依然是同一条失效直链时，保持原有行为：原样透传 CDN 的状态码。
func TestProxyMediaDirectPassesRejectedStatusWhenRefreshCannotHelp(t *testing.T) {
	svc := testStrmService(t)
	acct := &model.StrmAccount{Name: "fake115", Provider: model.StrmProvider115, Config: "{}", Enabled: true}
	if err := svc.repo.StrmAccount.Create(context.Background(), acct); err != nil {
		t.Fatal(err)
	}

	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer cdn.Close()

	expiry := time.Now().Add(30 * time.Minute).Unix()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"state":true,"data":{"1":{"pick_code":"pc-dead","url":{"url":%q}}}}`,
			fmt.Sprintf("%s/dead.mkv?t=%d&u=1&k=abc", cdn.URL, expiry))
	}))
	defer api.Close()
	oldPro := cloud115.ProAPIBase
	cloud115.ProAPIBase = api.URL
	defer func() { cloud115.ProAPIBase = oldPro }()
	defer cloud115.ClearDownloadURLCache("pc-dead")
	oldMinAge := directLinkRefreshMinAge
	directLinkRefreshMinAge = 0
	defer func() { directLinkRefreshMinAge = oldMinAge }()

	media := &model.Media{
		STRMURL: "/api/strm/play/cloud115/video.mkv?acct=" + acct.ID + "&pickcode=pc-dead",
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream/media-1?proxy=1", nil)

	if err := svc.ProxyMediaDirect(context.Background(), rec, req, media); err != nil {
		t.Fatalf("ProxyMediaDirect: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want the upstream 403 passed through", rec.Code)
	}
}

// 强制换链只认 /api/strm/play 形式播放目标里的 pickcode。
func TestPickCodeFromPlayTarget(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"/api/strm/play/cloud115/video.mkv?acct=a1&pickcode=pc1", "pc1"},
		{"https://nas.local/api/strm/play/cloud115/video.mkv?pickcode=pc2&acct=a1", "pc2"},
		{"/api/strm/play/cloud115/video.mkv?acct=a1", ""},
		{"https://cdn.example.test/Movie.mkv?sign=1", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := PickCodeFromPlayTarget(tc.raw); got != tc.want {
			t.Fatalf("PickCodeFromPlayTarget(%q) = %q, want %q", tc.raw, got, tc.want)
		}
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
