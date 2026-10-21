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
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// 媒体库封面拼图（Emby 风格的「封面集」）。
//
// Emby / Jellyfin 在没有管理员自定义封面时，会用库内几张海报拼成一张
// 2×2 的合成图作为库主图（folder image）。MeBox 早先只是挑评分最高的
// 那一张单图，于是每个库的封面看起来都像「随便选了一张」。这里按同样的
// 思路生成拼图，供 Emby 客户端、Web 和 App 共用同一份图片。

const (
	// libraryCoverDefaultWidth / Height 是拼图输出的默认画布。
	//
	// 库条目的 PrimaryImageAspectRatio 报的是 16:9（见 emby_system.go），
	// 客户端按这个比例摆卡片；生成同样比例的图可以避免客户端再裁一刀。
	libraryCoverDefaultWidth  = 480
	libraryCoverDefaultHeight = 270
	libraryCoverMinWidth      = 96
	libraryCoverMaxWidth      = 1920
	libraryCoverAspect        = 16.0 / 9.0

	// libraryCoverSourceLimit 是拼图实际取用的成员海报数上限。
	//
	// 封面是竖切分栏：三栏在 16:9 画布上每栏接近 3:5，既保留海报主体，又能
	// 让一个库有多部作品可辨识；再多就会把海报裁成很窄的竖条。
	libraryCoverSourceLimit = 3
	// libraryCoverMaxPanels 是合成函数能接受的最多分栏数（更长的来源列表
	// 只取前几张）。留 4 是给「按库类型调栏数」留的余量。
	libraryCoverMaxPanels = 4
	// libraryCoverCandidatePool 是挑选海报时的候选池大小。只看最近的一批
	// 成员，避免为一个库封面扫全库；对「最新入库」的库来说这批就是它最好
	// 的那几张。
	libraryCoverCandidatePool = 200

	libraryCoverQuality = 85
	libraryCoverSubdir  = "library-covers"
)

// libraryCoverSourceCache 缓存「库 → 成员海报来源」。Emby 客户端进入首页
// 时会对每个库各请求一次主图，把这个查询压到每库 10 分钟一次，避免几十个
// 200 行的列表查询同时打进来。
var libraryCoverSourceCache = struct {
	mu    sync.Mutex
	items map[string]libraryCoverSourceEntry
}{items: make(map[string]libraryCoverSourceEntry)}

type libraryCoverSourceEntry struct {
	sources   []string
	expiresAt time.Time
}

const libraryCoverSourceTTL = 10 * time.Minute

func cachedLibraryCoverSources(libraryID string) ([]string, bool) {
	libraryCoverSourceCache.mu.Lock()
	defer libraryCoverSourceCache.mu.Unlock()
	entry, ok := libraryCoverSourceCache.items[libraryID]
	if !ok || time.Now().After(entry.expiresAt) {
		if ok {
			delete(libraryCoverSourceCache.items, libraryID)
		}
		return nil, false
	}
	return entry.sources, true
}

func rememberLibraryCoverSources(libraryID string, sources []string) {
	libraryCoverSourceCache.mu.Lock()
	defer libraryCoverSourceCache.mu.Unlock()
	if len(libraryCoverSourceCache.items) > 4000 {
		libraryCoverSourceCache.items = make(map[string]libraryCoverSourceEntry)
	}
	libraryCoverSourceCache.items[libraryID] = libraryCoverSourceEntry{
		sources:   sources,
		expiresAt: time.Now().Add(libraryCoverSourceTTL),
	}
}

