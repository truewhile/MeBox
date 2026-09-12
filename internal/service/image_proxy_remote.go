package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

var errImageProxyRequestSetup = errors.New("image proxy request setup failed")
var errImageProxyNonImageContent = errors.New("upstream returned non-image content")

func (p *ImageProxy) PrefetchRemote(ctx context.Context, raw string) error {
	_, _, err := p.Fetch(ctx, raw)
	return err
}

func (p *ImageProxy) RemoveCached(raw string) error {
	if !isHTTPish(raw) {
		return nil
	}
	_, cachePath, failPath, err := p.remoteImageCachePaths(raw)
	if err != nil {
		return nil
	}
	if err := os.Remove(cachePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(failPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (p *ImageProxy) RemoveFailed(raw string) error {
	if !isHTTPish(raw) {
		return nil
	}
	_, _, failPath, err := p.remoteImageCachePaths(raw)
	if err != nil {
		return nil
	}
	if err := os.Remove(failPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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
	key, cachePath, failPath := p.remoteImageCachePathsForValidated(raw)
	forceRefresh := r.URL.Query().Get("refresh") != ""
	p.removeUnusableImageCache(cachePath, failPath)
	if !forceRefresh && p.serveCachedImage(w, r, key, cachePath, opts) {
		return nil
	}
	// No negative caching: a previously failed fetch is retried on every
	// subsequent request, so the image recovers as soon as upstream does.
	data, ctype, contentLength, err := p.fetchAndCacheRemoteImage(ctx, raw, host, cachePath, failPath)
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
	w.Header().Set("Content-Type", ctype)
	if contentLength != "" {
		w.Header().Set("Content-Length", contentLength)
	}
	modTime := time.Now()
	if stat, err := os.Stat(cachePath); err == nil && stat.Size() > 0 {
		modTime = stat.ModTime()
		w.Header().Set("ETag", imageFileETag(key, stat))
	}
	w.Header().Set("Cache-Control", imageBrowserCacheControl)
	http.ServeContent(w, r, key, modTime, bytes.NewReader(data))
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
	file, err := os.Open(cachePath) // #nosec G304 -- cachePath is SHA-derived under cacheDir.
	if err != nil {
		return
	}
	stat, err := file.Stat()
	if err != nil || stat.IsDir() || stat.Size() <= 0 {
		_ = file.Close()
		_ = os.Remove(cachePath)
		_ = os.Remove(failPath)
		return
	}
	headerSize := 512
	if stat.Size() < int64(headerSize) {
		headerSize = int(stat.Size())
	}
	header := make([]byte, headerSize)
	n, readErr := io.ReadFull(file, header)
	_ = file.Close()
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		_ = os.Remove(cachePath)
		_ = os.Remove(failPath)
		return
	}
	header = header[:n]
	// A transparent placeholder is exactly 67 bytes; checking the header alone
	// is enough for the normal image cache entries (they are much larger but
	// detectContentType only inspects the same leading 512 bytes anyway).
	// Close the handle before deleting: Windows refuses to delete an open file.
	if n > 0 && isImageContentType(detectContentType(header)) &&
		!(n == len(transparent1x1PNG) && bytes.Equal(header, transparent1x1PNG)) {
		return
	}
	_ = os.Remove(cachePath)
	_ = os.Remove(failPath)
}

func (p *ImageProxy) fetchAndCacheRemoteImage(ctx context.Context, raw, host, cachePath, failPath string) ([]byte, string, string, error) {
	if err := os.MkdirAll(p.cacheDir, 0o750); err != nil {
		p.log.Warn("imageproxy: mkdir failed", zap.String("dir", p.cacheDir), zap.Error(err))
		return nil, "", "", errImageProxyRequestSetup
	}
	var lastErr error
	for _, candidate := range p.remoteImageFetchClients() {
		data, ctype, contentLength, err := p.fetchRemoteImageOnce(ctx, raw, host, candidate)
		if err == nil {
			p.writeImageCache(cachePath, failPath, "img-*.tmp", data)
			return data, ctype, contentLength, nil
		}
		if errors.Is(err, errImageProxyRequestSetup) {
			return nil, "", "", err
		}
		lastErr = err
	}
	if p.canUseExternalImageFallback() && isDoubanImageHost(host) {
		data, ctype, contentLength, err := fetchRemoteImageWithCurl(ctx, raw, host)
		if err == nil {
			p.writeImageCache(cachePath, failPath, "img-*.tmp", data)
			return data, ctype, contentLength, nil
		}
		p.log.Warn("imageproxy: curl fallback failed", zap.String("host", host), zap.Error(err))
		lastErr = err
	}
	p.markImageFetchFailed(failPath)
	if lastErr == nil {
		lastErr = errors.New("upstream image fetch failed")
	}
	return nil, "", "", lastErr
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
	data, ctype, _, err := p.fetchAndCacheRemoteImage(ctx, raw, host, cachePath, failPath)
	return data, ctype, err
}

func (p *ImageProxy) writeImageCache(cachePath, failPath, pattern string, data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
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
