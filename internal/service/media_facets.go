package service

import (
	"context"
	"math/rand"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// LibraryFacets 是媒体库筛选面板需要的元数据：可选类型与年份区间。
type LibraryFacets struct {
	Genres  []GenreCount `json:"genres"`
	YearMin int          `json:"year_min"`
	YearMax int          `json:"year_max"`
}

// libraryFilterFrom 组装「可见性 + 用户筛选」的最终仓储条件，
// 与列表查询保持完全一致的语义。
func libraryFilterFrom(visibility MediaVisibility, filters MediaListFilters) repository.MediaQueryFilter {
	return filters.apply(repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
	})
}

// LibraryFacets 返回某个库的筛选项。类型统计复用 MediaDiscoveryService，
// 避免在筛选面板与 Emby /Genres 之间出现两套口径。
//
// 作用范围必须与列表查询一致：列表用的是「合并网盘库」后的库 ID 集合，因此这
// 里也把可见性收窄到同一集合，否则筛选项会漏掉列表里真实存在的条目。
func (s *MediaService) LibraryFacets(
	ctx context.Context,
	libraryID string,
	visibility MediaVisibility,
	discovery *MediaDiscoveryService,
) (LibraryFacets, error) {
	var facets LibraryFacets
	if s == nil || s.repo == nil || s.repo.Media == nil {
		return facets, nil
	}
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	merged, err := MergedLibraryIDsForLibrary(ctx, s.repo, libraryID)
	if err != nil {
		return facets, err
	}
	scoped, ok := scopeVisibilityToLibraries(visibility, merged)
	if !ok {
		facets.Genres = []GenreCount{}
		return facets, nil
	}

	yearMin, yearMax, err := s.repo.Media.YearRange(ctx, libraryFilterFrom(scoped, MediaListFilters{}))
	if err != nil {
		return facets, err
	}
	facets.YearMin = yearMin
	facets.YearMax = yearMax

	if discovery != nil {
		genres, err := discovery.AggregateGenres(ctx, scoped, "")
		if err != nil {
			return facets, err
		}
		facets.Genres = genres
	}
	if facets.Genres == nil {
		facets.Genres = []GenreCount{}
	}
	return facets, nil
}

// scopeVisibilityToLibraries 把可见性收窄到给定库集合。
//
// 返回 ok=false 表示「这些库与用户的可见性没有交集」——此时必须返回空结果，
// 而不是退化成不过滤，否则受限用户会看到别人的库。
func scopeVisibilityToLibraries(visibility MediaVisibility, libraryIDs []string) (MediaVisibility, bool) {
	allowed := make(map[string]struct{}, len(visibility.AllowedLibraryIDs))
	for _, id := range visibility.AllowedLibraryIDs {
		allowed[id] = struct{}{}
	}
	out := make([]string, 0, len(libraryIDs))
	for _, id := range libraryIDs {
		if len(allowed) > 0 {
			if _, ok := allowed[id]; !ok {
				continue
			}
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return visibility, false
	}
	visibility.AllowedLibraryIDs = out
	return visibility, true
}

// RandomMedia 在「可见性 + 筛选」的结果集里随机取一条。
//
// 实现是「先 COUNT 再随机 offset」的两段查询：SQLite 没有 TABLESAMPLE，
// ORDER BY RANDOM() 又会对整库排序（大库上会拖垮磁盘），因此用等价的
// 偏移量取法，且两种数据库方言完全一致。
//
// 返回 (nil, nil) 表示结果集为空，由调用方决定响应码。
func (s *MediaService) RandomMedia(
	ctx context.Context,
	libraryID string,
	visibility MediaVisibility,
	filters MediaListFilters,
) (*model.Media, error) {
	if s == nil || s.repo == nil || s.repo.Media == nil {
		return nil, nil
	}
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	libraryIDs, err := MergedLibraryIDsForLibrary(ctx, s.repo, libraryID)
	if err != nil {
		return nil, err
	}
	filter := libraryFilterFrom(visibility, filters)

	_, total, err := s.repo.Media.ListByLibrariesFiltered(ctx, libraryIDs, 0, 1, filter)
	if err != nil {
		return nil, err
	}
	if total <= 0 {
		return nil, nil
	}

	// 超大结果集直接对全部记录随机 OFFSET 会让数据库扫描海量行（SQLite 逐行计数）。
	// 当总量超过阈值时，收窄到按更新时间最新的前 N 条（列表排序首列是
	// release_date / updated_at，前 randomPoolCap 行即为"最新"子集），
	// 偏移量取其中随机位置，兼顾性能与覆盖面。
	const randomPoolCap = 5000
	effectiveTotal := total
	if effectiveTotal > randomPoolCap {
		effectiveTotal = randomPoolCap
	}
	offset := 0
	if effectiveTotal > 1 {
		offset = rand.Intn(int(effectiveTotal))
	}
	items, err := s.repo.Media.ListByLibrariesFilteredNoCount(ctx, libraryIDs, offset, 1, filter)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	s.attachLibraryMetadata(ctx, items)
	return &items[0], nil
}
