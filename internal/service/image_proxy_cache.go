package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// transparent1x1PNG is a baseline 67-byte PNG used as a fallback when the
// upstream image cannot be retrieved, so browser layouts never collapse.
var transparent1x1PNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

// detectContentType returns the MIME type of data using the first 512 bytes.
func detectContentType(data []byte) string {
	if len(data) > 512 {
		return http.DetectContentType(data[:512])
	}
	return http.DetectContentType(data)
}

func isImageContentType(ctype string) bool {
	ctype = strings.ToLower(strings.TrimSpace(strings.Split(ctype, ";")[0]))
	return strings.HasPrefix(ctype, "image/")
}

func validImageContentType(data []byte) (string, bool) {
	detected := detectContentType(data)
	if isImageContentType(detected) && !isTransparentPlaceholderData(data) {
		return detected, true
	}
	return "", false
}

func isTransparentPlaceholderData(data []byte) bool {
	return bytes.Equal(data, transparent1x1PNG)
}

// servePlaceholder writes a 1x1 transparent PNG to w. Used as a fallback
// when upstream fetch fails so the browser layout stays intact.
func servePlaceholder(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(transparent1x1PNG)
}

func serveCachedPlaceholder(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", imagePlaceholderCacheControl)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(transparent1x1PNG)
}

// remoteImageCachePaths 返回上游原图池的路径（带 URL 校验）。
func (p *ImageProxy) remoteImageCachePaths(raw string) (string, string, string, error) {
	if _, err := p.validateURL(raw); err != nil {
		return "", "", "", err
	}
	key, cachePath, failPath := p.remoteImageCachePathsForValidated(raw)
	return key, cachePath, failPath, nil
}

// remoteImageCachePathsForValidated 返回上游原图池的缓存路径。
func (p *ImageProxy) remoteImageCachePathsForValidated(raw string) (string, string, string) {
	return p.remoteImageCachePathsInPool(raw, imageOriginalCacheSubdir)
}

// remoteImageCachePathsInPool 生成某个池内的缓存路径。池决定清理策略：
// 原图池小配额 + 短保留，成品池（挂载 Emby 按尺寸产出的图）长期保留。
func (p *ImageProxy) remoteImageCachePathsInPool(raw, pool string) (string, string, string) {
	sum := sha256.Sum256([]byte(imageCacheKeyURL(raw)))
	key := hex.EncodeToString(sum[:])
	cachePath := filepath.Join(p.cacheDir, pool, key)
	return key, cachePath, cachePath + ".fail"
}

// remoteImageCachePathsEveryPool 返回同一个地址在所有池中的缓存路径。调用方
// 只知道原始 URL，无法判断它是原图还是挂载 Emby 按尺寸返回的成品，因此清理
// 类操作（refresh/retry）需要两个池都试一遍。
func (p *ImageProxy) remoteImageCachePathsEveryPool(raw string) [][3]string {
	pools := []string{imageOriginalCacheSubdir, imageRenditionCacheSubdir}
	out := make([][3]string, 0, len(pools))
	for _, pool := range pools {
		key, cachePath, failPath := p.remoteImageCachePathsInPool(raw, pool)
		out = append(out, [3]string{key, cachePath, failPath})
	}
	return out
}

// imageProxyMaxDownloadBytes 是单张图片的下载上限，防上游异常返回超大响应
// 把磁盘和内存打满。
const imageProxyMaxDownloadBytes = 32 << 20

var (
	// errImageCacheUnavailable 表示缓存目录不可写、连临时文件都建不出来。
	// 它发生在读取响应体之前，因此调用方还能退回内存缓冲，保证图片不因
	// 运维异常（磁盘只读/满）全部变成占位图。
	errImageCacheUnavailable = errors.New("image cache is not writable")
	// errImageCacheCommitFailed 表示响应体已被消费但无法提交到缓存（写入
	// 中断、rename 失败等），此时无法重放响应体，只能按拉取失败处理。
	errImageCacheCommitFailed = errors.New("image cache commit failed")
)