// LibraryCoverSources 返回媒体库封面拼图要用的海报来源，最多 limit 张。
//
// 选法与网页旧逻辑一致（库内最近一批成员，按 seriesArtworkScore 取质量
// 最高者），额外按剧集去重，避免四张海报全是同一部剧的不同集。
func LibraryCoverSources(ctx context.Context, repos *repository.Container, libraryID string, limit int) []string {
	if repos == nil || repos.Media == nil || strings.TrimSpace(libraryID) == "" {
		return nil
	}
	if limit <= 0 || limit > libraryCoverSourceLimit {
		limit = libraryCoverSourceLimit
	}
	if cached, ok := cachedLibraryCoverSources(libraryID); ok {
		if len(cached) > limit {
			return cached[:limit]
		}
		return cached
	}
	rows, err := repos.Media.ListByLibrariesFilteredNoCount(ctx, []string{libraryID}, 0, libraryCoverCandidatePool,
		repository.MediaQueryFilter{IncludeNSFW: true})
	if err != nil || len(rows) == 0 {
		return nil
	}
	type candidate struct {
		url   string
		key   string
		score int
	}
	candidates := make([]candidate, 0, len(rows))
	for i := range rows {
		row := rows[i]
		raw := strings.TrimSpace(row.PosterURL)
		if raw == "" {
			raw = strings.TrimSpace(row.BackdropURL)
		}
		if raw == "" {
			continue
		}
		key := mediaSeriesKey(row)
		if key == "" {
			key = "id:" + row.ID
		}
		candidates = append(candidates, candidate{url: raw, key: key, score: seriesArtworkScore(row)})
	}
	// 评分高的优先；同分保持数据库顺序（最近的在前），封面更贴近「最新入库」。
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })

	out := make([]string, 0, limit)
	seenKey := make(map[string]struct{}, limit)
	seenURL := make(map[string]struct{}, limit)
	for _, item := range candidates {
		if len(out) >= limit {
			break
		}
		if _, ok := seenKey[item.key]; ok {
			continue
		}
		if _, ok := seenURL[item.url]; ok {
			continue
		}
		seenKey[item.key] = struct{}{}
		seenURL[item.url] = struct{}{}
		out = append(out, item.url)
	}
	if len(out) == 0 {
		rememberLibraryCoverSources(libraryID, nil)
		return nil
	}
	rememberLibraryCoverSources(libraryID, out)
	return out
}

// LibraryCoverCacheDir 返回拼图缓存目录（与图片代理的 images/ 分开存放，
// 不会被图片缓存的配额清理误删）。
func LibraryCoverCacheDir(cfgCacheDir string) string {
	base := strings.TrimSpace(cfgCacheDir)
	if base == "" {
		base = "./cache"
	}
	return filepath.Join(base, libraryCoverSubdir)
}

// LibraryCoverImage 生成（或命中缓存）媒体库封面拼图。
//
// width/height 是期望的输出画布；服务端会按 16:9 收敛，返回的图片不会
// 超过该边界。sources 由调用方提供（见 LibraryCoverSources），这样 Emby
// 路由和 Web API 可以共用同一套生成与缓存逻辑。
func (p *ImageProxy) LibraryCoverImage(ctx context.Context, libraryID string, sources []string, width, height int) ([]byte, error) {
	if p == nil {
		return nil, errors.New("image proxy unavailable")
	}
	libraryID = strings.TrimSpace(libraryID)
	sources = normalizeCoverSources(sources)
	if libraryID == "" || len(sources) == 0 {
		return nil, errors.New("no library cover sources")
	}
	outW, outH := libraryCoverCanvas(width, height)
	cachePath, sourceKey := p.libraryCoverCachePath(libraryID, sources, outW, outH)
	if data, err := os.ReadFile(cachePath); err == nil && len(data) > 0 { // #nosec G304 -- cachePath is derived from a content hash.
		return data, nil
	}

	key := fmt.Sprintf("library-cover:%s-%dx%d-%s", libraryID, outW, outH, sourceKey)
	v, err, _ := p.fetchGroup.Do(key, func() (any, error) {
		if data, err := os.ReadFile(cachePath); err == nil && len(data) > 0 { // #nosec G304 -- cachePath is derived from a content hash.
			return data, nil
		}
		data, buildErr := p.buildLibraryCover(ctx, libraryID, sources, outW, outH, cachePath, sourceKey)
		if buildErr != nil {
			return nil, buildErr
		}
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	data, _ := v.([]byte)
	if len(data) == 0 {
		return nil, errors.New("empty library cover")
	}
	return data, nil
}

func (p *ImageProxy) buildLibraryCover(ctx context.Context, libraryID string, sources []string, width, height int, cachePath, sourceKey string) ([]byte, error) {
	images := p.fetchCoverSources(ctx, sources)
	if len(images) == 0 {
		return nil, errors.New("no library cover image fetched")
	}
	// 解码与合成是 CPU 密集操作：和缩略图共用同一组解码槽，避免一次首页
	// 刷新把 2 核主机打满。
	release, ok := p.acquireResizeSlotWithin(ctx, p.resizeQueueTimeout())
	if ok {
		defer release()
	}
	data, err := composeLibraryCover(images, width, height)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o750); err != nil {
		return data, nil
	}
	if err := writeFileAtomic(cachePath, data); err != nil {
		p.warn("write library cover cache failed", err)
		return data, nil
	}
	p.pruneLibraryCoverVariants(filepath.Dir(cachePath), libraryID, sourceKey)
	return data, nil
}

// fetchCoverSources 并发拉取成员海报（受图片代理自身的缓存/鉴权/SSRF 规则
// 保护）。单张失败只丢弃该张，其余仍然可以拼图。
func (p *ImageProxy) fetchCoverSources(ctx context.Context, sources []string) [][]byte {
	out := make([][]byte, len(sources))
	var wg sync.WaitGroup
	sem := make(chan struct{}, libraryCoverMaxPanels)
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for i, raw := range sources {
		i, raw := i, raw
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := p.coverSourceBytes(fetchCtx, raw)
			if err != nil || len(data) == 0 {
				return
			}
			out[i] = data
		}()
	}
	wg.Wait()
	compacted := make([][]byte, 0, len(out))
	for _, data := range out {
		if len(data) > 0 {
			compacted = append(compacted, data)
		}
	}
	return compacted
}

