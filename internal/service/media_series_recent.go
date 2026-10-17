package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// recentSeriesCardsCacheValue 缓存「最近添加」剧集卡片。Cards 视为不可变，
// 调用方只读不改（与 seriesCardsCacheValue 相同的约定）。
type recentSeriesCardsCacheValue struct {
	Cards []SeriesCard
}

// ListRecentSeriesCards 返回最近入库的剧集卡片（首页「最近添加」）。
//
// 性能约定：首页每次刷新都会打这条路径，因此不能再用 SearchMediaVisible：
// 它会先对整表跑一次 COUNT(*)，再 SELECT 全部列（含 overview/genres 等大文本）。
// 7k 行规模下，驱动层逐列解码就占掉 60% 的耗时，实测 ≈1.6s。
//
// 现在的做法：
//  1. 只取归组必需的列（repository.ListMediaSeriesIndex），不做 COUNT；
//  2. 在内存里完成分组/计数/排序，截断到 limit；
//  3. 只对最终这几十张卡片的代表行补一次完整行（客户端要 overview、genres）；
//  4. 结果按可见性缓存，媒体入库/刮削会按 "media:" 前缀统一失效。
//
// 语义与旧实现一致：key 与 Count 仍然基于整库所有可见行计算，不能只看
// 前 N 行，否则剧集 key 会和 /libraries/{id}/series/episodes 对不上。
func (s *MediaService) ListRecentSeriesCards(ctx context.Context, limit int, visibility MediaVisibility) ([]SeriesCard, error) {
	if limit <= 0 {
		limit = 24
	} else if limit > 100 {
		limit = 100
	}
	cacheKey := s.recentSeriesCardsCacheKey(limit, visibility)
	if cards, ok := s.cachedRecentSeriesCards(cacheKey); ok {
		return cards, nil
	}
	// singleflight：多个客户端同时刷新首页时只扫一遍库。
	value, err, _ := s.recentSeriesFlight.Do(cacheKey, func() (any, error) {
		if cards, ok := s.cachedRecentSeriesCards(cacheKey); ok {
			return &recentSeriesCardsCacheValue{Cards: cards}, nil
		}
		// 首个调用者取消不应让其它等待者一起失败。
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		cards, err := s.loadRecentSeriesCards(loadCtx, limit, visibility)
		if err != nil {
			return nil, err
		}
		cached := &recentSeriesCardsCacheValue{Cards: cards}
		if s.cache != nil {
			s.cache.SetObject(cacheKey, cached, s.derivedReadCacheTTL())
		}
		return cached, nil
	})
	if err != nil {
		return nil, err
	}
	if cached, ok := value.(*recentSeriesCardsCacheValue); ok {
		return cached.Cards, nil
	}
	return []SeriesCard{}, nil
}

func (s *MediaService) cachedRecentSeriesCards(cacheKey string) ([]SeriesCard, bool) {
	if s == nil || s.cache == nil {
		return nil, false
	}
	obj, ok := s.cache.GetObject(cacheKey)
	if !ok {
		return nil, false
	}
	cached, ok := obj.(*recentSeriesCardsCacheValue)
	if !ok || cached == nil {
		return nil, false
	}
	return cached.Cards, true
}

func (s *MediaService) loadRecentSeriesCards(ctx context.Context, limit int, visibility MediaVisibility) ([]SeriesCard, error) {
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	rows, err := s.repo.Media.ListMediaSeriesIndex(ctx, maxMediaSearchLimit, repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []SeriesCard{}, nil
	}
	// 归组 key 依赖库元数据（display library 路径参与指纹计算）。
	s.attachLibraryMetadata(ctx, rows)
	cards := groupMediaSeriesCards(rows)
	if len(cards) == 0 {
		return []SeriesCard{}, nil
	}
	if len(cards) > limit {
		cards = cards[:limit]
	}
	s.hydrateSeriesCardRows(ctx, cards)
	return cards, nil
}

// hydrateSeriesCardRows 把投影行替换成完整媒体行。
//
// 归组只需要少量列，但客户端展示卡片要用整行（简介、类型、编码等）。这里
// 只针对最终返回的卡片取两次 id 集合，一次 IN 查询补齐，避免把整库大文本列
// 都读出来。
func (s *MediaService) hydrateSeriesCardRows(ctx context.Context, cards []SeriesCard) {
	if s == nil || s.repo == nil || len(cards) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(cards)*2)
	ids := make([]string, 0, len(cards)*2)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for i := range cards {
		add(cards[i].Rep.ID)
		add(cards[i].LinkMedia.ID)
	}
	if len(ids) == 0 {
		return
	}
	rows, err := s.repo.Media.ListMediaByIDs(ctx, ids)
	if err != nil || len(rows) == 0 {
		// 补齐失败时保留投影行：卡片列表仍可用，只是缺少大文本字段。
		return
	}
	s.attachLibraryMetadata(ctx, rows)
	byID := make(map[string]model.Media, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	for i := range cards {
		if row, ok := byID[cards[i].Rep.ID]; ok {
			cards[i].Rep = row
		}
		if row, ok := byID[cards[i].LinkMedia.ID]; ok {
			cards[i].LinkMedia = row
		}
	}
}

func (s *MediaService) recentSeriesCardsCacheKey(limit int, visibility MediaVisibility) string {
	allowed := append([]string(nil), visibility.AllowedLibraryIDs...)
	hidden := append([]string(nil), visibility.HiddenLibraryIDs...)
	sort.Strings(allowed)
	sort.Strings(hidden)
	return "media:obj:recent-series:" + hashObjectCacheKey([]string{
		fmt.Sprintf("%d:%t", limit, visibility.IncludeNSFW),
		strings.Join(allowed, ","),
		strings.Join(hidden, ","),
	})
}
