// Package service — generic on-disk cleanup helper used by the
// scheduler. Public so handlers can call it for "purge transcode cache
// now" buttons.
package service

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// walkAndPrune recursively deletes every file under root whose mtime is
// older than cutoff. Empty directories left behind are removed too.
// Best-effort: per-file errors are ignored so a single permission denial
// doesn't abort the cleanup.
func walkAndPrune(root string, cutoff time.Time) error {
	if root == "" {
		return nil
	}
	if _, err := os.Stat(root); err != nil {
		return nil // nothing to clean
	}
	dirs := []string{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if path != root {
				dirs = append(dirs, path)
			}
			return nil
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path) // #nosec G122 -- cache pruning is best-effort under the configured cache root.
		}
		return nil
	})
	// Remove emptied directories from deepest to shallowest.
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
	return nil
}

// PruneImageCacheResult holds stats from an image cache prune operation.
type PruneImageCacheResult struct {
	TotalFilesBefore int
	TotalBytesBefore int64
	DeletedFiles     int
	FreedBytes       int64
	RemainingBytes   int64
}

// imageCacheFileEntry 是池内一个可被淘汰的缓存文件。
type imageCacheFileEntry struct {
	path    string
	pool    int
	size    int64
	modTime time.Time
}

// ImageCacheScope 是池内的一处文件范围。Recursive=false 时只处理该目录下的
// 直属文件（历史版本的图片缓存是平铺在 images/ 下的，升级后仍要能清理掉）。
type ImageCacheScope struct {
	Root      string
	Recursive bool
	// DropUnknownResizeVariants 只用于派生缩略图目录：直接删除文件名不符合当前
	// 命名（<源图键>.<宽>x<高>q<质量>.img）的历史缩放缓存。它们在新的查找路径下
	// 永远不会被命中，留着只会占用总量配额、把有用缓存挤出去。
	DropUnknownResizeVariants bool
}

// ImageCachePool 描述一个图片缓存池：范围 + 自身配额 + 保留时长。
//
// 分池的意义在于两类缓存的“可再生成本”完全不同：原图只是生成缩略图的原料，
// 丢了可以重新回源；派生缩略图是客户端热路径真正读取的成品，重新生成代价高。
// 因此原图配小配额、短保留，总量超限时也优先淘汰原图。
type ImageCachePool struct {
	Name     string
	Scopes   []ImageCacheScope
	MaxBytes int64         // 0 = 不单独限制
	MaxAge   time.Duration // 0 = 不按时间淘汰
}

// imageFailMarkerMaxAge 是失败标记文件的保留时长：标记只用于观测/重试，
// 过期即视为噪音清掉（不会因为标记残留而阻止重新抓取）。
const imageFailMarkerMaxAge = 24 * time.Hour

// tempImageCacheFile 报告文件名是否是下载/缩放过程中的临时文件。它们正在被
// 并发写入，清理时必须跳过。
func tempImageCacheFile(name string) bool {
	if strings.HasSuffix(name, ".tmp") {
		return true
	}
	return strings.HasPrefix(name, "img-") && strings.Contains(name, ".tmp")
}

// sha256HexLength 是十六进制 sha256 摘要的长度。
const sha256HexLength = 64

// isCurrentResizeVariantName 报告文件名是否符合当前的缩放缓存命名
// （<源图键>.<宽>x<高>q<质量>.img）。
func isCurrentResizeVariantName(name string) bool {
	if !strings.HasSuffix(name, ".img") {
		return false
	}
	spec := strings.TrimSuffix(name, ".img")
	key, _, found := strings.Cut(spec, ".")
	if !found || len(key) != sha256HexLength {
		return false
	}
	if _, err := hex.DecodeString(key); err != nil {
		return false
	}
	_, _, ok := parseResizeVariantSuffix(name, key)
	return ok
}