// coverSourceBytes 读取一张海报来源。
//
// 刮削会把 sidecar 海报存成 /media/... 这类绝对路径，而 fetch 路径只接受
// http(s)（validateURL 要求有 host），直接丢给 Fetch 会全部失败。本地路径
// 按图片代理同一套 isAllowedLocalPath 白名单读盘，远程地址才走 Fetch，
// 这样鉴权、SSRF 校验与磁盘缓存的行为与 /api/img 完全一致。
func (p *ImageProxy) coverSourceBytes(ctx context.Context, raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty cover source")
	}
	if isLocalImagePath(raw) {
		abs, err := filepath.Abs(filepath.Clean(raw))
		if err != nil {
			return nil, err
		}
		if !p.isAllowedLocalPath(abs) {
			return nil, errors.New("local cover path not allowed")
		}
		return os.ReadFile(abs) // #nosec G304 -- path validated against the library roots above.
	}
	data, _, err := p.Fetch(ctx, raw)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// libraryCoverCachePath 生成缓存路径。
//
// 文件名里的 sourceKey 只由成员海报列表决定（不含尺寸）：成员变化时换一批
// 文件，同一批成员的多个尺寸变体可以共存，不会被互相删掉。size 单独进文件名，
// 让 Emby（按客户端 maxWidth/maxHeight）与 Web/App（固定档位）各用自己的成品。
func (p *ImageProxy) libraryCoverCachePath(libraryID string, sources []string, width, height int) (string, string) {
	h := sha256.New()
	for _, src := range sources {
		_, _ = io.WriteString(h, src)
		_, _ = h.Write([]byte{0})
	}
	sourceKey := hex.EncodeToString(h.Sum(nil))[:16]
	name := fmt.Sprintf("%s-%dx%d-%s.jpg", safeCoverFileID(libraryID), width, height, sourceKey)
	return filepath.Join(LibraryCoverCacheDir(p.cacheDirBase()), name), sourceKey
}

func (p *ImageProxy) cacheDirBase() string {
	if p != nil && p.cfg != nil {
		return p.cfg.Cache.CacheDir
	}
	return ""
}

// pruneLibraryCoverVariants 清掉同一个库、旧成员列表留下的封面文件（含各
// 尺寸变体），当前成员列表对应的成品全部保留。
func (p *ImageProxy) pruneLibraryCoverVariants(dir, libraryID, keepSourceKey string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := safeCoverFileID(libraryID) + "-"
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".jpg") {
			continue
		}
		if strings.Contains(name, "-"+keepSourceKey+".jpg") {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name)) // #nosec G304 -- name comes from ReadDir of our own cache dir.
	}
}

// safeCoverFileID 让文件名只包含安全字符（库 ID 来自数据库，但仍做一次
// 白名单过滤，避免任何路径分隔符进入文件名）。
func safeCoverFileID(libraryID string) string {
	var b strings.Builder
	for _, r := range libraryID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "library"
	}
	return b.String()
}

func normalizeCoverSources(sources []string) []string {
	seen := make(map[string]struct{}, len(sources))
	out := make([]string, 0, len(sources))
	for _, raw := range sources {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		out = append(out, raw)
		if len(out) >= libraryCoverMaxPanels {
			break
		}
	}
	return out
}

// libraryCoverCanvas 把请求的尺寸收敛到 16:9 画布，并夹在合理区间内。
func libraryCoverCanvas(width, height int) (int, int) {
	if width <= 0 && height <= 0 {
		return libraryCoverDefaultWidth, libraryCoverDefaultHeight
	}
	if width <= 0 {
		width = int(float64(height)*libraryCoverAspect + 0.5)
	}
	if height <= 0 {
		height = int(float64(width)/libraryCoverAspect + 0.5)
	}
	// 客户端给的是一个「不超过」的盒子：按 16:9 装进去。
	if float64(width)/float64(height) > libraryCoverAspect {
		width = int(float64(height)*libraryCoverAspect + 0.5)
	} else {
		height = int(float64(width)/libraryCoverAspect + 0.5)
	}
	if width < libraryCoverMinWidth {
		width = libraryCoverMinWidth
		height = int(float64(width)/libraryCoverAspect + 0.5)
	}
	if width > libraryCoverMaxWidth {
		width = libraryCoverMaxWidth
		height = int(float64(width)/libraryCoverAspect + 0.5)
	}
	return width, height
}

