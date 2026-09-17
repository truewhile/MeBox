package service

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/repository"
)

// normalizeCloudPlayTarget 把存库的云盘播放 URL 规范化为相对路径。
//
// STRMURL 是扫描时根据当时的 server_url/请求地址生成并固化进数据库的。
// 在 Windows 开发机上扫描、再部署到 Docker（或更换了内网 IP/域名）后，
// 这些绝对 URL 会指向已失效的旧地址，第三方播放器跟随 302 就会拿到
// 连接失败/404。所以只要确认这个地址「是本机自己生成的」，就从 URL 中解析出
// provider+ref，重建为相对 /api/cloud/play 或 /api/strm/play 路径，由
// absoluteInternalRedirect 基于「当前请求」补全 host，从而对历史脏数据免疫。
//
// 反过来，指向**别的 MeBox / MediaStationGo 实例**的地址不能按本机账号解析：
// 别人 .strm 里的 acct 是他那台机器的账号 ID，拿到本机来查只会得到
// 「网盘账号不存在」。这种地址按普通第三方直链原样透传，让客户端跟着 302 去
// 对方实例取流（/api/strm/play 是公开端点，不需要本机凭据），或直接去对方的
// CDN 直链。归属判断见 isInternalPlaybackTarget。
func normalizeCloudPlayTarget(ctx context.Context, repo *repository.Container, cfg *config.Config, r *http.Request, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	if !isInternalPlaybackTarget(ctx, repo, cfg, r, raw) {
		return raw
	}
	if typ, ref, ok := parseCloudMediaPlaybackURL(raw); ok {
		return BuildRelativeCloudPlayURL(typ, ref)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	path := strings.ToLower(u.Path)
	if strings.HasPrefix(path, "/api/strm/play/") || strings.HasPrefix(path, "/api/cloud/play/") || strings.HasPrefix(path, "/api/stream/") {
		u.Scheme = ""
		u.Host = ""
		return u.String()
	}
	return raw
}

// isInternalPlaybackTarget 判断播放地址能否按「本机自己的云盘播放地址」处理
// （相对化 + 用本机账号解析）。判断顺序：
//
//  1. 相对路径一定是本机存库的常规形态；
//  2. 绝对地址的 host 与本机配置的 strm.base_url、各同步目录覆盖的 base_url、
//     当前请求 host 之一相同 → 就是本机（老 .strm 里固化的旧 host 属于这一类）；
//  3. host 对不上时，再看地址里带的网盘账号 ID 是不是本机账号：MeBox /
//     MediaStationGo 会把本机账号 ID 写进 acct，而账号 ID 由各实例自行生成，
//     跨实例几乎不可能撞号。这条用于「换了域名/IP 之后」认领自己的老 .strm。
//
// 三条都不满足（典型是另一台 MeBox 生成的 .strm）→ 视为第三方直链。
func isInternalPlaybackTarget(ctx context.Context, repo *repository.Container, cfg *config.Config, r *http.Request, raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return false
	}
	if u.Host == "" {
		return true
	}
	if !isPlaybackAPIPath(u.Path) {
		return false
	}
	if matchesLocalPlaybackHost(ctx, repo, cfg, r, u) {
		return true
	}
	return localAccountOwnsPlaybackTarget(ctx, repo, u)
}

