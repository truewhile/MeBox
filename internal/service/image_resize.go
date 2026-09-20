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
	//
	// 1200 万像素覆盖常见的高清海报（2892×4096 约 1180 万），单张解码
	// 峰值约 48MB RGBA；原先的 3000 万在 2 核 2GB 的机器上意味着单张
	// 就可能吃掉 120MB 以上，两个并发槽足以触发 OOM/大量换页。
	imageResizeMaxSourcePixels = 12_000_000

	// imageResizeCheapScaleRatio 是启用低成本插值的缩小比例阈值：目标尺寸
	// 小于源图一半时，CatmullRom 的收益肉眼不可见，但耗时和临时缓冲明显
	// 更高（电视端海报墙会同时请求几十张缩略图）。
	imageResizeCheapScaleRatio = 0.5

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
	resizeScaled(dst, src, cfg.Width, cfg.Height)

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

// useCheapScaleInterpolator 报告这次缩放是否该用低成本插值：目标尺寸在宽高
// 两个方向都缩到源图一半以下时，CatmullRom 的额外采样换来的观感差异不可见。
func useCheapScaleInterpolator(dstW, dstH, srcW, srcH int) bool {
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return false
	}
	return float64(dstW)/float64(srcW) <= imageResizeCheapScaleRatio &&
		float64(dstH)/float64(srcH) <= imageResizeCheapScaleRatio
}

// resizeScaled 把 src 缩放到 dst。大幅缩小时改用低成本插值：CatmullRom
// 与 ApproxBiLinear 在大比例缩小下观感差异看不出来，但前者要遍历更多
// 邻域样本，在 2 核机器上会明显拖慢海报墙的并发缩略图请求。
func resizeScaled(dst *image.RGBA, src image.Image, srcW, srcH int) {
	if dst == nil {
		return
	}
	bounds := dst.Bounds()
	interp := draw.Interpolator(draw.CatmullRom)
	if useCheapScaleInterpolator(bounds.Dx(), bounds.Dy(), srcW, srcH) {
		interp = draw.ApproxBiLinear
	}
	interp.Scale(dst, bounds, src, src.Bounds(), draw.Src, nil)
}

// isOpaqueImage 逐像素确认图像不含透明像素。dst 已知是 *image.RGBA，用
// RGBAAt 直取字段可以避免 At() 的接口分派与颜色模型换算（PNG 海报每次
// 生成缩略图都要走一遍全图扫描）。
func isOpaqueImage(img *image.RGBA) bool {
	if img == nil {
		return false
	}
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if img.RGBAAt(x, y).A != 0xff {
				return false
			}
		}
	}
	return true
}

// resizeSourceKey 生成源图身份键：只覆盖源文件身份（路径 + 大小 + mtime），不含
// 目标尺寸。同一源图的所有尺寸档位共用这个前缀，才能互相列举与派生（小图直接
// 从已缓存的大图缩小，而不是重新解码原图）。
func (o imageResizeOptions) resizeSourceKey(sourceID string, stat os.FileInfo) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "v2|%s", sourceID)
	if stat != nil {
		_, _ = fmt.Fprintf(h, "|%d|%d", stat.Size(), stat.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// resizeCacheKey 是「源图身份 + 档位」的稳定标识，用于 ETag 等需要区分档位的
// 场合（磁盘路径只用源图身份 + 文件名后缀）。
func (o imageResizeOptions) resizeCacheKey(sourceID string, stat os.FileInfo) string {
	return o.resizeSourceKey(sourceID, stat) + "." + o.resizeVariantSuffix()
}

func (p *ImageProxy) resizeCachePath(sourceKey string, o imageResizeOptions) string {
	return filepath.Join(p.cacheDir, imageResizeCacheSubdir, sourceKey+"."+o.resizeVariantSuffix()+".img")
}

// resizeVariantSuffix 把目标尺寸与质量写进缩放结果的文件名。这样同一源图的
// 各档尺寸可以被列举出来：请求小图时若已有更大的档位缓存，直接从它缩小即可，
// 不必再解码多兆字节的原图（解码 480×600 约 1MB RGBA，解码原图可达数十 MB）。
func (o imageResizeOptions) resizeVariantSuffix() string {
	return strconv.Itoa(o.MaxWidth) + "x" + strconv.Itoa(o.MaxHeight) + "q" + strconv.Itoa(o.encodingQuality())
}

// parseResizeVariantSuffix 解析 "<sourceKey>.<w>x<h>q<q>.img" 文件名中的尺寸。
func parseResizeVariantSuffix(name, sourceKey string) (width, height int, ok bool) {
	prefix := sourceKey + "."
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".img") {
		return 0, 0, false
	}
	spec := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".img")
	dimensions, quality, found := strings.Cut(spec, "q")
	if !found {
		return 0, 0, false
	}
	w, h, found := strings.Cut(dimensions, "x")
	if !found {
		return 0, 0, false
	}
	width, err := strconv.Atoi(w)
	if err != nil || width < 0 {
		return 0, 0, false
	}
	height, err = strconv.Atoi(h)
	if err != nil || height < 0 {
		return 0, 0, false
	}
	if _, err := strconv.Atoi(quality); err != nil {
		return 0, 0, false
	}
	return width, height, true
}