// composeLibraryCover 把若干张海报拼成库封面：竖切成若干等宽竖条。
//
//   - 1 张：铺满整块画布；
//   - 2 张：左右各一半（默认形态，对应网页/客户端卡片的分屏观感）；
//   - 3/4 张：三等分 / 四等分竖条。
//
// 每格按中心裁切铺满（crop-to-fill），这样不同比例的海报都能对齐分栏。
func composeLibraryCover(images [][]byte, width, height int) ([]byte, error) {
	if len(images) == 0 {
		return nil, errors.New("no images to compose")
	}
	if width <= 0 || height <= 0 {
		return nil, errors.New("invalid cover size")
	}
	decoded := make([]image.Image, 0, len(images))
	for _, data := range images {
		img, ok := decodeCoverImage(data)
		if ok {
			decoded = append(decoded, img)
		}
		if len(decoded) >= libraryCoverMaxPanels {
			break
		}
	}
	if len(decoded) == 0 {
		return nil, errors.New("no decodable cover images")
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	cells := make([]image.Rectangle, 0, len(decoded))
	if len(decoded) == 1 {
		cells = append(cells, canvas.Bounds())
	} else {
		for i := range decoded {
			x0 := width * i / len(decoded)
			x1 := width * (i + 1) / len(decoded)
			cells = append(cells, image.Rect(x0, 0, x1, height))
		}
	}
	for i, cell := range cells {
		drawCoverCell(canvas, cell, decoded[i])
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: libraryCoverQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeCoverImage 解码一张海报，并对异常大的图做像素上限保护：一次拼图
// 会同时解码 4 张，2 核 2G 的机器上不能任由超大原图把内存吃光。
func decodeCoverImage(data []byte) (image.Image, bool) {
	if len(data) == 0 {
		return nil, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, false
	}
	if cfg.Width*cfg.Height > imageResizeMaxSourcePixels {
		return nil, false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	return img, true
}

// drawCoverCell 把一张图按中心裁切铺满目标格子。
func drawCoverCell(dst *image.RGBA, cell image.Rectangle, src image.Image) {
	if src == nil || cell.Dx() <= 0 || cell.Dy() <= 0 {
		return
	}
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return
	}
	targetAspect := float64(cell.Dx()) / float64(cell.Dy())
	srcAspect := float64(srcW) / float64(srcH)
	crop := bounds
	if srcAspect > targetAspect {
		cropW := int(float64(srcH)*targetAspect + 0.5)
		if cropW < 1 {
			cropW = 1
		}
		x0 := bounds.Min.X + (srcW-cropW)/2
		crop = image.Rect(x0, bounds.Min.Y, x0+cropW, bounds.Max.Y)
	} else if srcAspect < targetAspect {
		cropH := int(float64(srcW)/targetAspect + 0.5)
		if cropH < 1 {
			cropH = 1
		}
		y0 := bounds.Min.Y + (srcH-cropH)/2
		crop = image.Rect(bounds.Min.X, y0, bounds.Max.X, y0+cropH)
	}
	scale := float64(cell.Dx()) / float64(crop.Dx())
	var scaler draw.Scaler = draw.CatmullRom
	if scale < imageResizeCheapScaleRatio {
		scaler = draw.ApproxBiLinear
	}
	scaler.Scale(dst, cell, src, crop, draw.Src, nil)
}

// writeFileAtomic 先写临时文件再 rename，避免并发的读取方读到半个文件。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cover-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// LibraryCoverSourcesForLibrary 是给 handler 的便捷封装：库存在且没有自定
// 义封面时返回成员海报来源；远程挂载库由各自的上游负责封面，不在此生成。
func LibraryCoverSourcesForLibrary(ctx context.Context, repos *repository.Container, libraryID string) ([]string, *model.Library) {
	if repos == nil || repos.Library == nil {
		return nil, nil
	}
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return nil, nil
	}
	lib, err := repos.Library.FindByID(ctx, libraryID)
	if err != nil || lib == nil {
		return nil, nil
	}
	if strings.TrimSpace(lib.CoverURL) != "" {
		return nil, lib
	}
	return LibraryCoverSources(ctx, repos, libraryID, libraryCoverSourceLimit), lib
}