// collectImageCachePoolFiles 收集池内可淘汰的文件（跳过并发写入中的临时文件）。
// 失败标记单独返回，它们不计入容量，只按年龄清理。
func collectImageCachePoolFiles(pool ImageCachePool, poolIndex int, entries *[]imageCacheFileEntry, failMarkers *[]imageCacheFileEntry) {
	visit := func(path string, info os.FileInfo, scope ImageCacheScope) {
		if info.IsDir() {
			return
		}
		name := info.Name()
		if tempImageCacheFile(name) {
			return
		}
		if scope.DropUnknownResizeVariants && strings.HasSuffix(name, ".img") && !isCurrentResizeVariantName(name) {
			_ = os.Remove(path) // 旧版命名，永远不会命中，直接清掉
			return
		}
		entry := imageCacheFileEntry{path: path, pool: poolIndex, size: info.Size(), modTime: info.ModTime()}
		if strings.HasSuffix(name, ".fail") {
			*failMarkers = append(*failMarkers, entry)
			return
		}
		*entries = append(*entries, entry)
	}
	for _, scope := range pool.Scopes {
		if strings.TrimSpace(scope.Root) == "" {
			continue
		}
		if !scope.Recursive {
			dir, err := os.Open(scope.Root) // #nosec G304 -- cache root from config.
			if err != nil {
				continue
			}
			names, err := dir.Readdir(-1)
			_ = dir.Close()
			if err != nil {
				continue
			}
			for _, info := range names {
				visit(filepath.Join(scope.Root, info.Name()), info, scope)
			}
			continue
		}
		_ = filepath.Walk(scope.Root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil // best-effort: one unreadable dir must not abort cleanup
			}
			visit(path, info, scope)
			return nil
		})
	}
}

// removeImageCacheEntry 删除一个缓存文件，并顺带清掉它的失败标记。
func removeImageCacheEntry(entry imageCacheFileEntry, result *PruneImageCacheResult) {
	if err := os.Remove(entry.path); err != nil {
		return
	}
	result.DeletedFiles++
	result.FreedBytes += entry.size
	result.RemainingBytes -= entry.size
	_ = os.Remove(entry.path + ".fail")
}

// PruneImageCachePools 按池清理图片缓存：
//  1. 删除超过池 MaxAge 的文件（原图按保留时长淘汰）；
//  2. 池自身超过 MaxBytes 时按 mtime 淘汰到 80%（留水位，避免连续写入即触发）；
//  3. 全部池合计超过 totalBytes 时，仍按“先原图、后派生”的顺序淘汰最旧文件，
//     使总量上限始终是硬保证，同时让客户端热路径的缩略图活得更久。
//
// 传入的池顺序即总量超限时的淘汰优先级（排在前面的先被淘汰）。
func PruneImageCachePools(pools []ImageCachePool, totalBytes int64) (PruneImageCacheResult, error) {
	var result PruneImageCacheResult
	if len(pools) == 0 {
		return result, nil
	}

	var entries []imageCacheFileEntry
	var failMarkers []imageCacheFileEntry
	for i, pool := range pools {
		collectImageCachePoolFiles(pool, i, &entries, &failMarkers)
	}

	result.TotalFilesBefore = len(entries)
	for _, entry := range entries {
		result.TotalBytesBefore += entry.size
	}
	result.RemainingBytes = result.TotalBytesBefore

	// 过期的失败标记直接清掉：它们不计容量，只用于观测与重试。
	cutoffMarkers := time.Now().Add(-imageFailMarkerMaxAge)
	for _, marker := range failMarkers {
		if marker.modTime.Before(cutoffMarkers) {
			_ = os.Remove(marker.path)
		}
	}

	now := time.Now()
	deleted := make(map[string]bool, len(entries))
	deleteEntry := func(entry imageCacheFileEntry) {
		if deleted[entry.path] {
			return
		}
		deleted[entry.path] = true
		removeImageCacheEntry(entry, &result)
	}

	// 1. 按池保留时长淘汰（原图池）。
	for i := range pools {
		if pools[i].MaxAge <= 0 {
			continue
		}
		cutoff := now.Add(-pools[i].MaxAge)
		for _, entry := range entries {
			if entry.pool == i && entry.modTime.Before(cutoff) {
				deleteEntry(entry)
			}
		}
	}

	// 2. 池自身配额。
	for i := range pools {
		maxBytes := pools[i].MaxBytes
		if maxBytes <= 0 {
			continue
		}
		var poolBytes int64
		for _, entry := range entries {
			if entry.pool == i && !deleted[entry.path] {
				poolBytes += entry.size
			}
		}
		if poolBytes <= maxBytes {
			continue
		}
		target := maxBytes * 80 / 100
		for _, entry := range oldestFirst(entries, i, deleted) {
			if poolBytes <= target {
				break
			}
			deleteEntry(entry)
			poolBytes -= entry.size
		}
	}

	// 3. 总量硬上限：跨池按淘汰优先级（池顺序）再按 mtime 淘汰。
	if totalBytes > 0 && result.RemainingBytes > totalBytes {
		target := totalBytes * 80 / 100
		for _, entry := range evictionOrder(entries, deleted) {
			if result.RemainingBytes <= target {
				break
			}
			deleteEntry(entry)
		}
	}

	removeEmptyImageCacheDirs(pools)
	return result, nil
}

