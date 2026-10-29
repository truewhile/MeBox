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
	"context"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	"github.com/truewhile/MeBox/internal/config"
)

// ImageProxy fetches and caches remote images on behalf of the browser.
type ImageProxy struct {
	cfg        *config.Config
	log        *zap.Logger
	client     *http.Client
	cacheDir   string
	mu         sync.Mutex
	fetchGroup singleflight.Group

	// directClient bypasses HTTP_PROXY / OS proxy settings. It is built once and
	// shared: rebuilding the transport on every fetch discarded all keep-alive
	// connections, so every burst of poster requests paid a fresh TCP (and TLS)
	// handshake per image.
	directClient *http.Client

	// resizeSem bounds concurrent decode/resize jobs. Emby TV clients request
	// poster grids in bursts; letting every request decode a source image at
	// once causes CPU and memory spikes that make the whole UI feel sluggish.
	resizeSemMu sync.Mutex
	resizeSem   chan struct{}

	// resizeQueueWait 是一次请求最多为一格解码槽排队的时长。为 0 时用
	// defaultResizeQueueWait；测试会把它调小以免真的等待两秒。
	resizeQueueWait time.Duration

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

	// remoteEmbyTokenFn / refreshRemoteEmbyTokenFn let the proxy carry a fresh
	// credential for configured remote-Emby mounts. Image URLs are minted from
	// whatever token the account had when the payload was built, so a URL can
	// outlive its api_key; these hooks re-supply the current one and force a
	// re-login when the mount rejects it.
	remoteEmbyTokenFn        func(ctx context.Context, host string) (string, bool)
	refreshRemoteEmbyTokenFn func(ctx context.Context, host string) (string, error)
	remoteEmbyTokenMu        sync.Mutex
	remoteEmbyTokenCache     map[string]remoteEmbyImageToken
}

const (
	imageBrowserCacheControl     = "public, max-age=2592000, immutable"
	imagePlaceholderCacheControl = "no-store"
	imageMaxResizeConcurrency    = 4

	// imageOriginalCacheSubdir 存放上游原图（生成各种尺寸的原料，可再生）。
	imageOriginalCacheSubdir = "originals"
	// imageRenditionCacheSubdir 存放挂载 Emby 直接按尺寸产出的成品图：它们
	// 已经是客户端要的最终尺寸，和派生缩略图一样属于长期保留的热路径缓存。
	imageRenditionCacheSubdir = "renditions"

	// defaultResizeQueueWait 是请求为一格解码槽排队的最长时间。一张 3MP
	// 海报的解码+缩放约 0.15s，2 核机器两格槽位足以在几秒内消化一整屏
	// 海报；等待远好于「槽位忙就回退多兆字节原图」（那样每张图在外部网络
	// 下要十几秒）。超时后仍回退原图，保证请求不会被无限期挂住。
	defaultResizeQueueWait = 3 * time.Second
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
	proxy.guardTransport(transport)
	directTransport := NewInternalTransport()
	proxy.guardTransport(directTransport)
	proxy.client = &http.Client{Timeout: 30 * time.Second, Transport: transport}
	proxy.directClient = &http.Client{Timeout: 30 * time.Second, Transport: directTransport}
	return proxy
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

// Prune 只执行图片原图子配额与旧缓存清理；整个磁盘缓存的总量淘汰由调度器的
// cache_cleanup 任务（PruneCache）负责。
func (p *ImageProxy) Prune() (PruneImageCacheResult, error) {
	if p.cfg == nil {
		return PruneImageCacheResult{}, nil
	}
	var originalsMaxBytes int64
	if p.cfg.Cache.ImagesOriginalsMaxSizeMB > 0 {
		originalsMaxBytes = int64(p.cfg.Cache.ImagesOriginalsMaxSizeMB) * 1024 * 1024
	}
	if originalsMaxBytes <= 0 {
		return PruneImageCacheResult{}, nil
	}
	pools := ImageCachePools(p.cacheDir, originalsMaxBytes)
	return PruneImageCachePools(pools, 0)
}