// largerCachedVariant 找出同一源图已缓存的、能覆盖目标尺寸的最小档位（宽高
// 都不小于目标）。返回空串表示没有可用的档位，调用方回退到解码原图。
func (p *ImageProxy) largerCachedVariant(key string, o imageResizeOptions) string {
	pattern := filepath.Join(p.cacheDir, imageResizeCacheSubdir, key+".*.img")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return ""
	}
	best := ""
	bestPixels := 0
	for _, path := range matches {
		width, height, ok := parseResizeVariantSuffix(filepath.Base(path), key)
		if !ok {
			continue
		}
		if width == o.MaxWidth && height == o.MaxHeight {
			continue // 精确档位，本该在上面就命中
		}
		if width < o.MaxWidth || height < o.MaxHeight {
			continue
		}
		pixels := width * height
		if best == "" || pixels < bestPixels {
			best, bestPixels = path, pixels
		}
	}
	return best
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

// tryAcquireResizeSlot 不等待。槽位忙时返回 false，调用方应改出原图。
func (p *ImageProxy) tryAcquireResizeSlot() (func(), bool) {
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
	default:
		return nil, false
	}
}

// acquireResizeSlotFor 对中等体积的图只尝试一次槽位。忙则放弃缩放，
// 避免背景图墙在 2 核机器上排成数秒。超大原图仍等待，以免直出多兆字节。
func (p *ImageProxy) acquireResizeSlotFor(ctx context.Context, size int64) (func(), bool) {
	if size > 0 && size <= resizeQueueBypassBytes {
		return p.tryAcquireResizeSlot()
	}
	return p.acquireResizeSlot(ctx)
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
	cachePath := p.resizeCachePath(o.resizeSourceKey(srcPath, stat), o)
	if serveCachedImageFile(w, r, key, cachePath) {
		return true
	}
	// 本地海报大多已经是一百多 KB 的 JPEG。先解码再缩放会把 2 核机器的
	// 并发槽（2）堵成数秒队列。体积已经适合直接下发时，不要排队。
	if serveCompactOriginal(w, r, srcPath, stat) {
		return true
	}

	release, ok := p.acquireResizeSlotFor(r.Context(), stat.Size())
	if !ok {
		return false
	}
	defer release()

	// 等待并发槽期间，别的请求可能已经生成了同一张缩略图。
	if serveCachedImageFile(w, r, key, cachePath) {
		return true
	}

	// 优先从同一源图已缓存的大尺寸档位缩小：小图（如 160px 模糊占位图）
	// 往往能直接由已缓存的卡片图派生，代价从“解码多兆字节原图”降为
	// “解码几百 KB 的档位文件”。
	sourcePath := srcPath
	if derived := p.largerCachedVariant(o.resizeSourceKey(srcPath, stat), o); derived != "" {
		sourcePath = derived
	}

	data, err := os.ReadFile(sourcePath) // #nosec G304 -- srcPath comes from an allowed local path or a SHA-derived cache path.
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

// ensureResizeCache 在请求路径之外预先生成某档缩略图（刮削预取用）。已存在
// 时直接返回，无需调用方再次解码。
func (p *ImageProxy) ensureResizeCache(ctx context.Context, srcPath string, o imageResizeOptions) error {
	if p == nil || !o.active() {
		return nil
	}
	stat, err := os.Stat(srcPath)
	if err != nil || stat.IsDir() || stat.Size() <= 0 {
		return errors.New("image source unavailable")
	}
	sourceKey := o.resizeSourceKey(srcPath, stat)
	cachePath := p.resizeCachePath(sourceKey, o)
	if _, err := os.Stat(cachePath); err == nil {
		return nil
	}
	release, ok := p.acquireResizeSlotFor(ctx, stat.Size())
	if !ok {
		return errors.New("image resize slot unavailable")
	}
	defer release()
	if _, err := os.Stat(cachePath); err == nil {
		return nil
	}
	sourcePath := srcPath
	if derived := p.largerCachedVariant(sourceKey, o); derived != "" {
		sourcePath = derived
	}
	data, err := os.ReadFile(sourcePath) // #nosec G304 -- srcPath is an allowed local path or a SHA-derived cache path.
	if err != nil {
		return err
	}
	out, _, unchanged, err := resizeImageData(data, o)
	if err != nil {
		return err
	}
	if unchanged {
		return nil // 源图已在目标尺寸内，客户端会直接使用原文件。
	}
	p.writeResizeCache(cachePath, out)
	return nil
}

// compactImageSkipBytes 是“直接出原图”的体积上限。超过它的原图（多兆字节
// 的剧照、未压缩 sidecar）仍然走缩放，避免把大文件直接塞给电视端。
// ponytail: 200KB 覆盖这台机器上的典型海报（平均约 100KB）；更大的图仍尝试缩放。
const compactImageSkipBytes = 200 * 1024

// resizeQueueBypassBytes 是“槽位忙就放弃缩放”的上限。超过它的原图继续排队，
// 以免把未压缩的剧照直接发给客户端。
const resizeQueueBypassBytes = 1536 * 1024

// serveCompactOriginal 在源文件已经很小且是浏览器可直接显示的 JPEG/WebP 时
// 跳过解码。返回 false 表示仍应走缩放路径。
func serveCompactOriginal(w http.ResponseWriter, r *http.Request, srcPath string, stat os.FileInfo) bool {
	if stat == nil || stat.Size() <= 0 || stat.Size() > compactImageSkipBytes {
		return false
	}
	file, err := os.Open(srcPath) // #nosec G304 -- srcPath is an allowed local path or a SHA-derived cache path.
	if err != nil {
		return false
	}
	var header [12]byte
	n, _ := file.Read(header[:])
	_ = file.Close()
	if !isCompactWebImage(header[:n]) {
		return false
	}
	return serveImageFile(w, r, filepath.Base(srcPath), srcPath, imageBrowserCacheControl)
}

func isCompactWebImage(header []byte) bool {
	if len(header) >= 3 && header[0] == 0xff && header[1] == 0xd8 && header[2] == 0xff {
		return true
	}
	return len(header) >= 12 &&
		string(header[0:4]) == "RIFF" &&
		string(header[8:12]) == "WEBP"
}

// writeResizeCache 原子写入缩放结果；失败只记日志，不影响本次响应。
// 与 writeImageCache 一样不再持有全局锁：临时文件名唯一、rename 原子，
// 持锁只会把缩略图的写盘和原图的写盘串成一条队。
func (p *ImageProxy) writeResizeCache(cachePath string, data []byte) {
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		p.warn("imageproxy: resize cache mkdir failed", err)
		return
	}
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
