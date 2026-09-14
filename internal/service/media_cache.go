package service

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

type mediaListCacheValue struct {
	Items []model.Media `json:"items"`
	Total int64         `json:"total"`
}

func (s *MediaService) mediaListCacheKey(libraryID string, libraryIDs []string, page, pageSize int, filter repository.MediaQueryFilter) string {
	allowed := append([]string(nil), filter.AllowedLibraryIDs...)
	hidden := append([]string(nil), filter.HiddenLibraryIDs...)
	libs := append([]string(nil), libraryIDs...)
	sort.Strings(allowed)
	sort.Strings(hidden)
	sort.Strings(libs)
	sum := sha1.Sum([]byte(strings.Join([]string{
		libraryID,
		strings.Join(libs, ","),
		fmt.Sprintf("%d:%d:%t", page, pageSize, filter.IncludeNSFW),
		strings.Join(allowed, ","),
		strings.Join(hidden, ","),
		filter.SeriesID,
	}, "|")))
	return "media:list:" + hex.EncodeToString(sum[:])
}

func (s *MediaService) libraryPreviewCacheKey(libraries []model.Library, cardLimit int, filter repository.MediaQueryFilter, includeCounts bool) string {
	libIDs := make([]string, len(libraries))
	for i, lib := range libraries {
		libIDs[i] = lib.ID
	}
	sort.Strings(libIDs)
	allowed := append([]string(nil), filter.AllowedLibraryIDs...)
	hidden := append([]string(nil), filter.HiddenLibraryIDs...)
	sort.Strings(allowed)
	sort.Strings(hidden)
	sum := sha1.Sum([]byte(strings.Join([]string{
		"preview",
		strings.Join(libIDs, ","),
		fmt.Sprintf("%d:%t:%t", cardLimit, filter.IncludeNSFW, includeCounts),
		strings.Join(allowed, ","),
		strings.Join(hidden, ","),
		filter.SeriesID,
	}, "|")))
	return "media:preview:" + hex.EncodeToString(sum[:])
}

func (s *MediaService) seriesCardsCacheKey(libraryID string, visibility MediaVisibility) string {
	allowed := append([]string(nil), visibility.AllowedLibraryIDs...)
	hidden := append([]string(nil), visibility.HiddenLibraryIDs...)
	sort.Strings(allowed)
	sort.Strings(hidden)
	sum := sha1.Sum([]byte(strings.Join([]string{
		libraryID,
		fmt.Sprintf("%t", visibility.IncludeNSFW),
		strings.Join(allowed, ","),
		strings.Join(hidden, ","),
	}, "|")))
	return "media:series-cards:" + hex.EncodeToString(sum[:])
}

func (s *MediaService) libraryCountCacheKey(libraryIDs []string, filter repository.MediaQueryFilter) string {
	ids := append([]string(nil), libraryIDs...)
	allowed := append([]string(nil), filter.AllowedLibraryIDs...)
	hidden := append([]string(nil), filter.HiddenLibraryIDs...)
	sort.Strings(ids)
	sort.Strings(allowed)
	sort.Strings(hidden)
	sum := sha1.Sum([]byte(strings.Join([]string{
		"counts",
		strings.Join(ids, ","),
		fmt.Sprintf("%t", filter.IncludeNSFW),
		strings.Join(allowed, ","),
		strings.Join(hidden, ","),
		filter.SeriesID,
	}, "|")))
	return "media:lib-counts:" + hex.EncodeToString(sum[:])
}

func (s *MediaService) mediaCacheTTLSeconds() int {
	if s == nil || s.cfg == nil || s.cfg.Cache.MediaTTLSeconds < 1 {
		return 90
	}
	return s.cfg.Cache.MediaTTLSeconds
}

// mediaObjectTTL 对象缓存与字节缓存同 TTL，失效同走 invalidateMediaCache
// 的 DeletePrefix("media:")（对象存储一并清除）。
func (s *MediaService) mediaObjectTTL() time.Duration {
	return time.Duration(s.mediaCacheTTLSeconds()) * time.Second
}

// derivedReadCacheTTL 给首页预览、剧集分组、库计数这类派生读缓存一个更长的
// 下限。扫描/刮削/删改都会走 invalidateMediaCache 清掉 media: 前缀，所以
// 拉长 TTL 不会在写入后继续吐旧卡片。配置里的 TTL 更长时仍以配置为准。
func (s *MediaService) derivedReadCacheTTL() time.Duration {
	ttl := s.mediaObjectTTL()
	const floor = 10 * time.Minute
	if ttl < floor {
		return floor
	}
	return ttl
}

func hashObjectCacheKey(parts []string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// groupedItemsCacheKey caches the fully grouped version list. Version grouping
// requires scanning every row in the library, so recomputing it for each page
// request makes pagination O(pages × library size) instead of O(library size).
// Only the grouped result is cached; caching the ungrouped rows as well would
// duplicate large libraries in the process memory for no additional hit path.
func (s *MediaService) groupedItemsCacheKey(libraryID string, libraryIDs []string, filter repository.MediaQueryFilter) string {
	libs := append([]string(nil), libraryIDs...)
	sort.Strings(libs)
	allowed := append([]string(nil), filter.AllowedLibraryIDs...)
	hidden := append([]string(nil), filter.HiddenLibraryIDs...)
	sort.Strings(allowed)
	sort.Strings(hidden)
	return "media:obj:grouped-items:" + hashObjectCacheKey([]string{
		libraryID,
		strings.Join(libs, ","),
		fmt.Sprintf("%t", filter.IncludeNSFW),
		strings.Join(allowed, ","),
		strings.Join(hidden, ","),
		filter.SeriesID,
	})
}

// libraryRowsCacheKey 整库可见行 + 预计算剧集索引的对象缓存（剧集卡片/剧集
// 列表共用一次 SQL 加载与一次 key 解析）。
func (s *MediaService) libraryRowsCacheKey(libraryID string, visibility MediaVisibility) string {
	allowed := append([]string(nil), visibility.AllowedLibraryIDs...)
	hidden := append([]string(nil), visibility.HiddenLibraryIDs...)
	sort.Strings(allowed)
	sort.Strings(hidden)
	return "media:obj:library-rows:" + hashObjectCacheKey([]string{
		libraryID,
		fmt.Sprintf("%t", visibility.IncludeNSFW),
		strings.Join(allowed, ","),
		strings.Join(hidden, ","),
	})
}

// libraryCardsObjectKey 系列卡片结果的对象缓存（与 seriesCardsCacheKey 同维度，
// 换独立前缀避免与字节缓存 key 冲突）。
func (s *MediaService) libraryCardsObjectKey(libraryID string, visibility MediaVisibility) string {
	return s.seriesCardsCacheKey(libraryID, visibility) + ":obj"
}

func (s *MediaService) invalidateMediaCache(ctx context.Context) {
	if s != nil && s.cache != nil {
		s.cache.DeletePrefix(ctx, "media:")
		s.cache.DeletePrefix(ctx, "stats:")
	}
}
