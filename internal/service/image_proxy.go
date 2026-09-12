// Package service — image proxy.
//
// Some deployments cannot reach image.tmdb.org directly (GFW, internal-only
// networks). ImageProxy fronts a remote image URL so the browser only ever
// talks to the MeBox origin. The proxy:
//
//   - validates the URL scheme is http/https,
//   - streams bytes through with a small disk cache under cache/images,
//   - falls back to a transparent 1×1 PNG on upstream failure so the UI
//     never breaks layout,
//   - honors HTTP(S)_PROXY environment variables so users behind GFW can
//     route image fetches through their proxy.
package service

import (
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

// ImageProxy fetches and caches remote images on behalf of the browser.
type ImageProxy struct {
	cfg      *config.Config
	log      *zap.Logger
	client   *http.Client
	cacheDir string
	mu       sync.Mutex

	// resizeSem bounds concurrent decode/resize jobs. Emby TV clients request
	// poster grids in bursts; letting every request decode a source image at
	// once causes CPU and memory spikes that make the whole UI feel sluggish.
	resizeSemMu sync.Mutex
	resizeSem   chan struct{}

	// libraryRootsFn returns the configured media library roots so that
	// sidecar poster/artwork files stored alongside media (under arbitrary
	// per-library paths) are allowed by isAllowedLocalPath. It is provided
	// by the service container after construction and may be nil in tests.
	libraryRootsFn func() []string
	libRootsMu     sync.Mutex
	libRootsCache  []string
	libRootsAt     time.Time

	// allowedRemoteHostsFn returns hostnames or IPs of explicitly configured
	// upstream services (e.g. remote Emby mounts) that should bypass SSRF private IP checks.
	allowedRemoteHostsFn func() []string
	allowedHostsMu       sync.Mutex
	allowedHostsCache    map[string]bool
	allowedHostsAt       time.Time
}

const (
	imageBrowserCacheControl     = "public, max-age=2592000, immutable"
	imagePlaceholderCacheControl = "no-store"
	imageMaxResizeConcurrency    = 4
)

// NewImageProxy is the constructor.
func NewImageProxy(cfg *config.Config, log *zap.Logger) *ImageProxy {
	proxy := &ImageProxy{
		cfg:      cfg,
		log:      log,
		cacheDir: filepath.Join(cfg.Cache.CacheDir, "images"),
	}
	proxy.resizeSem = make(chan struct{}, imageResizeConcurrency())

	// Honor HTTP(S)_PROXY env vars so deployments behind GFW can pull
	// from image.tmdb.org via their HTTP proxy without extra config. On
	// Windows we also honor the current user's system proxy settings.
	transport := NewExternalTransport()
	if proxyConfiguredForImageFetch() {
		// 走本地代理（如 127.0.0.1:7890）时，拨号目标是代理本身，
		// 连接层 SSRF 校验会误杀本地回环代理；此时沿用 URL 级校验。
		log.Info("image proxy: outbound proxy detected, connection-level SSRF guard disabled")
	} else {
		// 仅 URL 解析层的 isPrivateHost 可被十进制/十六进制 IP、解析到
		// 私网的域名与 DNS rebinding 绕过；在拨号层对最终连接 IP 做二次
		// 校验（含重定向后的每条连接）堵住该旁路。
		// 用户明确配置的远程挂载源（如内网 Emby）豁免该私网限制。
		dialer := &net.Dialer{
			Timeout: 15 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				if proxy.isAllowedRemoteHost(host) {
					return nil
				}
				ip := net.ParseIP(host)
				if ip == nil {
					return errors.New("image proxy: refusing non-IP dial target")
				}
				if isPrivateIP(ip) {
					return errors.New("image proxy: requests to private/internal hosts are not allowed")
				}
				return nil
			},
		}
		transport.DialContext = dialer.DialContext
	}

	proxy.client = &http.Client{Timeout: 30 * time.Second, Transport: transport}
	return proxy
}

// proxyConfiguredForImageFetch 探测环境变量或系统代理是否会影响图片抓取。
func proxyConfiguredForImageFetch() bool {
	req, err := http.NewRequest(http.MethodGet, "https://image.tmdb.org/", nil)
	if err != nil {
		return false
	}
	proxy, err := ProxyFromEnvironmentOrSystem(req)
	return err == nil && proxy != nil
}

// SetLibraryRootsProvider injects a callback that returns the current set of
// media library root directories. Sidecar posters live under these roots
// (which are arbitrary, user-defined, and not necessarily under the
// configured movies/tv/anime dirs), so they must be treated as allowed
// local-image locations.
func (p *ImageProxy) SetLibraryRootsProvider(fn func() []string) {
	p.libraryRootsFn = fn
}

// libraryRoots returns the cached library roots, refreshing at most every
// 30 seconds to avoid a DB hit per image request (posters load in bulk).
func (p *ImageProxy) libraryRoots() []string {
	if p.libraryRootsFn == nil {
		return nil
	}
	p.libRootsMu.Lock()
	defer p.libRootsMu.Unlock()
	if p.libRootsCache != nil && time.Since(p.libRootsAt) < 30*time.Second {
		return p.libRootsCache
	}
	p.libRootsCache = p.libraryRootsFn()
	p.libRootsAt = time.Now()
	return p.libRootsCache
}

// SetAllowedRemoteHostsProvider injects a callback that returns hostnames or IPs
// of explicitly configured remote services (e.g. remote Emby mounts). Requests to
// these hosts bypass SSRF private-IP restrictions.
func (p *ImageProxy) SetAllowedRemoteHostsProvider(fn func() []string) {
	p.allowedRemoteHostsFn = fn
}

func (p *ImageProxy) isAllowedRemoteHost(host string) bool {
	if p == nil || p.allowedRemoteHostsFn == nil {
		return false
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	// Strip port if present
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = strings.ToLower(strings.TrimSpace(h))
	}

	p.allowedHostsMu.Lock()
	defer p.allowedHostsMu.Unlock()
	if p.allowedHostsCache == nil || time.Since(p.allowedHostsAt) >= 30*time.Second {
		rawList := p.allowedRemoteHostsFn()
		cache := make(map[string]bool, len(rawList))
		for _, item := range rawList {
			item = strings.ToLower(strings.TrimSpace(item))
			if item == "" {
				continue
			}
			if h, _, err := net.SplitHostPort(item); err == nil {
				item = strings.ToLower(strings.TrimSpace(h))
			}
			cache[item] = true
		}
		p.allowedHostsCache = cache
		p.allowedHostsAt = time.Now()
	}
	return p.allowedHostsCache[host]
}

// imageResizeConcurrency keeps decode/resize concurrency within the number
// of CPU threads the process is allowed to use, capped to avoid large
// temporary RGBA buffers on tiny hosts.
func imageResizeConcurrency() int {
	n := runtime.GOMAXPROCS(0)
	if n < 1 {
		n = 1
	}
	if n > imageMaxResizeConcurrency {
		n = imageMaxResizeConcurrency
	}
	return n
}

// Prune removes oldest cached images until disk usage is within the configured limit.
func (p *ImageProxy) Prune() (PruneImageCacheResult, error) {
	if p.cfg == nil || p.cfg.Cache.ImagesMaxSizeMB <= 0 {
		return PruneImageCacheResult{}, nil
	}
	maxBytes := int64(p.cfg.Cache.ImagesMaxSizeMB) * 1024 * 1024
	return PruneImageCache(p.cacheDir, maxBytes)
}
