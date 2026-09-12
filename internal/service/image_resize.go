package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.uber.org/zap"

	_ "golang.org/x/image/bmp" // register BMP decoder for .bmp/.tbn sidecar art
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register WebP decoder for poster art
)

const (
	imageResizeDefaultQuality = 90
	imageResizeMinQuality     = 1
	imageResizeMaxQuality     = 100

	// imageResizeMaxSourcePixels 限制参与缩放的原图像素总量。解码一张
	// N 像素的图在内存中约需 4N 字节；没有上限时，一张异常的超大图
	// 就能在多张并发缩略图请求下打爆小内存主机。超过该上限时直接
	// 回退为原图直出，宁可不缩放也不冒 OOM 风险。
	imageResizeMaxSourcePixels = 30_000_000

	// imageResizeCacheSubdir 存放缩放结果，与远程原图缓存分开放，
	// 便于单独清理且不与原始字节流缓存互相覆盖。
	imageResizeCacheSubdir = "resized"
)

// imageResizeOptions 描述客户端通过图片 URL 查询参数请求的目标尺寸。
// Emby / Infuse / Yamby / RodelPlayer 等客户端普遍使用
// maxWidth / maxHeight / quality，也有客户端使用 width / height。
type imageResizeOptions struct {
	MaxWidth  int
	MaxHeight int
	Quality   int
}

// active 报告是否需要缩放。没有任何尺寸参数时返回 false，调用方保持
// 原有的原图直出路径（支持 Range / ETag，行为完全不变）。
func (o imageResizeOptions) active() bool {
	return o.MaxWidth > 0 || o.MaxHeight > 0
}

// encodingQuality 返回生效的 JPEG 编码质量，缺省 90。
func (o imageResizeOptions) encodingQuality() int {
	q := o.Quality
	if q < imageResizeMinQuality || q > imageResizeMaxQuality {
		return imageResizeDefaultQuality
	}
	return q
}

// parseImageResizeOptions 从请求查询串解析缩放参数。Emby 客户端对参数名
// 大小写不敏感，这里逐项做 EqualFold 匹配。
func parseImageResizeOptions(r *http.Request) imageResizeOptions {
	if r == nil || r.URL == nil {
		return imageResizeOptions{}
	}
	q := r.URL.Query()
	return imageResizeOptions{
		MaxWidth:  firstPositiveQueryInt(q, "maxWidth", "width"),
		MaxHeight: firstPositiveQueryInt(q, "maxHeight", "height"),
		Quality:   firstPositiveQueryInt(q, "quality"),
	}
}

// firstPositiveQueryInt 按顺序返回第一个能解析为正数的查询参数。
func firstPositiveQueryInt(q url.Values, names ...string) int {
	for _, name := range names {
		for key, values := range q {
			if !strings.EqualFold(key, name) {
				continue
			}
			for _, raw := range values {
				raw = strings.TrimSpace(raw)
				if n, err := strconv.Atoi(raw); err == nil && n > 0 {
					return n
				}
				// Emby 客户端偶发传入 "400.0" 这类浮点字面量。
				if f, err := strconv.ParseFloat(raw, 64); err == nil && f > 0 {
					return int(f)
				}
			}
		}
	}
	return 0
}

// fitSize 按等比缩放把 srcW x srcH 装进 maxW/maxH 边界，且从不放大。
func fitSize(srcW, srcH, maxW, maxH int) (int, int) {
	if srcW <= 0 || srcH <= 0 {
		return srcW, srcH
	}
	scale := 1.0
	if maxW > 0 && srcW > maxW {
		scale = math.Min(scale, float64(maxW)/float64(srcW))
	}
	if maxH > 0 && srcH > maxH {
		scale = math.Min(scale, float64(maxH)/float64(srcH))
	}
	if scale >= 1 {
		return srcW, srcH
	}
	dstW := int(math.Round(float64(srcW) * scale))
	dstH := int(math.Round(float64(srcH) * scale))
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}
	return dstW, dstH
}

var errImageResizeTooLarge = errors.New("image source exceeds resize pixel budget")

