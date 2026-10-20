package service

import (
	"context"
	"fmt"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

type MediaQualityItem struct {
	Issue string      `json:"issue"`
	Media model.Media `json:"media"`
}

func (s *MediaService) QualityIssues(ctx context.Context, issue string, page, pageSize int) ([]MediaQualityItem, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	q := s.repo.DB.WithContext(ctx).Model(&model.Media{})
	switch issue {
	case "missing":
		q = q.Where("availability_state = ?", "missing")
	case "unmatched":
		q = q.Where("scrape_status IN ? OR scrape_status = ''", []string{"pending", "no_match"})
	case "poster":
		q = q.Where("poster_url IS NULL OR TRIM(poster_url) = ''")
	default:
		return nil, 0, fmt.Errorf("unknown quality issue: %s", issue)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.Media
	if err := q.Order("updated_at DESC, id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	s.attachLibraryMetadata(ctx, rows)
	items := make([]MediaQualityItem, 0, len(rows))
	for _, media := range rows {
		items = append(items, MediaQualityItem{Issue: issue, Media: media})
	}
	return items, total, nil
}

type MediaSearchFilter struct {
	LibraryID string
	YearMin   int
	YearMax   int
	Genres    []string
}

// SearchMedia performs a simple LIKE search across titles.
func (s *MediaService) SearchMedia(ctx context.Context, query string, limit int) ([]model.Media, error) {
	return s.SearchMediaVisible(ctx, query, limit, MediaVisibility{IncludeNSFW: true})
}

func (s *MediaService) SearchMediaVisible(ctx context.Context, query string, limit int, visibility MediaVisibility) ([]model.Media, error) {
	return s.SearchMediaVisibleFiltered(ctx, query, limit, visibility, MediaSearchFilter{})
}

func (s *MediaService) SearchMediaVisibleFiltered(ctx context.Context, query string, limit int, visibility MediaVisibility, filter MediaSearchFilter) ([]model.Media, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > maxMediaSearchLimit {
		limit = maxMediaSearchLimit
	}
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	items, err := s.repo.Media.SearchFiltered(ctx, query, limit, repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
		LibraryID:         filter.LibraryID,
		YearMin:           filter.YearMin,
		YearMax:           filter.YearMax,
		Genres:            filter.Genres,
	})
	if err != nil {
		return nil, err
	}
	s.attachLibraryMetadata(ctx, items)
	return items, nil
}

func (s *MediaService) SearchMediaVisibleGrouped(ctx context.Context, query string, limit int, visibility MediaVisibility) ([]MediaItem, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > maxMediaSearchLimit {
		limit = maxMediaSearchLimit
	}
	items, err := s.SearchMediaVisible(ctx, query, maxMediaSearchLimit, visibility)
	if err != nil {
		return nil, err
	}
	return firstMediaItems(groupMediaVersions(items), limit), nil
}

func (s *MediaService) SearchMediaVisiblePage(ctx context.Context, query string, page, pageSize int, visibility MediaVisibility) ([]model.Media, int64, error) {
	return s.SearchMediaVisiblePageFiltered(ctx, query, page, pageSize, visibility, MediaSearchFilter{})
}

func (s *MediaService) SearchMediaVisiblePageFiltered(ctx context.Context, query string, page, pageSize int, visibility MediaVisibility, filter MediaSearchFilter) ([]model.Media, int64, error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > maxMediaSearchPageSize {
		pageSize = maxMediaSearchPageSize
	}
	if page < 1 {
		page = 1
	}
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	items, total, err := s.repo.Media.SearchFilteredPage(ctx, query, (page-1)*pageSize, pageSize, repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
		LibraryID:         filter.LibraryID,
		YearMin:           filter.YearMin,
		YearMax:           filter.YearMax,
		Genres:            filter.Genres,
	})
	if err != nil {
		return nil, 0, err
	}
	s.attachLibraryMetadata(ctx, items)
	return items, total, nil
}

func (s *MediaService) SearchMediaVisiblePageGrouped(ctx context.Context, query string, page, pageSize int, visibility MediaVisibility) ([]MediaItem, int64, error) {
	page, pageSize = normalizeGroupedMediaPage(page, pageSize)
	items, err := s.SearchMediaVisible(ctx, query, maxMediaSearchLimit, visibility)
	if err != nil {
		return nil, 0, err
	}
	grouped := groupMediaVersions(items)
	return paginateMediaItems(grouped, page, pageSize), int64(len(grouped)), nil
}
