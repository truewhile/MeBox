package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// TestNormalizeCloudPlayTarget 验证存库的云盘播放 URL（可能携带扫描时的旧 host）
// 被规范化为相对路径，使 302 始终基于当前请求地址构造。
func TestNormalizeCloudPlayTarget(t *testing.T) {
	ref := "/电影/某部影片 (2024)/movie.mkv"
	req := httptest.NewRequest(http.MethodGet, "http://nas.local:18080/api/stream/media-1", nil)
	ctx := context.Background()

	// 相对路径：本来就是本机形态，按 provider+ref 重建（保持相对）。
	relative := "/api/cloud/play/openlist?ref=" + url.QueryEscape(ref)
	got := normalizeCloudPlayTarget(ctx, nil, nil, req, relative)
	want := BuildRelativeCloudPlayURL("openlist", ref)
	if got != want {
		t.Fatalf("normalizeCloudPlayTarget(relative) = %q, want %q", got, want)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.IsAbs() || parsed.Host != "" {
		t.Fatalf("normalized target should be relative, got %q", got)
	}
	if parsed.Query().Get("ref") != ref {
		t.Fatalf("ref round-trip failed: %q", parsed.Query().Get("ref"))
	}

	// 绝对地址但 host 就是当前请求 host：宿主切换过（开发机扫描 → 部署）
	// 之后仍要能当作本机地址处理。
	sameHost := "http://nas.local:18080/api/strm/play/cloud115/video.mkv?acct=abc&pickcode=123"
	gotStrm := normalizeCloudPlayTarget(ctx, nil, nil, req, sameHost)
	wantStrm := "/api/strm/play/cloud115/video.mkv?acct=abc&pickcode=123"
	if gotStrm != wantStrm {
		t.Fatalf("normalizeCloudPlayTarget(same host) = %q, want %q", gotStrm, wantStrm)
	}

	// 非云盘播放 URL 保持原样（WebDAV/直链等）。
	passthrough := "https://dav.example.com/media/file.mkv"
	if got := normalizeCloudPlayTarget(ctx, nil, nil, req, passthrough); got != passthrough {
		t.Fatalf("non-cloud target should pass through, got %q", got)
	}
}

// TestNormalizeCloudPlayTargetKeepsForeignInstanceURL 验证别的 MeBox /
// MediaStationGo 实例生成的 .strm 内容不被本机账号解析，而是按第三方直链透传。
func TestNormalizeCloudPlayTargetKeepsForeignInstanceURL(t *testing.T) {
	svc := testStrmService(t)
	ctx := context.Background()
	// 本机自己的 115 账号（strm.base_url 由 testStrmService 设为 http://test.local:8096）。
	own := &model.StrmAccount{
		Base:     model.Base{ID: "acct-own-115"},
		Name:     "own",
		Provider: model.StrmProvider115,
		Enabled:  true,
	}
	if err := svc.repo.StrmAccount.Create(ctx, own); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://mebox.local/api/stream/media-1", nil)

	// host 不认识 + acct 不是本机账号 → 另一个实例的 .strm，原样透传。
	foreign := "http://other-mebox.example:18080/api/strm/play/cloud115/video.mkv?acct=acct-other&pickcode=xyz"
	if got := normalizeCloudPlayTarget(ctx, svc.repo, nil, req, foreign); got != foreign {
		t.Fatalf("foreign instance url should pass through, got %q", got)
	}

	// host 不认识但 acct 是本机账号 → 本机换了域名/IP 的老 .strm，仍要认领。
	staleOwn := "http://192.168.1.4:9011/api/strm/play/cloud115/video.mkv?acct=acct-own-115&pickcode=123"
	wantStale := "/api/strm/play/cloud115/video.mkv?acct=acct-own-115&pickcode=123"
	if got := normalizeCloudPlayTarget(ctx, svc.repo, nil, req, staleOwn); got != wantStale {
		t.Fatalf("own acct on stale host = %q, want %q", got, wantStale)
	}

	// acct 撞上本机账号 ID 但账号类型与路径 provider 不一致：不算本机。
	wrongProvider := "http://other-mebox.example/api/strm/play/openlist/video.mkv?acct=acct-own-115&pickcode=123"
	if got := normalizeCloudPlayTarget(ctx, svc.repo, nil, req, wrongProvider); got != wrongProvider {
		t.Fatalf("provider mismatch should pass through, got %q", got)
	}
}

// TestNormalizeCloudPlayTargetLegacyCloudURL 旧格式 /api/cloud/play（不带 acct）
// 无法凭账号 ID 判断归属：本机配了该类型账号就按本机处理（保住老固化地址的可
// 播放性），完全没配才透传。
func TestNormalizeCloudPlayTargetLegacyCloudURL(t *testing.T) {
	svc := testStrmService(t)
	ctx := context.Background()
	req := httptest.NewRequest(http.MethodGet, "http://mebox.local/api/stream/media-1", nil)
	ref := "/Movies/Movie.mkv"
	legacy := "http://old-host:9011/api/cloud/play/openlist?ref=" + url.QueryEscape(ref)

	// 本机没有 openlist 账号 → 不是本机地址。
	if got := normalizeCloudPlayTarget(ctx, svc.repo, nil, req, legacy); got != legacy {
		t.Fatalf("legacy url without local provider should pass through, got %q", got)
	}

	if err := svc.repo.StrmAccount.Create(ctx, &model.StrmAccount{
		Base:     model.Base{ID: "acct-openlist"},
		Name:     "openlist",
		Provider: model.StrmProviderOpenList,
		Enabled:  true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := normalizeCloudPlayTarget(ctx, svc.repo, nil, req, legacy); got != BuildRelativeCloudPlayURL("openlist", ref) {
		t.Fatalf("legacy url with local provider = %q, want relative rebuild", got)
	}
}
