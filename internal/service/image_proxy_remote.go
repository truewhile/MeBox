package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errImageProxyRequestSetup = errors.New("image proxy request setup failed")
var errImageProxyNonImageContent = errors.New("upstream returned non-image content")

// prefetchCardResizeOptions 对应前端 ARTWORK.posterCard（见 web/src/api/client.ts）：
// 卡片是海报墙最常请求的档位，刮削阶段预生成它能让首个列表请求直接命中缓存。
// 若前端调整该预设，这里只是白生成一份用不到的档位（约几十 KB），不影响正确性。
var prefetchCardResizeOptions = imageResizeOptions{MaxWidth: 480, MaxHeight: 600, Quality: 80}

func (p *ImageProxy) PrefetchRemote(ctx context.Context, raw string) error {
	_, _, err := p.Fetch(ctx, raw)
	return err
}

// PrefetchCardVariant 预取原图后再离线生成卡片档位的缩略图。刮削是后台任务，
// 在这里做掉解码可以把海报墙首屏的 CPU 抖动移到请求路径之外。
//
// 预生成失败不影响预取结果：预取的成功含义是「图片可达」（刮削据此决定是否
// 替换旧图），而缩略图只是优化，客户端首次请求时会自己生成。
func (p *ImageProxy) PrefetchCardVariant(ctx context.Context, raw string) error {
	if err := p.PrefetchRemote(ctx, raw); err != nil {
		return err
	}
	if !isHTTPish(raw) {
		return nil
	}
	_, cachePath, _ := p.remoteImageCachePathsForValidated(raw)
	if err := p.ensureResizeCache(ctx, cachePath, prefetchCardResizeOptions); err != nil {
		p.warn("imageproxy: prefetch card variant failed", err)
	}
	return nil
}

func (p *ImageProxy) RemoveCached(raw string) error {
	if !isHTTPish(raw) {
		return nil
	}
	if _, err := p.validateURL(raw); err != nil {
		return nil
	}
	var firstErr error
	for _, paths := range p.remoteImageCachePathsEveryPool(raw) {
		cachePath, failPath := paths[1], paths[2]
		if err := os.Remove(cachePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			firstErr = err
		}
		if err := os.Remove(failPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			firstErr = err
		}
	}
	return firstErr
}

func (p *ImageProxy) RemoveFailed(raw string) error {
	if !isHTTPish(raw) {
		return nil
	}
	if _, err := p.validateURL(raw); err != nil {
		return nil
	}
	var firstErr error
	for _, paths := range p.remoteImageCachePathsEveryPool(raw) {
		failPath := paths[2]
		if err := os.Remove(failPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			firstErr = err
		}
	}
	return firstErr
}

// Serve writes the requested image to w. Caller is expected to validate
// the JWT before invoking it.
func (p *ImageProxy) Serve(ctx context.Context, w http.ResponseWriter, r *http.Request, raw string) error {
	// Emby 客户端用 maxWidth / maxHeight / quality 请求缩略图。不解析这些
	// 参数就会把多兆字节的原图发给客户端，移动端往往在下载中途超时。
	opts := parseImageResizeOptions(r)
	if isLocalImagePath(raw) {
		return p.serveLocalImage(w, r, raw, opts)
	}
	return p.serveRemoteImage(ctx, w, r, raw, opts)
}

func (p *ImageProxy) serveLocalImage(w http.ResponseWriter, r *http.Request, raw string, opts imageResizeOptions) error {
	path := filepath.Clean(raw)
	abs, err := filepath.Abs(path)
	if err != nil || !p.isAllowedLocalPath(abs) {
		servePlaceholder(w)
		return nil
	}
	if opts.active() && p.serveResizedFromFile(w, r, abs, opts) {
		return nil
	}
	if !serveImageFile(w, r, filepath.Base(abs), abs, imageBrowserCacheControl) {
		servePlaceholder(w)
	}
	return nil
}