// cachedImageFileValid 只读取文件头判断缓存文件是否是可用图片。透明占位图
// 与空文件都视为不可用（历史实现曾把上游失败时的占位图写进缓存）。
func cachedImageFileValid(path string) error {
	file, err := os.Open(path) // #nosec G304 -- cache paths are SHA-derived under cacheDir.
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || stat.IsDir() || stat.Size() <= 0 {
		return errImageProxyNonImageContent
	}
	headerSize := 512
	if stat.Size() < int64(headerSize) {
		headerSize = int(stat.Size())
	}
	header := make([]byte, headerSize)
	n, readErr := io.ReadFull(file, header)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return readErr
	}
	header = header[:n]
	if !isImageContentType(detectContentType(header)) {
		return errImageProxyNonImageContent
	}
	// 透明占位图恰好 67 字节；只看头部就够（detectContentType 也只读前 512 字节）。
	if n == len(transparent1x1PNG) && bytes.Equal(header, transparent1x1PNG) {
		return errImageProxyNonImageContent
	}
	return nil
}

// streamImageToCache 把上游响应体流式写入临时文件，校验确为图片后原子替换到
// cachePath。原图常有数兆字节，旧实现每次都要先整张读进内存再写盘，在小内存
// 主机上几个并发海报请求就能把内存顶满。
func (p *ImageProxy) streamImageToCache(cachePath, failPath string, body io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o750); err != nil {
		p.warn("imageproxy: cache dir failed", err)
		return errImageCacheUnavailable
	}
	tmp, err := os.CreateTemp(p.cacheDir, "img-*.tmp")
	if err != nil {
		p.warn("imageproxy: cache temp file failed", err)
		return errImageCacheUnavailable
	}
	tmpName := tmp.Name()
	discard := func() { _ = os.Remove(tmpName) }

	written, copyErr := io.Copy(tmp, io.LimitReader(body, imageProxyMaxDownloadBytes))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		discard()
		if copyErr != nil {
			return copyErr
		}
		return fmt.Errorf("%w: %v", errImageCacheCommitFailed, closeErr)
	}
	if written == 0 {
		discard()
		return errors.New("upstream image body is empty")
	}
	if err := cachedImageFileValid(tmpName); err != nil {
		discard()
		return err
	}
	if err := os.Rename(tmpName, cachePath); err != nil {
		discard()
		return fmt.Errorf("%w: %v", errImageCacheCommitFailed, err)
	}
	_ = os.Remove(failPath)
	return nil
}

func serveCachedImageFile(w http.ResponseWriter, r *http.Request, key, cachePath string) bool {
	return serveImageFile(w, r, key, cachePath, imageBrowserCacheControl)
}

func serveImageFile(w http.ResponseWriter, r *http.Request, key, path, cacheControl string) bool {
	file, err := os.Open(path) // #nosec G304 -- caller only passes validated local paths or SHA-derived cache paths.
	if err != nil {
		return false
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || stat.IsDir() || stat.Size() <= 0 {
		return false
	}
	var sample [512]byte
	n, _ := file.Read(sample[:])
	_, _ = file.Seek(0, io.SeekStart)
	ctype := detectContentType(sample[:n])
	if !isImageContentType(ctype) {
		return false
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("ETag", imageFileETag(key, stat))
	http.ServeContent(w, r, key, stat.ModTime(), file)
	return true
}

func imageFileETag(key string, stat os.FileInfo) string {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "image"
	}
	sum := sha256.Sum256([]byte(key))
	return `"img-` + hex.EncodeToString(sum[:8]) + "-" + strconv.FormatInt(stat.Size(), 16) + "-" + strconv.FormatInt(stat.ModTime().Unix(), 16) + `"`
}

func (p *ImageProxy) markImageFetchFailed(failPath string) {
	if err := os.MkdirAll(filepath.Dir(failPath), 0o750); err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = os.WriteFile(failPath, []byte(time.Now().Format(time.RFC3339Nano)), 0o600)
}
