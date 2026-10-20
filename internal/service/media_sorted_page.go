package service

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ListMediaVisibleSortedPage reuses grouped rows and a small position index.
// Only the requested page is copied; pagination does not sort the full library
// again or retain a second full-size MediaItem slice for every sort order.
func (s *MediaService) ListMediaVisibleSortedPage(
	ctx context.Context,
	libraryID string,
	page, pageSize int,
	visibility MediaVisibility,
	filters MediaListFilters,
	spec MediaSortSpec,
	history map[string]time.Time,
) ([]MediaItem, int64, error) {
	grouped, err := s.groupedMediaVisibleFiltered(ctx, libraryID, visibility, filters)
	if err != nil {
		return nil, 0, err
	}
	spec = NormalizeMediaSort(spec.Field, spec.Order)
	indices := s.sortedMediaIndices(grouped, spec, history)
	page, pageSize = normalizeGroupedMediaPage(page, pageSize)
	total := len(grouped.Items)
	// Division avoids overflowing (page-1)*pageSize on an excessive page value.
	if total == 0 || page-1 > (total-1)/pageSize {
		return []MediaItem{}, int64(total), nil
	}
	start := (page - 1) * pageSize
	end := min(start+pageSize, total)
	items := make([]MediaItem, 0, end-start)
	for _, index := range indices[start:end] {
		items = append(items, grouped.Items[index])
	}
	return items, int64(total), nil
}

func (s *MediaService) sortedMediaIndices(grouped *groupedMediaCacheValue, spec MediaSortSpec, history map[string]time.Time) []int {
	// Playback history changes independently of the media cache and is personal.
	// Keep this explicit sort live rather than caching another user's history.
	if s.cache == nil || spec.Field == "last_played" || len(grouped.Items) == 0 {
		return sortMediaIndices(grouped.Items, spec, history)
	}
	key := fmt.Sprintf("media:obj:sort:%s:%s:%s", grouped.Revision, spec.Field, spec.Order)
	value, _, _ := s.mediaSortFlight.Do(key, func() (any, error) {
		if cached, ok := s.cache.GetObject(key); ok {
			if indices, ok := cached.([]int); ok {
				return indices, nil
			}
		}
		indices := sortMediaIndices(grouped.Items, spec, history)
		s.cache.SetObject(key, indices, s.mediaObjectTTL())
		return indices, nil
	})
	return value.([]int)
}

func sortMediaIndices(items []MediaItem, spec MediaSortSpec, history map[string]time.Time) []int {
	indices := make([]int, len(items))
	for i := range indices {
		indices[i] = i
	}
	sort.SliceStable(indices, func(i, j int) bool {
		a, b := items[indices[i]], items[indices[j]]
		cmp := compareMediaByField(a, b, spec, history)
		if cmp == 0 {
			cmp = compareID(a.ID, b.ID)
		}
		return cmp < 0
	})
	return indices
}