func (p *ImageProxy) serveRemoteImage(ctx context.Context, w http.ResponseWriter, r *http.Request, raw string, opts imageResizeOptions) error {
	u, err := p.validateURL(raw)
	if err != nil {
		return err
	}
	host := strings.ToLower(u.Host)
	// 已配置的远程 Emby 挂载：把尺寸直接转发给远端生成缩略图，缓存键也用
	// 带尺寸的地址，这样不同尺寸各自缓存互不覆盖。
	fetchURL := p.upstreamImageFetchURL(raw, opts)
	// 尺寸被转发给挂载的远端时，上游返回的就是客户端要的最终尺寸成品，归入
	// 成品池长期保留；其他上游返回的是原图，只是生成各种尺寸的原料，归入
	// 原图池（小配额 + 短保留）。
	pool := imageOriginalCacheSubdir
	if fetchURL != raw {
		pool = imageRenditionCacheSubdir
	}
	key, cachePath, failPath := p.remoteImageCachePathsInPool(fetchURL, pool)
	forceRefresh := r.URL.Query().Get("refresh") != ""
	p.removeUnusableImageCache(cachePath, failPath)
	if !forceRefresh && p.serveCachedImage(w, r, key, cachePath, opts) {
		return nil
	}
	// No negative caching: a previously failed fetch is retried on every
	// subsequent request, so the image recovers as soon as upstream does.
	result, err := p.fetchAndCacheRemoteImageShared(ctx, fetchURL, host, cachePath, failPath)
	if err != nil {
		if forceRefresh && p.serveCachedImage(w, r, key, cachePath, opts) {
			return nil
		}
		if errors.Is(err, errImageProxyRequestSetup) {
			servePlaceholder(w)
		} else {
			serveCachedPlaceholder(w)
		}
		return nil
	}
	// 上游原图已落盘，缩放结果复用同一条缓存流水线。
	if opts.active() && p.serveResizedFromFile(w, r, cachePath, opts) {
		return nil
	}
	// 缓存目录不可写时的内存兜底：图片只在本次响应里直出，不落盘。
	if len(result.data) > 0 {
		w.Header().Set("Content-Type", result.contentType)
		w.Header().Set("Cache-Control", imageBrowserCacheControl)
		http.ServeContent(w, r, key, time.Now(), bytes.NewReader(result.data))
		return nil
	}
	if !p.serveCachedImage(w, r, key, cachePath, opts) {
		serveCachedPlaceholder(w)
	}
	return nil
}

// serveCachedImage 在远程原图已缓存时提供服务。请求带缩放参数时优先命中
// 缩放缓存，未命中则从已缓存的原图生成一份；缩放不可用时退回原图直出。
func (p *ImageProxy) serveCachedImage(w http.ResponseWriter, r *http.Request, key, cachePath string, opts imageResizeOptions) bool {
	if opts.active() && p.serveResizedFromFile(w, r, cachePath, opts) {
		return true
	}
	return serveCachedImageFile(w, r, key, cachePath)
}

func (p *ImageProxy) removeUnusableImageCache(cachePath, failPath string) {
	// 只读取文件头判断缓存是否可用。旧实现每次命中远程图片缓存都会把整个
	// 原图读进内存再丢弃，电视端批量加载海报时会产生大量无意义的磁盘 I/O。
	if _, err := os.Stat(cachePath); err != nil {
		return
	}
	if err := cachedImageFileValid(cachePath); err != nil {
		_ = os.Remove(cachePath)
		_ = os.Remove(failPath)
	}
}

