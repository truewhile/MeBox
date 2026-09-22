package service

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"time"
)

// Emby 发现类接口：NextUp / Similar / Genres。
//
// 候选集的选取交给 MediaDiscoveryService（纯查询、无 DTO 概念），本文件只做
// 「候选 → Emby DTO」的映射，复用 itemPayload 以保证与 /Items 的形状一致。

const (
	embyNextUpDefaultLimit  = 20
	embySimilarDefaultLimit = 12
	embyNextUpMaxLimit      = 100
	embySimilarMaxLimit     = 50
)

// NextUp 返回「每部在看的剧的下一集」，即 Emby 客户端首页「接下来播放」的数据源。
func (e *EmbyService) NextUp(ctx context.Context, userID string, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = embyNextUpDefaultLimit
	}
	if limit > embyNextUpMaxLimit {
		limit = embyNextUpMaxLimit
	}
	if strings.TrimSpace(userID) == "" {
		return emptyItemsEnvelope(0), nil
	}
	discovery := e.discoveryService()
	if discovery == nil {
		return emptyItemsEnvelope(0), nil
	}

	rows, err := discovery.NextUpCandidates(ctx, userID, limit, e.mediaVisibility(ctx, userID))
	if err != nil {
		return nil, err
	}
	// Bug 3 fix: use payloadsForMedia which attaches the request-scoped payload
	// cache (withPayloadCache + prefetchPayloadCache) to avoid N+1 DB queries.
	items, err := e.payloadsForMedia(ctx, rows, userID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"Items":            items,
		"TotalRecordCount": int64(len(items)),
	}, nil
}

// SimilarItems 返回与指定条目相似的本地媒体。
//
// 找不到条目（或该条目对当前用户不可见）时返回空列表而不是错误：客户端会在
// 详情页无条件请求它，404/500 会让客户端把条目判定为不完整。
func (e *EmbyService) SimilarItems(ctx context.Context, mediaID, userID string, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = embySimilarDefaultLimit
	}
	if limit > embySimilarMaxLimit {
		limit = embySimilarMaxLimit
	}
	discovery := e.discoveryService()
	if discovery == nil || strings.TrimSpace(mediaID) == "" {
		return emptyItemsEnvelope(0), nil
	}

	// 详情页每次打开都会请求相似推荐，而重建要走「取候选池 + 内存打分」
	// （实测冷 340ms / 热 70ms）。推荐列表短暂陈旧无害，用短 TTL 缓存，
	// 新建库或换用户都会因为键名不同而自然隔离。
	cacheKey := e.embySimilarCacheKey(mediaID, userID, limit)
	if e.cache != nil {
		var cached map[string]any
		if e.cache.GetJSON(ctx, cacheKey, &cached) && cached != nil {
			if _, ok := cached["Items"]; ok {
				return cached, nil
			}
		}
	}

	// Bug 2 fix: resolve virtual series IDs (msgo-series-*) and real series
	// table IDs to a representative episode so SimilarCandidates (which calls
	// Media.FindByID) can seed similarity from concrete media metadata.
	resolvedID := mediaID
	if strings.HasPrefix(mediaID, embyVirtualSeriesPrefix) {
		series, ok, err := e.findSeriesGroup(ctx, mediaID, userID)
		if err != nil {
			return nil, err
		}
		if !ok || len(series.Episodes) == 0 {
			return emptyItemsEnvelope(0), nil
		}
		resolvedID = series.Episodes[0].ID
	} else if e.repo != nil && e.repo.Media != nil {
		// For non-virtual IDs that are series-level (not in media table), also
		// resolve via findSeriesGroup so the seed row can be found.
		m, err := e.repo.Media.FindByID(ctx, mediaID)
		if err != nil {
			return nil, err
		}
		if m == nil {
			series, ok, err := e.findSeriesGroup(ctx, mediaID, userID)
			if err != nil {
				return nil, err
			}
			if !ok || len(series.Episodes) == 0 {
				return emptyItemsEnvelope(0), nil
			}
			resolvedID = series.Episodes[0].ID
		}
	}

	rows, err := discovery.SimilarCandidates(ctx, resolvedID, limit, e.mediaVisibility(ctx, userID))
	if err != nil {
		return nil, err
	}
	// Bug 3 fix: use payloadsForMedia which attaches the request-scoped payload
	// cache (withPayloadCache + prefetchPayloadCache) to avoid N+1 DB queries.
	items, err := e.payloadsForMedia(ctx, rows, userID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"Items":            items,
		"TotalRecordCount": int64(len(items)),
	}
	if e.cache != nil {
		e.cache.SetJSON(ctx, cacheKey, out, embySimilarCacheTTL)
	}
	return out, nil
}

// embySimilarCacheTTL 是「相似推荐」结果的缓存时长。列表只是推荐，短暂陈旧
// 无害；TTL 取短一些，让新入库的内容尽快出现。
const embySimilarCacheTTL = 2 * time.Minute

// Genres 返回类型清单。parentID 非空时（客户端按媒体库浏览类型）只统计该库。
func (e *EmbyService) Genres(ctx context.Context, userID, parentID string) (map[string]any, error) {
	discovery := e.discoveryService()
	if discovery == nil {
		return emptyItemsEnvelope(0), nil
	}

	libraryID := ""
	if trimmed := strings.TrimSpace(parentID); trimmed != "" {
		// 只有本地的真实库 ID 才能用于收窄；虚拟视图 ID（Emby 客户端自己的
		// 视图标识）收窄后会得到空结果，因此识别不出来时按全库统计。
		if e.libraryExists(ctx, trimmed) {
			libraryID = trimmed
		}
	}

	genres, err := discovery.AggregateGenres(ctx, e.mediaVisibility(ctx, userID), libraryID)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(genres))
	for _, genre := range genres {
		items = append(items, map[string]any{
			"Id":                embyGenreID(genre.Name),
			"Name":              genre.Name,
			"ItemCount":         genre.Count,
			"Type":              "Genre",
			"ServerId":          embyServerID,
			"IsFolder":          false,
			"CanDelete":         false,
			"CanDownload":       false,
			"ImageTags":         map[string]any{},
			"BackdropImageTags": []any{},
		})
	}
	return map[string]any{
		"Items":            items,
		"TotalRecordCount": int64(len(items)),
	}, nil
}

// libraryExists 判断 ID 是否对应本地媒体库。
func (e *EmbyService) libraryExists(ctx context.Context, id string) bool {
	if e == nil || e.repo == nil || e.repo.Library == nil {
		return false
	}
	lib, err := e.repo.Library.FindByID(ctx, id)
	if err != nil {
		return false
	}
	return lib != nil
}

// embyGenreID 为类型生成稳定的虚拟 ID。
//
// 客户端会把 Id 当作条目去请求图片/详情，直接用类型名会带上空格与非 ASCII
// 字符，因此用固定前缀 + 名称哈希；同一个名称永远得到同一个 ID。
func embyGenreID(name string) string {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(name))))
	return "msgo-genre-" + hex.EncodeToString(sum[:])[:16]
}
