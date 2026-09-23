package service

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
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
// seriesID 非空时只返回该剧的下一集（剧集详情页「继续播放」）；空则返回全站列表。
func (e *EmbyService) NextUp(ctx context.Context, userID, seriesID string, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = embyNextUpDefaultLimit
	}
	if limit > embyNextUpMaxLimit {
		limit = embyNextUpMaxLimit
	}
	if strings.TrimSpace(userID) == "" {
		return emptyItemsEnvelope(0), nil
	}
	seriesID = strings.TrimSpace(seriesID)
	discovery := e.discoveryService()
	if discovery == nil {
		return emptyItemsEnvelope(0), nil
	}

	if seriesID != "" {
		return e.nextUpForSeries(ctx, userID, seriesID, limit)
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

// nextUpForSeries 只解析指定剧的下一集。远程挂载剧集按本机播放历史 + 远程
// 分集列表计算，避免把其它本地剧的 NextUp 塞进详情页继续播放按钮。
func (e *EmbyService) nextUpForSeries(ctx context.Context, userID, seriesID string, limit int) (map[string]any, error) {
	if IsEmbyRemoteID(seriesID) {
		return e.nextUpForRemoteSeries(ctx, userID, seriesID, limit)
	}
	discovery := e.discoveryService()
	if discovery == nil {
		return emptyItemsEnvelope(0), nil
	}
	// 多取候选再按 SeriesId 精确过滤，避免「全站 TopN」把目标剧挤掉。
	scanLimit := embyNextUpMaxLimit
	if limit > scanLimit {
		scanLimit = limit
	}
	rows, err := discovery.NextUpCandidates(ctx, userID, scanLimit, e.mediaVisibility(ctx, userID))
	if err != nil {
		return nil, err
	}
	items, err := e.payloadsForMedia(ctx, rows, userID)
	if err != nil {
		return nil, err
	}
	filtered := make([]map[string]any, 0, 1)
	for _, item := range items {
		itemSeries, _ := item["SeriesId"].(string)
		if itemSeries == seriesID {
			filtered = append(filtered, item)
			if len(filtered) >= limit {
				break
			}
		}
	}
	return map[string]any{
		"Items":            filtered,
		"TotalRecordCount": int64(len(filtered)),
	}, nil
}

// nextUpForRemoteSeries 用 MeBox 本地播放历史在远程剧的分集里找「下一集」。
// 不透传远程账号的 NextUp，避免多用户共用挂载账号时串进度。
func (e *EmbyService) nextUpForRemoteSeries(ctx context.Context, userID, seriesID string, limit int) (map[string]any, error) {
	if e == nil || e.remote == nil || strings.TrimSpace(userID) == "" {
		return emptyItemsEnvelope(0), nil
	}
	if limit <= 0 {
		limit = 1
	}
	mountID, remoteSeriesID, ok := DecodeEmbyRemoteID(seriesID)
	if !ok {
		return emptyItemsEnvelope(0), nil
	}
	mount, acct, err := e.remote.ResolveMount(ctx, mountID)
	if err != nil || mount == nil || acct == nil {
		return emptyItemsEnvelope(0), nil
	}
	if !EmbyMountLibraryAllowed(e.mediaVisibility(ctx, userID), mount) {
		return emptyItemsEnvelope(0), nil
	}

	prefix := EmbyRemoteIDPrefix + mountID + "~"
	var hist []model.PlaybackHistory
	if err := e.repo.DB.WithContext(ctx).
		Where("user_id = ? AND position_ms > 0 AND media_id LIKE ?", userID, prefix+"%").
		Order("watched_at desc").
		Limit(nextUpHistoryScanLimit).
		Find(&hist).Error; err != nil {
		return nil, err
	}
	if len(hist) == 0 {
		return emptyItemsEnvelope(0), nil
	}

	episodes, err := e.remote.RemoteEpisodes(ctx, mount, acct, remoteSeriesID)
	if err != nil || len(episodes) == 0 {
		return emptyItemsEnvelope(0), nil
	}
	epByID := make(map[string]*model.Media, len(episodes))
	for i := range episodes {
		epByID[episodes[i].ID] = &episodes[i]
	}

	var current *model.Media
	for i := range hist {
		if m := epByID[hist[i].MediaID]; m != nil {
			current = m
			break
		}
	}
	if current == nil {
		return emptyItemsEnvelope(0), nil
	}

	completed := map[string]bool{}
	epIDs := make([]string, 0, len(episodes))
	for i := range episodes {
		epIDs = append(epIDs, episodes[i].ID)
	}
	var done []model.PlaybackHistory
	if err := e.repo.DB.WithContext(ctx).
		Where("user_id = ? AND completed = ? AND media_id IN ?", userID, true, epIDs).
		Find(&done).Error; err == nil {
		for _, h := range done {
			completed[h.MediaID] = true
		}
	}

	next, ok := pickNextEpisode(episodes, current, completed)
	if !ok {
		return emptyItemsEnvelope(0), nil
	}
	_, remoteEpID, ok := DecodeEmbyRemoteID(next.ID)
	if !ok {
		return emptyItemsEnvelope(0), nil
	}
	item, err := e.remote.RemoteItem(ctx, mount, acct, remoteEpID)
	if err != nil || item == nil {
		return emptyItemsEnvelope(0), nil
	}
	if err := e.mergeRemoteUserData(ctx, userID, item); err != nil {
		return nil, err
	}
	items := []map[string]any{item}
	if limit < len(items) {
		items = items[:limit]
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