func (p *ImageProxy) fetchAndCacheRemoteImage(ctx context.Context, raw, host, cachePath, failPath string) (remoteImageFetchResult, error) {
	var lastErr error
	for _, candidate := range p.remoteImageFetchClients(host) {
		result, err := p.fetchRemoteImageOnce(ctx, raw, host, candidate, cachePath, failPath)
		if err == nil {
			return result, nil
		}
		if errors.Is(err, errImageProxyRequestSetup) {
			return remoteImageFetchResult{}, err
		}
		lastErr = err
	}
	if p.canUseExternalImageFallback() && isDoubanImageHost(host) {
		data, ctype, _, err := fetchRemoteImageWithCurl(ctx, raw, host)
		if err == nil {
			p.writeImageCache(cachePath, failPath, "img-*.tmp", data)
			return remoteImageFetchResult{data: data, contentType: ctype}, nil
		}
		logImageFetchError(p.log, "imageproxy: curl fallback failed", host, "curl", err)
		lastErr = err
	}
	p.markImageFetchFailed(failPath)
	if lastErr == nil {
		lastErr = errors.New("upstream image fetch failed")
	}
	return remoteImageFetchResult{}, redactSensitiveError(lastErr)
}

// fetchAndCacheRemoteImageShared coalesces concurrent requests for the same
// upstream image. A poster can appear in the hero, a shelf and the detail page
// at the same time; without this guard every resize variant may fetch the same
// original before the first cache write finishes.
func (p *ImageProxy) fetchAndCacheRemoteImageShared(ctx context.Context, raw, host, cachePath, failPath string) (remoteImageFetchResult, error) {
	value, err, _ := p.fetchGroup.Do(cachePath, func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		return p.fetchAndCacheRemoteImage(loadCtx, raw, host, cachePath, failPath)
	})
	if err != nil {
		return remoteImageFetchResult{}, err
	}
	result, ok := value.(remoteImageFetchResult)
	if !ok {
		return remoteImageFetchResult{}, errors.New("upstream image fetch failed")
	}
	return result, nil
}

// Fetch pulls a remote image and returns bytes plus Content-Type using cache.
func (p *ImageProxy) Fetch(ctx context.Context, raw string) ([]byte, string, error) {
	u, err := p.validateURL(raw)
	if err != nil {
		return nil, "", err
	}
	host := strings.ToLower(u.Host)
	_, cachePath, failPath := p.remoteImageCachePathsForValidated(raw)
	p.removeUnusableImageCache(cachePath, failPath)
	if data, err := os.ReadFile(cachePath); err == nil && len(data) > 0 { // #nosec G304 -- cachePath is SHA-derived under cacheDir.
		ctype := detectContentType(data)
		if isImageContentType(ctype) && !isTransparentPlaceholderData(data) {
			return data, ctype, nil
		}
		_ = os.Remove(cachePath)
		_ = os.Remove(failPath)
	}
	// No negative caching: a previously failed fetch is retried on every
	// subsequent request, so the image recovers as soon as upstream does.
	result, err := p.fetchAndCacheRemoteImage(ctx, raw, host, cachePath, failPath)
	if err != nil {
		return nil, "", err
	}
	// 正常路径只落盘，这里按需读回（调用方需要字节）。
	if len(result.data) > 0 {
		return result.data, result.contentType, nil
	}
	data, err := os.ReadFile(cachePath) // #nosec G304 -- cachePath is SHA-derived under cacheDir.
	if err != nil || len(data) == 0 {
		return nil, "", errors.New("cached image is unreadable")
	}
	return data, detectContentType(data), nil
}

// writeImageCache atomically writes the fetched original. The global mutex is
// deliberately not held: os.CreateTemp already yields a unique name and
// os.Rename is atomic, so the lock only serialized multi-megabyte disk writes
// and made one poster's write block every other image in flight.
func (p *ImageProxy) writeImageCache(cachePath, failPath, pattern string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o750); err != nil {
		return
	}
	tmp, tmpErr := os.CreateTemp(p.cacheDir, pattern)
	if tmpErr != nil {
		return
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return
	}
	_ = tmp.Close()
	if err := os.Rename(tmp.Name(), cachePath); err != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	_ = os.Remove(failPath)
}