// resizeImageData 按选项缩放图片并重新编码。返回 unchanged=true 表示原图
// 本身已满足目标尺寸，调用方应直接输出原始字节（不重复编码、不损失质量）。
func resizeImageData(data []byte, o imageResizeOptions) (out []byte, ctype string, unchanged bool, err error) {
	if len(data) == 0 || !o.active() {
		return data, detectContentType(data), true, nil
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", false, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", false, errors.New("invalid image dimensions")
	}
	// 先用 DecodeConfig 判断是否需要解码整图：图已够小就零成本返回原字节。
	dstW, dstH := fitSize(cfg.Width, cfg.Height, o.MaxWidth, o.MaxHeight)
	if dstW == cfg.Width && dstH == cfg.Height {
		return data, detectContentType(data), true, nil
	}
	if int64(cfg.Width)*int64(cfg.Height) > imageResizeMaxSourcePixels {
		return nil, "", false, errImageResizeTooLarge
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", false, err
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)

	// 只有可能带透明的源格式才需要逐像素确认，避免 JPEG 的无谓遍历。
	// 写实海报的 PNG 通常比等价 JPEG 大一个数量级，因此在确认不含透明
	// 像素后统一转 JPEG —— 客户端本来就只按缩略图显示。
	if strings.EqualFold(format, "png") && !isOpaqueImage(dst) {
		var buf bytes.Buffer
		if err := png.Encode(&buf, dst); err != nil {
			return nil, "", false, err
		}
		return buf.Bytes(), "image/png", false, nil
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: o.encodingQuality()}); err != nil {
		return nil, "", false, err
	}
	return buf.Bytes(), "image/jpeg", false, nil
}

// isOpaqueImage 逐像素确认图像不含透明像素。
func isOpaqueImage(img *image.RGBA) bool {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

// resizeCacheKey 生成缩放结果的缓存键，覆盖源文件身份（路径 + 大小 +
// 修改时间）与全部影响输出的参数，源文件被替换后不会命中陈旧缩略图。
func (o imageResizeOptions) resizeCacheKey(sourceID string, stat os.FileInfo) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "v1|%s|%dx%d|q%d", sourceID, o.MaxWidth, o.MaxHeight, o.encodingQuality())
	if stat != nil {
		_, _ = fmt.Fprintf(h, "|%d|%d", stat.Size(), stat.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (p *ImageProxy) resizeCachePath(key string) string {
	return filepath.Join(p.cacheDir, imageResizeCacheSubdir, key+".img")
}

// acquireResizeSlot bounds CPU-heavy decode/resize work. Returning false means
// the caller should fall back to the original image instead of blocking after
// the request has already been canceled.
func (p *ImageProxy) acquireResizeSlot(ctx context.Context) (func(), bool) {
	if p == nil {
		return func() {}, true
	}
	p.resizeSemMu.Lock()
	if p.resizeSem == nil {
		p.resizeSem = make(chan struct{}, imageResizeConcurrency())
	}
	sem := p.resizeSem
	p.resizeSemMu.Unlock()

	select {
	case sem <- struct{}{}:
		return func() { <-sem }, true
	case <-ctx.Done():
		return nil, false
	}
}

// serveResizedFromFile 从 srcPath 读取图片，按选项缩放后写出，并把结果缓存
// 到磁盘以免每次请求都重新解码。原图已满足目标尺寸时直接输出原文件。
// 返回 false 表示缩放不可用，调用方应回退到原图直出。
func (p *ImageProxy) serveResizedFromFile(w http.ResponseWriter, r *http.Request, srcPath string, o imageResizeOptions) bool {
	stat, err := os.Stat(srcPath)
	if err != nil || stat.IsDir() || stat.Size() <= 0 {
		return false
	}
	// 缓存命中必须发生在读原图和解码之前。否则电视端每次刷新海报墙都会
	// 把已经是缩略图缓存的原图重新解码、缩放一遍，造成明显的 CPU 抖动。
	key := o.resizeCacheKey(srcPath, stat)
	cachePath := p.resizeCachePath(key)
	if serveCachedImageFile(w, r, key, cachePath) {
		return true
	}

	release, ok := p.acquireResizeSlot(r.Context())
	if !ok {
		return false
	}
	defer release()

	// 等待并发槽期间，别的请求可能已经生成了同一张缩略图。
	if serveCachedImageFile(w, r, key, cachePath) {
		return true
	}

	data, err := os.ReadFile(srcPath) // #nosec G304 -- srcPath comes from an allowed local path or a SHA-derived cache path.
	if err != nil {
		return false
	}
	out, ctype, unchanged, err := resizeImageData(data, o)
	if err != nil {
		return false
	}
	if unchanged {
		// 原图已经在目标尺寸内：直接流式输出，保留 ETag / Range 语义。
		return serveImageFile(w, r, filepath.Base(srcPath), srcPath, imageBrowserCacheControl)
	}

	p.writeResizeCache(cachePath, out)
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", imageBrowserCacheControl)
	http.ServeContent(w, r, key, stat.ModTime(), bytes.NewReader(out))
	return true
}

// writeResizeCache 原子写入缩放结果；失败只记日志，不影响本次响应。
func (p *ImageProxy) writeResizeCache(cachePath string, data []byte) {
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		p.warn("imageproxy: resize cache mkdir failed", err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	tmp, err := os.CreateTemp(dir, "resized-*.tmp")
	if err != nil {
		p.warn("imageproxy: resize cache temp failed", err)
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
	}
}

func (p *ImageProxy) warn(msg string, err error) {
	if p == nil || p.log == nil {
		return
	}
	p.log.Warn(msg, zap.Error(err))
}