// isPlaybackAPIPath 判断路径是不是本服务自己的播放端点。
func isPlaybackAPIPath(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	for _, prefix := range []string{"/api/strm/play/", "/api/cloud/play/", "/api/stream/"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// matchesLocalPlaybackHost 判断绝对播放地址的 host 是否就是本机。
func matchesLocalPlaybackHost(ctx context.Context, repo *repository.Container, cfg *config.Config, r *http.Request, u *url.URL) bool {
	if u == nil {
		return false
	}
	target := canonicalHostPort(u)
	if target == "" {
		return false
	}
	for _, base := range localPlaybackBaseURLs(ctx, repo, cfg, r) {
		parsed, err := url.Parse(base)
		if err != nil || parsed == nil {
			continue
		}
		if canonicalHostPort(parsed) == target {
			return true
		}
	}
	return false
}

// localPlaybackBaseURLs 汇总本机的播放基地址：管理员配置的公网地址 /
// strm.base_url、每条 STRM 同步目录单独覆盖的 base_url，以及当前请求的 host
// （含反向代理头）。请求 host 也要算进来：没有配 base_url 时 .strm 里固化的
// 就是请求地址。
func localPlaybackBaseURLs(ctx context.Context, repo *repository.Container, cfg *config.Config, r *http.Request) []string {
	bases := make([]string, 0, 4)
	if base := PublicServerURL(ctx, repo, cfg); base != "" {
		bases = append(bases, base)
	}
	if repo != nil && repo.StrmSyncPath != nil {
		if paths, err := repo.StrmSyncPath.List(ctx); err == nil {
			for i := range paths {
				if override := strings.TrimSpace(paths[i].StrmBaseURL); override != "" {
					bases = append(bases, override)
				}
			}
		}
	}
	if r != nil {
		if host := strings.TrimSpace(r.Host); host != "" {
			bases = append(bases, "//"+host)
		}
		for _, header := range []string{"X-Forwarded-Host", "X-Original-Host"} {
			if host := strings.TrimSpace(r.Header.Get(header)); host != "" {
				bases = append(bases, "//"+host)
			}
		}
	}
	return bases
}

// canonicalHostPort 归一化 host[:port]：小写、忽略默认端口（http 80 / https 443）。
func canonicalHostPort(u *url.URL) string {
	if u == nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return ""
	}
	port := strings.TrimSpace(u.Port())
	if port == "" {
		return host
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if (scheme == "http" || scheme == "") && port == "80" {
		return host
	}
	if scheme == "https" && port == "443" {
		return host
	}
	return host + ":" + port
}

// localAccountOwnsPlaybackTarget 用「地址里带的网盘账号是不是本机的」判断播放
// 地址归属。账号 ID 跨实例不会撞号，因此比 host 更可靠：
//
//   - /api/strm/play/{provider}/video{ext}?acct=…：acct 能在本机查到、且账号类型
//     与路径里的 provider 一致，才认作本机地址；
//   - /api/cloud/play/{provider}?ref=…（旧格式，不带 acct）：只能退化为「本机是否
//     配置了该类型账号」，配置了才按本机处理，保持老版本固化地址的可播放性。
func localAccountOwnsPlaybackTarget(ctx context.Context, repo *repository.Container, u *url.URL) bool {
	if repo == nil || repo.StrmAccount == nil || u == nil {
		return false
	}
	segments := strings.Split(strings.Trim(strings.TrimSpace(u.Path), "/"), "/")
	if len(segments) < 4 || !strings.EqualFold(segments[0], "api") || !strings.EqualFold(segments[2], "play") {
		return false
	}
	provider := strings.TrimSpace(segments[3])
	if provider == "" {
		return false
	}
	switch strings.ToLower(segments[1]) {
	case "strm":
		accountID := strings.TrimSpace(u.Query().Get("acct"))
		if accountID == "" {
			return false
		}
		account, err := repo.StrmAccount.FindByID(ctx, accountID)
		if err != nil || account == nil {
			return false
		}
		return strings.EqualFold(strings.TrimSpace(account.Provider), provider)
	case "cloud":
		accounts, err := repo.StrmAccount.List(ctx)
		if err != nil {
			return false
		}
		for i := range accounts {
			if strings.EqualFold(strings.TrimSpace(accounts[i].Provider), provider) {
				return true
			}
		}
	}
	return false
}

// BuildRelativeCloudPlayURL 构造相对的云盘播放 API 路径。
func BuildRelativeCloudPlayURL(typ, ref string) string {
	return "/api/cloud/play/" + url.PathEscape(strings.TrimSpace(typ)) + "?" + url.Values{"ref": []string{ref}}.Encode()
}

// withAuthToken propagates the caller's auth token onto an internal redirect
// target. A browser <video> element cannot send Authorization headers or
// cookies when it follows a 302, so the cloud 302 chain
// (/api/stream?token=… → /api/cloud/play → CDN) would otherwise hit
// /api/cloud/play unauthenticated and 401. We only attach the token to our
// own relative API endpoints — never to an absolute external direct link —
// so the JWT is never leaked off-site (e.g. to the cloud CDN).
func withAuthToken(target string, r *http.Request) string {
	return withAuthTokenForInternalRedirect(target, r, "")
}

func withAuthTokenForInternalRedirect(target string, r *http.Request, publicBase string) string {
	if r == nil {
		return target
	}
	if strings.HasPrefix(target, "//") {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	if u.IsAbs() && !isInternalAPIURL(u, r, publicBase) {
		return target
	}
	if !strings.HasPrefix(strings.ToLower(u.Path), "/api/") {
		return target
	}
	tok := requestToken(r)
	if tok == "" {
		return target
	}
	q := u.Query()
	if q.Get("token") == "" {
		q.Set("token", tok)
	}
	if q.Get("media_id") == "" && strings.HasPrefix(strings.ToLower(u.Path), "/api/cloud/play/") {
		if mediaID := playbackMediaIDFromRequestPath(r.URL.Path); mediaID != "" {
			q.Set("media_id", mediaID)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func playbackMediaIDFromRequestPath(pathValue string) string {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		return ""
	}
	segments := strings.Split(strings.Trim(pathValue, "/"), "/")
	lower := make([]string, len(segments))
	for i, segment := range segments {
		lower[i] = strings.ToLower(segment)
	}
	var mediaID string
	switch {
	case len(segments) >= 3 && lower[0] == "api" && lower[1] == "stream":
		mediaID = segments[2]
	case len(segments) >= 4 && lower[0] == "emby" && lower[1] == "api" && lower[2] == "stream":
		mediaID = segments[3]
	case len(segments) >= 3 && lower[0] == "videos":
		mediaID = segments[1]
	}
	if decoded, err := url.PathUnescape(mediaID); err == nil {
		mediaID = decoded
	}
	return strings.TrimSpace(mediaID)
}

func absoluteInternalRedirect(target string, r *http.Request) string {
	if r == nil || target == "" || strings.HasPrefix(target, "//") {
		return target
	}
	u, err := url.Parse(target)
	if err != nil || u.IsAbs() || !strings.HasPrefix(target, "/") {
		return target
	}
	scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return target
	}
	u.Scheme = scheme
	u.Host = host
	return u.String()
}

func isInternalAPIURL(u *url.URL, r *http.Request, publicBase string) bool {
	if u == nil || !strings.HasPrefix(strings.ToLower(u.Path), "/api/") {
		return false
	}
	targetHost := strings.ToLower(strings.TrimSpace(u.Host))
	if targetHost == "" {
		return true
	}
	if r != nil {
		if host := strings.ToLower(strings.TrimSpace(r.Host)); host != "" && targetHost == host {
			return true
		}
		if host := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))); host != "" && targetHost == host {
			return true
		}
	}
	if publicBase != "" {
		if base, err := url.Parse(publicBase); err == nil && strings.EqualFold(strings.TrimSpace(base.Host), targetHost) {
			return true
		}
	}
	return false
}

// requestToken extracts the bearer JWT from the incoming request the same way
// the auth middleware does (Authorization header, Emby token headers, or the
// token / api_key query params used by <video>.src).
func requestToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	for _, hk := range []string{"X-Emby-Token", "X-MediaBrowser-Token"} {
		if v := strings.TrimSpace(r.Header.Get(hk)); v != "" {
			return v
		}
	}
	for _, hk := range []string{"X-Emby-Authorization", "X-MediaBrowser-Authorization"} {
		if token := streamTokenFromAuthHeader(r.Header.Get(hk)); token != "" {
			return token
		}
	}
	if token := streamTokenFromAuthHeader(r.Header.Get("Authorization")); token != "" {
		return token
	}
	for _, k := range []string{"token", "api_key", "apiKey", "ApiKey"} {
		if v := strings.TrimSpace(r.URL.Query().Get(k)); v != "" {
			return v
		}
	}
	return ""
}

func streamTokenFromAuthHeader(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, prefix := range []string{"Bearer ", "Emby "} {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(value, prefix))
		}
	}
	if strings.HasPrefix(value, "MediaBrowser ") || strings.Contains(value, "Token=") {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "MediaBrowser "))
			if !strings.HasPrefix(part, "Token=") {
				continue
			}
			token := strings.TrimSpace(strings.TrimPrefix(part, "Token="))
			return strings.Trim(token, `"`)
		}
		return ""
	}
	return value
}
