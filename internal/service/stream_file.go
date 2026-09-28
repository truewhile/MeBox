package service

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// ServeFile streams the file backing the given media ID using
// http.ServeContent so HEAD / Range / If-Modified-Since are handled for free.
//
// When the media row has a STRMURL set we redirect (302) to that URL
// instead of opening a local file. This lets WebDAV / Alist / S3 / HTTP
// direct links flow through the rest of the player UI unchanged.
func (s *StreamService) ServeFile(w http.ResponseWriter, r *http.Request, mediaID string) error {
	return s.ServeFileWithCloudMode(w, r, mediaID, "")
}

func (s *StreamService) ServeFileWithCloudMode(w http.ResponseWriter, r *http.Request, mediaID, cloudMode string) error {
	m, err := s.repo.Media.FindByID(r.Context(), mediaID)
	if err != nil {
		return err
	}
	if m == nil {
		return ErrMediaNotFound
	}
	if strmURL := strings.TrimSpace(m.STRMURL); strmURL != "" && playableSTRMTarget(r.Context(), s.repo, strmURL, m) {
		if !cloudPlaybackModeEnabled(r.Context(), s.repo, cloudMode) {
			return ErrCloudPlaybackDisabled
		}
		// 能在服务端换到最终直链就直接 302 过去：客户端少跟随一次 302，等于
		// 省掉一次「DNS+TCP+TLS+请求」的往返。解析结果同时写进 strm 层直链
		// 缓存，后续 /api/strm/play 请求直接命中。
		if direct, ok := s.resolveDirectPlayTargetURL(r, strmURL); ok {
			setCloudRedirectNoStore(w)
			http.Redirect(w, r, direct, http.StatusFound)
			return nil
		}
		// 云盘播放 URL 先规范化为相对路径，免疫扫描时固化的旧 host；
		// 指向别的 MeBox 实例的地址保持原样，按第三方直链透传。
		target := normalizeCloudPlayTarget(r.Context(), s.repo, s.cfg, r, strmURL)
		target = withAuthTokenForInternalRedirect(target, r, PublicServerURL(r.Context(), s.repo, s.cfg))
		setCloudRedirectNoStore(w)
		http.Redirect(w, r, absoluteInternalRedirect(target, r), http.StatusFound)
		return nil
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(m.Path)), "cloud://") {
		// 云盘媒体没有本地文件可回退；走到这里说明 STRM 播放被关闭或
		// STRMURL 缺失。返回明确错误而不是笼统的「文件不存在」，
		// 处理器据此回 502 + 原因，方便用户在播放器/日志里定位。
		return ErrCloudPlaybackUnavailable
	}
	f, err := os.Open(m.Path)
	if err != nil {
		return ErrMediaNotFound
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
	return nil
}

func setCloudRedirectNoStore(w http.ResponseWriter) {
	if w == nil {
		return
	}
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

// directPlayResolveTimeout 是服务端换链的等待上限。115 开放平台在跨太平洋线路
// 上单次换链实测 0.4–1.1s，这里给足余量；一旦超时就回退到原来的 strm 端点跳转，
// 由 /api/strm/play 再去换链，最坏情况只是回到改动前的行为。
const directPlayResolveTimeout = 10 * time.Second

// resolveDirectPlayTargetURL 尝试在服务端把 strm 目标解析成客户端可直接拉取的
// 最终直链，供调用方直接 302。
//
// 只在「明确的直链」上短路：需要服务端反向代理（云盘 WebDAV 等必须附加请求头）、
// 解析到本地文件、以及解析失败都会返回 false，由调用方按改动前的方式回退到
// strm 端点跳转，行为不会变差。
func (s *StreamService) resolveDirectPlayTargetURL(r *http.Request, raw string) (string, bool) {
	if s == nil || s.strmResolve == nil || r == nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(r.Context(), directPlayResolveTimeout)
	defer cancel()
	// 必须带播放器 UA：115 直链绑定换取时的 UA，且按 UA 分键缓存，换错会拿到
	// 与播放器不匹配（或未命中缓存）的地址。
	result, err := s.strmResolve(ctx, raw, r.Header.Get("User-Agent"))
	if err != nil || result == nil || result.Proxy || result.Link == nil {
		return "", false
	}
	// 客户端只能自带 User-Agent 这类基础请求头。链接一旦要求其它头（Referer /
	// Authorization），就必须继续由服务端反向代理，不能在这里短路。
	for name := range result.Link.Headers {
		if !strings.EqualFold(strings.TrimSpace(name), "User-Agent") {
			return "", false
		}
	}
	direct := strings.TrimSpace(result.RedirectURL)
	if direct == "" {
		return "", false
	}
	return direct, true
}

func isCloudPlaybackTarget(raw string) bool {
	_, _, ok := parseCloudMediaPlaybackURL(raw)
	return ok
}

func playableSTRMTarget(ctx context.Context, repo *repository.Container, raw string, m *model.Media) bool {
	if isCloudPlaybackTarget(raw) || isHTTPPlaybackTarget(raw) {
		return true
	}
	if m != nil && strings.EqualFold(strings.TrimSpace(m.Container), "strm") {
		return true
	}
	return STRMPlaybackEnabled(ctx, repo)
}

// IsStrmMediaRow 判断媒体行是否为 .strm（远程直链）媒体：STRMURL 非空、
// container=strm 或路径以 .strm 结尾。网页播放默认直连，失败后可转码。
func IsStrmMediaRow(m *model.Media) bool {
	if m == nil {
		return false
	}
	if strings.TrimSpace(m.STRMURL) != "" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(m.Container), "strm") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(m.Path)), ".strm")
}

func isStrmMediaRow(m *model.Media) bool {
	return IsStrmMediaRow(m)
}

func isHTTPPlaybackTarget(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || !u.IsAbs() {
		return false
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	return scheme == "http" || scheme == "https"
}
