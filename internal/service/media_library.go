package service

import (
	"context"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"gorm.io/gorm"
)

type LibraryPreviewItem struct {
	model.Library
	Total int64        `json:"total"`
	Cards []SeriesCard `json:"cards"`
}

type libraryPreviewCacheValue struct {
	Items []LibraryPreviewItem `json:"items"`
}

// ListLibraries returns every library configured on the server.
func (s *MediaService) ListLibraries(ctx context.Context) ([]model.Library, error) {
	return s.repo.Library.List(ctx)
}

// CountLibrariesCached returns per-library media totals. The homepage metadata
// request asks for every library, and the underlying COUNT is repeated on each
// refresh. Writes already drop the media: prefix, so a longer TTL is safe.
func (s *MediaService) CountLibrariesCached(ctx context.Context, libraryIDs []string, filter repository.MediaQueryFilter) (map[string]int64, error) {
	if len(libraryIDs) == 0 {
		return map[string]int64{}, nil
	}
	cacheKey := s.libraryCountCacheKey(libraryIDs, filter)
	var cached map[string]int64
	if s.cache != nil && s.cache.GetJSON(ctx, cacheKey, &cached) && cached != nil {
		return cached, nil
	}
	counts, err := s.repo.Media.CountByLibraries(ctx, libraryIDs, filter)
	if err != nil {
		return nil, err
	}
	if counts == nil {
		counts = map[string]int64{}
	}
	if s.cache != nil {
		s.cache.SetJSON(ctx, cacheKey, counts, s.derivedReadCacheTTL())
	}
	return counts, nil
}

// ListLibrariesWithPreview returns libraries populated with item counts and latest preview cards.
func (s *MediaService) ListLibrariesWithPreview(ctx context.Context, libraries []model.Library, visibility MediaVisibility, cardLimit int) ([]LibraryPreviewItem, error) {
	return s.listLibrariesWithPreview(ctx, libraries, visibility, cardLimit, true)
}

// ListLibraryPreviews returns only the latest preview cards. The metadata
// endpoint already returns totals, so preview batches used by the home and
// library pages can skip an otherwise repeated COUNT(*) over every library.
func (s *MediaService) ListLibraryPreviews(ctx context.Context, libraries []model.Library, visibility MediaVisibility, cardLimit int) ([]LibraryPreviewItem, error) {
	return s.listLibrariesWithPreview(ctx, libraries, visibility, cardLimit, false)
}

func (s *MediaService) listLibrariesWithPreview(ctx context.Context, libraries []model.Library, visibility MediaVisibility, cardLimit int, includeCounts bool) ([]LibraryPreviewItem, error) {
	if cardLimit <= 0 {
		cardLimit = 10
	}
	out := make([]LibraryPreviewItem, len(libraries))
	if len(libraries) == 0 {
		return out, nil
	}

	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	filter := repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
	}
	cacheKey := s.libraryPreviewCacheKey(libraries, cardLimit, filter, includeCounts)
	var cached libraryPreviewCacheValue
	if s.cache != nil && s.cache.GetJSON(ctx, cacheKey, &cached) {
		return cached.Items, nil
	}

	libIDs := make([]string, 0, len(libraries))
	for i, lib := range libraries {
		out[i] = LibraryPreviewItem{
			Library: lib,
			Total:   0,
			Cards:   []SeriesCard{},
		}
		libIDs = append(libIDs, lib.ID)
	}

	if includeCounts {
		counts, err := s.repo.Media.CountByLibraries(ctx, libIDs, filter)
		if err != nil {
			return nil, err
		}
		for i := range out {
			if total, ok := counts[out[i].ID]; ok {
				out[i].Total = total
			}
		}
	}

	// Preview rows are only an internal candidate window. Keep it bounded so a
	// library with a very long series cannot turn a homepage request into a full
	// 50k-row scan merely to find another distinct card.
	fetchCount := cardLimit * 12
	if fetchCount < 120 {
		fetchCount = 120
	} else if fetchCount > 400 {
		fetchCount = 400
	}

	recentByLibrary, err := s.repo.Media.ListRecentByLibraries(ctx, libIDs, fetchCount, filter)
	if err != nil {
		return nil, err
	}

	allPreviewItems := make([]model.Media, 0, len(libIDs)*fetchCount)
	for i := range out {
		items := recentByLibrary[out[i].ID]
		if len(items) == 0 {
			continue
		}
		allPreviewItems = append(allPreviewItems, items...)
	}
	s.attachLibraryMetadata(ctx, allPreviewItems)

	for i := range out {
		items := recentByLibrary[out[i].ID]
		if len(items) == 0 {
			continue
		}
		cards := groupMediaSeriesCards(items)
		if len(cards) > cardLimit {
			cards = cards[:cardLimit]
		}
		if cards == nil {
			cards = []SeriesCard{}
		}
		out[i].Cards = cards
	}

	if s.cache != nil {
		s.cache.SetJSON(ctx, cacheKey, libraryPreviewCacheValue{Items: out}, s.derivedReadCacheTTL())
	}

	return out, nil
}

// DeleteLibrary removes a library and its media rows. The on-disk files are
// left untouched.
func (s *MediaService) DeleteLibrary(ctx context.Context, id string) error {
	lib, err := s.repo.Library.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if lib != nil {
		if _, ok := ParseCloudLibraryMount(lib.Path); ok {
			err := s.repo.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if err := tx.Unscoped().Where("library_id = ?", id).Delete(&model.Media{}).Error; err != nil {
					return err
				}
				if err := hardDeleteLibraryRoots(ctx, tx, id); err != nil {
					return err
				}
				return tx.Unscoped().Where("id = ?", id).Delete(&model.Library{}).Error
			})
			if err == nil {
				s.invalidateMediaCache(ctx)
			}
			return err
		}
	}
	err = s.repo.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 本地库媒体行统一硬删除（与云库分支一致）：删库即彻底清空该库的
		// media 行，避免重扫时旧行被 upsert 复活却仍挂在已删库下，导致重建
		// 同名库后条目始终为 0。只作用于数据库表行，磁盘文件不受影响。
		if err := tx.Unscoped().Where("library_id = ?", id).Delete(&model.Media{}).Error; err != nil {
			return err
		}
		if err := hardDeleteLibraryRoots(ctx, tx, id); err != nil {
			return err
		}
		return tx.Unscoped().Delete(&model.Library{}, "id = ?", id).Error
	})
	if err == nil {
		s.invalidateMediaCache(ctx)
	}
	return err
}

func hardDeleteLibraryRoots(ctx context.Context, tx *gorm.DB, libraryID string) error {
	if tx == nil || !tx.Migrator().HasTable(&model.LibraryRoot{}) {
		return nil
	}
	return tx.WithContext(ctx).Unscoped().Where("library_id = ?", libraryID).Delete(&model.LibraryRoot{}).Error
}
