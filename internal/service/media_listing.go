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
	items, err := s.listMediaVisibleForGrouping(ctx, libraryID, visibility)
	if err != nil {
		return nil, 0, err
	}
	grouped := groupMediaVersions(items)
	return paginateMediaItems(grouped, page, pageSize), int64(len(grouped)), nil
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
	cacheKey := s.mediaGroupedRowsCacheKey(libraryID, libraryIDs, filter)
	if s.cache != nil {
		if cachedObj, ok := s.cache.GetObject(cacheKey); ok {
			if cached, ok := cachedObj.([]model.Media); ok {
				// 对象缓存中的切片视为不可变；attachLibraryMetadata 会在填充时
				// 执行过，命中路径直接返回副本即可（调用方只读）。
				return cached, nil
			}
		}
	}
	items, total, err := s.repo.Media.ListByLibrariesFiltered(ctx, libraryIDs, 0, maxMediaSearchLimit, filter)
	if err != nil {
		return nil, err
	}
	if total > int64(len(items)) && s.log != nil {
		s.log.Warn("media version grouping truncated by safety limit",
			zap.String("library_id", libraryID),
			zap.Int64("total", total),
			zap.Int("limit", maxMediaSearchLimit))
	}
	s.attachLibraryMetadata(ctx, items)
	if s.cache != nil {
		s.cache.SetObject(cacheKey, items, s.mediaObjectTTL())
	}
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
