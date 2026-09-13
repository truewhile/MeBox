package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// ListMedia paginates media items inside a library.
func (s *MediaService) ListMedia(ctx context.Context, libraryID string, page, pageSize int) ([]model.Media, int64, error) {
	return s.ListMediaVisible(ctx, libraryID, page, pageSize, MediaVisibility{IncludeNSFW: true})
}

func (s *MediaService) ListMediaVisible(ctx context.Context, libraryID string, page, pageSize int, visibility MediaVisibility) ([]model.Media, int64, error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 2000 {
		pageSize = 2000
	}
	if page < 1 {
		page = 1
	}
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	libraryIDs, err := MergedLibraryIDsForLibrary(ctx, s.repo, libraryID)
	if err != nil {
		return nil, 0, err
	}
	filter := repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
	}
	cacheKey := s.mediaListCacheKey(libraryID, libraryIDs, page, pageSize, filter)
	var cached mediaListCacheValue
	if s.cache != nil && s.cache.GetJSON(ctx, cacheKey, &cached) {
		s.attachLibraryMetadata(ctx, cached.Items)
		return cached.Items, cached.Total, nil
	}
	items, total, err := s.repo.Media.ListByLibrariesFiltered(ctx, libraryIDs, (page-1)*pageSize, pageSize, filter)
	if err != nil {
		return nil, 0, err
	}
	s.attachLibraryMetadata(ctx, items)
	if s.cache != nil {
		s.cache.SetJSON(ctx, cacheKey, mediaListCacheValue{Items: items, Total: total}, time.Duration(s.mediaCacheTTLSeconds())*time.Second)
	}
	return items, total, nil
}

func (s *MediaService) ListMediaVisibleGrouped(ctx context.Context, libraryID string, page, pageSize int, visibility MediaVisibility) ([]MediaItem, int64, error) {
	page, pageSize = normalizeGroupedMediaPage(page, pageSize)
	grouped, err := s.GroupedMediaVisible(ctx, libraryID, visibility)
	if err != nil {
		return nil, 0, err
	}
	return paginateMediaItems(grouped, page, pageSize), int64(len(grouped)), nil
}

// GroupedMediaVisible returns the complete version-grouped media list before pagination.
// The result is cached as an immutable slice; sort/pagination callers must copy it before mutating.
func (s *MediaService) GroupedMediaVisible(ctx context.Context, libraryID string, visibility MediaVisibility) ([]MediaItem, error) {
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	libraryIDs, err := MergedLibraryIDsForLibrary(ctx, s.repo, libraryID)
	if err != nil {
		return nil, err
	}
	filter := repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
	}
	itemsCacheKey := s.groupedItemsCacheKey(libraryID, libraryIDs, filter)
	if s.cache != nil {
		if cachedObj, ok := s.cache.GetObject(itemsCacheKey); ok {
			if cached, ok := cachedObj.([]MediaItem); ok {
				return cached, nil
			}
		}
	}
	items, err := s.listMediaVisibleForGrouping(ctx, libraryID, visibility)
	if err != nil {
		return nil, err
	}
	grouped := groupMediaVersions(items)
	if s.cache != nil && len(grouped) > 0 {
		s.cache.SetObject(itemsCacheKey, grouped, s.mediaObjectTTL())
	}
	return grouped, nil
}

func (s *MediaService) listMediaVisibleForGrouping(ctx context.Context, libraryID string, visibility MediaVisibility) ([]model.Media, error) {
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	libraryIDs, err := MergedLibraryIDsForLibrary(ctx, s.repo, libraryID)
	if err != nil {
		return nil, err
	}
	filter := repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
	}
	// 版本分组的 URL 分页发生在 Go 进程内，响应里的 total 是分组后的数量，
	// 不需要数据库再为原始行做一次 COUNT(*)。全量 COUNT 在超大媒体库上
	// 会重复扫描整个 library_id 范围，而这里只关心是否存在截断风险。
	items, err := s.repo.Media.ListByLibrariesFilteredNoCount(ctx, libraryIDs, 0, maxMediaSearchLimit, filter)
	if err != nil {
		return nil, err
	}
	if len(items) >= maxMediaSearchLimit && s.log != nil {
		s.log.Warn("media version grouping may be truncated by safety limit",
			zap.String("library_id", libraryID),
			zap.Int("limit", maxMediaSearchLimit))
	}
	s.attachLibraryMetadata(ctx, items)
	return items, nil
}

// GetMedia returns a single media row.
func (s *MediaService) GetMedia(ctx context.Context, id string) (*model.Media, error) {
	media, err := s.repo.Media.FindByID(ctx, id)
	if err != nil || media == nil {
		return media, err
	}
	items := []model.Media{*media}
	s.attachLibraryMetadata(ctx, items)
	*media = items[0]
	return media, nil
}

// GetMediaItem 返回媒体详情，并附带同片多版本列表（用于详情页/播放器切换）。
func (s *MediaService) GetMediaItem(ctx context.Context, id string) (*MediaItem, error) {
	media, err := s.GetMedia(ctx, id)
	if err != nil || media == nil {
		return nil, err
	}
	versions, err := s.listVersionSiblings(ctx, media)
	if err != nil {
		return nil, err
	}
	item := &MediaItem{Media: *media}
	if len(versions) > 1 {
		item.Versions = versions
	}
	return item, nil
}

// listVersionSiblings 查找与当前条目同属一个版本组的全部媒体（含自身）。
func (s *MediaService) listVersionSiblings(ctx context.Context, media *model.Media) ([]model.Media, error) {
	if media == nil || strings.TrimSpace(media.ID) == "" {
		return nil, nil
	}
	key := mediaVersionGroupKey(*media)
	if key == "" {
		return []model.Media{*media}, nil
	}
	libraryIDs, err := MergedLibraryIDsForLibrary(ctx, s.repo, media.LibraryID)
	if err != nil {
		return nil, err
	}
	if len(libraryIDs) == 0 {
		libraryIDs = []string{media.LibraryID}
	}
	filter := repository.MediaQueryFilter{IncludeNSFW: true}
	candidates, err := s.repo.Media.ListByLibrariesFilteredNoCount(ctx, libraryIDs, 0, 5000, filter)
	if err != nil {
		return nil, err
	}
	s.attachLibraryMetadata(ctx, candidates)
	matched := make([]model.Media, 0, 4)
	for _, row := range candidates {
		if mediaVersionGroupKey(row) == key {
			matched = append(matched, row)
		}
	}
	if len(matched) == 0 {
		return []model.Media{*media}, nil
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].ID == media.ID {
			return true
		}
		if matched[j].ID == media.ID {
			return false
		}
		return betterMediaVersion(matched[i], matched[j])
	})
	return matched, nil
}