// oldestFirst 返回指定池内未被删除的文件，按修改时间从旧到新。
func oldestFirst(entries []imageCacheFileEntry, pool int, deleted map[string]bool) []imageCacheFileEntry {
	out := make([]imageCacheFileEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.pool == pool && !deleted[entry.path] {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].modTime.Before(out[j].modTime) })
	return out
}

// evictionOrder 返回总量超限时的淘汰顺序：先按池优先级（原图池在前），
// 池内再按修改时间从旧到新。
func evictionOrder(entries []imageCacheFileEntry, deleted map[string]bool) []imageCacheFileEntry {
	out := make([]imageCacheFileEntry, 0, len(entries))
	for _, entry := range entries {
		if !deleted[entry.path] {
			out = append(out, entry)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].pool != out[j].pool {
			return out[i].pool < out[j].pool
		}
		return out[i].modTime.Before(out[j].modTime)
	})
	return out
}

// removeEmptyImageCacheDirs 清理淘汰后留下的空子目录（不含池根目录本身）。
func removeEmptyImageCacheDirs(pools []ImageCachePool) {
	seen := map[string]bool{}
	for _, pool := range pools {
		for _, scope := range pool.Scopes {
			if !scope.Recursive || seen[scope.Root] {
				continue
			}
			seen[scope.Root] = true
			dirs := []string{}
			_ = filepath.Walk(scope.Root, func(path string, info os.FileInfo, err error) error {
				if err != nil || info == nil {
					return nil
				}
				if info.IsDir() && path != scope.Root {
					dirs = append(dirs, path)
				}
				return nil
			})
			for i := len(dirs) - 1; i >= 0; i-- {
				_ = os.Remove(dirs[i])
			}
		}
	}
}

// PruneImageCache 是单池版本的兼容入口：把整个图片目录当作一个池，超限时按
// mtime 淘汰到 80%。保留它是为了「立刻清理图片缓存」这类只关心总量的调用方。
func PruneImageCache(imagesDir string, maxSizeBytes int64) (PruneImageCacheResult, error) {
	var result PruneImageCacheResult
	if imagesDir == "" || maxSizeBytes <= 0 {
		return result, nil
	}
	if _, err := os.Stat(imagesDir); err != nil {
		return result, nil
	}
	pool := ImageCachePool{
		Name:     "images",
		Scopes:   []ImageCacheScope{{Root: imagesDir, Recursive: true}},
		MaxBytes: maxSizeBytes,
	}
	return PruneImageCachePools([]ImageCachePool{pool}, maxSizeBytes)
}

// ImageCachePools 按配置组装图片缓存的两个清理池。
//
// 第一个池是原图：历史版本平铺在 images/ 下的旧缓存也归入此池，升级后会被
// 逐步淘汰；第二个池是派生成品（本地缩放结果 + 挂载 Emby 按尺寸返回的成品）。
// 池顺序即总量超限时的淘汰优先级。
func ImageCachePools(imagesDir string, originalsMaxBytes int64, originalsMaxAge time.Duration) []ImageCachePool {
	originals := ImageCachePool{
		Name:     "originals",
		MaxBytes: originalsMaxBytes,
		MaxAge:   originalsMaxAge,
		Scopes: []ImageCacheScope{
			{Root: imagesDir, Recursive: false}, // 旧版平铺布局
			{Root: filepath.Join(imagesDir, imageOriginalCacheSubdir), Recursive: true},
		},
	}
	derived := ImageCachePool{
		Name: "derived",
		Scopes: []ImageCacheScope{
			{Root: filepath.Join(imagesDir, imageResizeCacheSubdir), Recursive: true, DropUnknownResizeVariants: true},
			{Root: filepath.Join(imagesDir, imageRenditionCacheSubdir), Recursive: true},
		},
	}
	return []ImageCachePool{originals, derived}
}
