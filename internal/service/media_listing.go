package service

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// ListMedia paginates media items inside a library.
// MediaListFilters 是列表接口的可选筛选条件（来自查询串或库内筛选面板）。
//
// 与 MediaVisibility 分开：可见性是权限约束（服务端强制），这些是用户主动
// 选择的浏览条件，两者在 SQL 层是「与」关系。
type MediaListFilters struct {
	Genres    []string
	YearMin   int
	YearMax   int
	RatingMin float64
	Unwatched bool
	// UserID 是「未观看」判定所需的账号；为空时 Unwatched 被忽略。
	UserID string
}

// empty 报告是否没有任何筛选条件。调用方据此走缓存友好的默认路径。
func (f MediaListFilters) empty() bool {
	return len(f.Genres) == 0 && f.YearMin <= 0 && f.YearMax <= 0 &&
		f.RatingMin <= 0 && !f.Unwatched
}

// apply 把筛选条件叠加到仓储过滤条件上。
func (f MediaListFilters) apply(filter repository.MediaQueryFilter) repository.MediaQueryFilter {
	if len(f.Genres) > 0 {
		filter.Genres = f.Genres
	}
	if f.YearMin > 0 {
		filter.YearMin = f.YearMin
	}
	if f.YearMax > 0 {
		filter.YearMax = f.YearMax
	}
	if f.RatingMin > 0 {
		filter.RatingMin = f.RatingMin
	}
	if f.Unwatched && strings.TrimSpace(f.UserID) != "" {
		filter.UnwatchedOnly = true
		filter.UnwatchedUserID = f.UserID
	}
	return filter
}

func (s *MediaService) ListMedia(ctx context.Context, libraryID string, page, pageSize int) ([]model.Media, int64, error) {
	return s.ListMediaVisible(ctx, libraryID, page, pageSize, MediaVisibility{IncludeNSFW: true})
}

func (s *MediaService) ListMediaVisible(ctx context.Context, libraryID string, page, pageSize int, visibility MediaVisibility) ([]model.Media, int64, error) {
	return s.ListMediaVisibleFiltered(ctx, libraryID, page, pageSize, visibility, MediaListFilters{})
}

// ListMediaVisibleFiltered 在可见性之上叠加用户筛选条件。
func (s *MediaService) ListMediaVisibleFiltered(
	ctx context.Context,
	libraryID string,
	page, pageSize int,
	visibility MediaVisibility,
	filters MediaListFilters,
) ([]model.Media, int64, error) {
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
	filter := filters.apply(repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
	})
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
	grouped, err := s.GroupedMediaVisible(ctx, libraryID, visibility)
	if err != nil {
		return nil, 0, err
	}
	return paginateMediaItems(grouped, page, pageSize), int64(len(grouped)), nil
}

// GroupedMediaVisible returns the complete version-grouped media list before pagination.
// The result is cached as an immutable slice; sort/pagination callers must copy it before mutating.
func (s *MediaService) GroupedMediaVisible(ctx context.Context, libraryID string, visibility MediaVisibility) ([]MediaItem, error) {
	return s.GroupedMediaVisibleFiltered(ctx, libraryID, visibility, MediaListFilters{})
}

// GroupedMediaVisibleFiltered 在可见性之上叠加用户筛选条件。
//
// 版本分组的筛选必须作用在原始行上（先筛后分组）：否则「按类型筛选」会把
// 同一部作品的不同版本拆到不同筛选结果里，出现重复卡片。
func (s *MediaService) GroupedMediaVisibleFiltered(
	ctx context.Context,
	libraryID string,
	visibility MediaVisibility,
	filters MediaListFilters,
) ([]MediaItem, error) {
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	libraryIDs, err := MergedLibraryIDsForLibrary(ctx, s.repo, libraryID)
	if err != nil {
		return nil, err
	}
	filter := filters.apply(repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
	})
	itemsCacheKey := s.groupedItemsCacheKey(libraryID, libraryIDs, filter)
	value, err, _ := s.groupedMediaFlight.Do(itemsCacheKey, func() (any, error) {
		if s.cache != nil {
			if cachedObj, ok := s.cache.GetObject(itemsCacheKey); ok {
				if cached, ok := cachedObj.([]MediaItem); ok {
					return cached, nil
				}
			}
		}
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		items, err := s.listMediaVisibleForGrouping(loadCtx, libraryID, visibility, filters)
		if err != nil {
			return nil, err
		}
		grouped := groupMediaVersions(items)
		if s.cache != nil && len(grouped) > 0 {
			s.cache.SetObject(itemsCacheKey, grouped, s.mediaObjectTTL())
		}
		return grouped, nil
	})
	if err != nil {
		return nil, err
	}
	if grouped, ok := value.([]MediaItem); ok {
		return grouped, nil
	}
	return nil, nil
}

func (s *MediaService) listMediaVisibleForGrouping(ctx context.Context, libraryID string, visibility MediaVisibility, filters MediaListFilters) ([]model.Media, error) {
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	libraryIDs, err := MergedLibraryIDsForLibrary(ctx, s.repo, libraryID)
	if err != nil {
		return nil, err
	}
	filter := filters.apply(repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
	})
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
func (s *MediaService) GetMedia(ctx context.Context, id string) (*model.Media, error) {	media, err := s.repo.Media.FindByID(ctx, id)
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
	// 成人条目按番号分组，而番号常常只存在于路径/标题里：本地 NFO 来源的分片
	// 没有 douban_id/thetvdb_id（MetaTube 刮削出来的那几个才有）。若仍按外部
	// ID 预先收窄候选集，同番号的其它分片会被 SQL 直接排除，表现就是库里折叠
	// 出了 N 个版本、详情页却只列出带 ID 的那几个。因此成人条目不预先收窄，
	// 直接在该库范围内比对版本键。未刮削但文件名带番号的分片同理：同一部片的
	// 各分片元数据来源不一致（刮削 vs pending），收窄后标题也对不上。
	var candidates []model.Media
	narrowed := false
	hasAdultCode := mediaAdultGroupCode(*media) != "" ||
		canonicalAdultGroupCode(AdultCodeFromMediaPath(media.Path)) != ""
	if !hasAdultCode {
		candidates, narrowed, err = s.repo.Media.ListVersionCandidates(ctx, libraryIDs, *media, 5000)
		if err != nil {
			return nil, err
		}
	}
	if !narrowed {
		candidates, err = s.repo.Media.ListByLibrariesFilteredNoCount(ctx, libraryIDs, 0, 5000, filter)
		if err != nil {
			return nil, err
		}
	}
	s.attachLibraryMetadata(ctx, candidates)
	// 同番号只要有一个分片被确认为成人内容，尚未刮削的分片也按番号折叠，
	// 与列表页的分组结果保持一致。
	vouched := adultCodesVouchedByNSFW(candidates)
	if vouchKey := adultVouchedGroupKey(*media, vouched); vouchKey != "" {
		key = vouchKey
	}
	matched := make([]model.Media, 0, 4)
	for _, row := range candidates {
		if mediaVersionGroupKeyWithAdultVouch(row, vouched) == key {
			matched = append(matched, row)
		}
	}
	if len(matched) == 0 {
		return []model.Media{*media}, nil
	}
	sortMediaVersionsForDisplay(matched)
	return matched, nil
}
