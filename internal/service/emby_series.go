package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

type embySeriesGroup struct {
	ID                 string
	LibraryID          string
	Name               string
	PosterURL          string
	BackdropURL        string
	Overview           string
	Rating             float32
	Year               int
	ReleaseDate        string
	TMDbID             int
	BangumiID          int
	CreatedAt          time.Time
	DateLastMediaAdded time.Time
	Episodes           []model.Media
}

type embySeasonGroup struct {
	ID        string
	SeriesID  string
	LibraryID string
	Name      string
	SeasonNum int
	Series    embySeriesGroup
	Episodes  []model.Media
}

func (e *EmbyService) findSeriesGroup(ctx context.Context, id, userID string) (embySeriesGroup, bool, error) {
	if strings.TrimSpace(id) == "" {
		return embySeriesGroup{}, false, nil
	}
	if strings.HasPrefix(id, embyVirtualSeriesPrefix) {
		if group, ok := e.cachedSeriesGroup(id); ok {
			return group, true, nil
		}
	}
	var rows []model.Media
	q := e.repo.DB.WithContext(ctx).Model(&model.Media{}).Where("season_num > 0 OR episode_num > 0")
	q = e.applyUserMediaVisibility(ctx, q, userID)
	if !strings.HasPrefix(id, embyVirtualSeriesPrefix) {
		q = q.Where("series_id = ?", id)
	} else {
		// 虚拟 series ID 只可能来自 series_id 为空的媒体：
		// 有 series_id 时分组 key 就是 series_id 本身（UUID，不带虚拟前缀）。
		q = q.Where("series_id IS NULL OR series_id = ''")
	}
	if err := q.Order("media.season_num asc, media.episode_num asc, media.created_at asc").Limit(embySeriesGroupingLimit).Find(&rows).Error; err != nil {
		return embySeriesGroup{}, false, err
	}
	for _, group := range e.seriesGroupsFromMedia(ctx, rows) {
		if group.ID == id {
			e.rememberSeriesGroup(group)
			return group, true, nil
		}
	}
	if !strings.HasPrefix(id, embyVirtualSeriesPrefix) {
		if series, err := e.repo.Series.FindByID(ctx, id); err != nil {
			return embySeriesGroup{}, false, err
		} else if series != nil {
				return embySeriesGroup{
					ID:                 series.ID,
					LibraryID:          series.LibraryID,
					Name:               series.Title,
					PosterURL:          series.PosterURL,
					BackdropURL:        series.BackdropURL,
					Overview:           series.Overview,
					Rating:             series.Rating,
					Year:               series.Year,
					TMDbID:             series.TMDbID,
					BangumiID:          series.BangumiID,
					CreatedAt:          series.CreatedAt,
					DateLastMediaAdded: series.CreatedAt,
				}, true, nil
		}
	}
	return embySeriesGroup{}, false, nil
}

func (e *EmbyService) findSeasonGroup(ctx context.Context, id, userID string) (embySeasonGroup, bool, error) {
	if strings.TrimSpace(id) == "" || !strings.HasPrefix(id, embyVirtualSeasonPrefix) {
		return embySeasonGroup{}, false, nil
	}
	if season, ok := e.cachedSeasonGroup(id); ok {
		return season, true, nil
	}
	// 虚拟 Season ID 是 hash(seriesKey, seasonNum)，无法反解出 series。
	// 常见情况（已刮削、series_id 非空）先用一条小型 DISTINCT 查询枚举候选对，
	// 在内存中算哈希匹配，命中后只加载该一部剧的剧集行，避免整库扫描。
	if season, ok, err := e.findSeasonGroupBySeriesCandidates(ctx, id, userID); err != nil {
		return embySeasonGroup{}, false, err
	} else if ok {
		return season, true, nil
	}
	// 回退：未刮削（series_id 为空，虚拟 key 由库名+名称派生）的媒体只能全量分组。
	var rows []model.Media
	q := e.repo.DB.WithContext(ctx).Model(&model.Media{}).
		Where("(series_id IS NULL OR series_id = '') AND (season_num > 0 OR episode_num > 0)")
	q = e.applyUserMediaVisibility(ctx, q, userID)
	if err := q.
		Order("media.season_num asc, media.episode_num asc, media.created_at asc").
		Limit(embySeriesGroupingLimit).
		Find(&rows).Error; err != nil {
		return embySeasonGroup{}, false, err
	}
	for _, series := range e.seriesGroupsFromMedia(ctx, rows) {
		for _, season := range e.seasonsForSeries(series) {
			if season.ID == id {
				e.rememberSeriesGroup(series)
				return season, true, nil
			}
		}
	}
	return embySeasonGroup{}, false, nil
}

// findSeasonGroupBySeriesCandidates resolves virtual season IDs for media that
// carry a real series_id: enumerate distinct (series_id, season_num) pairs via
// SQL, hash each candidate to find the matching season, then load only that
// one series' episodes.
func (e *EmbyService) findSeasonGroupBySeriesCandidates(ctx context.Context, id, userID string) (embySeasonGroup, bool, error) {
	type seasonCandidate struct {
		SeriesID  string
		SeasonNum int
	}
	var candidates []seasonCandidate
	q := e.repo.DB.WithContext(ctx).Model(&model.Media{}).
		Select("DISTINCT series_id, season_num").
		Where("series_id <> '' AND (season_num > 0 OR episode_num > 0)")
	q = e.applyUserMediaVisibility(ctx, q, userID)
	if err := q.Find(&candidates).Error; err != nil {
		return embySeasonGroup{}, false, err
	}
	matched := make([]string, 0, 1)
	for _, cand := range candidates {
		for _, seasonNum := range embySeasonCandidates(cand.SeasonNum) {
			if seasonID(cand.SeriesID, seasonNum) == id {
				matched = append(matched, cand.SeriesID)
				break
			}
		}
	}
	for _, matchedSeries := range matched {
		season, ok, err := e.seasonGroupForSeries(ctx, id, matchedSeries, userID)
		if err != nil || ok {
			return season, ok, err
		}
	}
	return embySeasonGroup{}, false, nil
}

// seasonGroupForSeries rebuilds the season groups of one series (small row
// set) and returns the one matching the virtual season id.
func (e *EmbyService) seasonGroupForSeries(ctx context.Context, id, seriesID, userID string) (embySeasonGroup, bool, error) {
	var rows []model.Media
	rq := e.repo.DB.WithContext(ctx).Model(&model.Media{}).
		Where("series_id = ? AND (season_num > 0 OR episode_num > 0)", seriesID)
	rq = e.applyUserMediaVisibility(ctx, rq, userID)
	if err := rq.
		Order("media.season_num asc, media.episode_num asc, media.created_at asc").
		Limit(embySeriesGroupingLimit).
		Find(&rows).Error; err != nil {
		return embySeasonGroup{}, false, err
	}
	for _, series := range e.seriesGroupsFromMedia(ctx, rows) {
		if series.ID != seriesID {
			continue
		}
		for _, season := range e.seasonsForSeries(series) {
			if season.ID == id {
				e.rememberSeriesGroup(series)
				return season, true, nil
			}
		}
	}
	return embySeasonGroup{}, false, nil
}

func (e *EmbyService) seriesGroupsFromMedia(ctx context.Context, rows []model.Media) []embySeriesGroup {
	byID := map[string]*embySeriesGroup{}
	order := []string{}
	for _, row := range rows {
		row := row
		seriesID := e.seriesIDForMedia(ctx, &row)
		group, ok := byID[seriesID]
		if !ok {
				group = &embySeriesGroup{
					ID:                 seriesID,
					LibraryID:          row.LibraryID,
					Name:               e.seriesNameForMedia(ctx, &row),
					Year:               row.Year,
					ReleaseDate:        row.ReleaseDate,
					TMDbID:             row.TMDbID,
					BangumiID:          row.BangumiID,
					CreatedAt:          row.CreatedAt,
					DateLastMediaAdded: row.CreatedAt,
				}
				byID[seriesID] = group
				order = append(order, seriesID)
			}
			if row.CreatedAt.Before(group.CreatedAt) || group.CreatedAt.IsZero() {
				group.CreatedAt = row.CreatedAt
			}
			if row.CreatedAt.After(group.DateLastMediaAdded) {
				group.DateLastMediaAdded = row.CreatedAt
			}
		if strings.TrimSpace(row.ReleaseDate) != "" && mediaReleaseSortTime(row).After(embySeriesReleaseSortTime(*group)) {
			group.ReleaseDate = row.ReleaseDate
			if row.Year > 0 {
				group.Year = row.Year
			}
		} else if group.ReleaseDate == "" && group.Year == 0 && row.Year > 0 {
			group.Year = row.Year
		}
		if group.PosterURL == "" && row.PosterURL != "" {
			group.PosterURL = row.PosterURL
		}
		if group.BackdropURL == "" && row.BackdropURL != "" {
			group.BackdropURL = row.BackdropURL
		}
		if group.Overview == "" && row.Overview != "" {
			group.Overview = row.Overview
		}
		if group.Rating == 0 && row.Rating > 0 {
			group.Rating = row.Rating
		}
		if group.Year == 0 && row.Year > 0 {
			group.Year = row.Year
		}
		group.Episodes = append(group.Episodes, row)
	}
	groups := make([]embySeriesGroup, 0, len(order))
	for _, id := range order {
		group := *byID[id]
		sort.SliceStable(group.Episodes, func(i, j int) bool {
			if group.Episodes[i].SeasonNum != group.Episodes[j].SeasonNum {
				return group.Episodes[i].SeasonNum < group.Episodes[j].SeasonNum
			}
			if group.Episodes[i].EpisodeNum != group.Episodes[j].EpisodeNum {
				return group.Episodes[i].EpisodeNum < group.Episodes[j].EpisodeNum
			}
			return group.Episodes[i].CreatedAt.Before(group.Episodes[j].CreatedAt)
		})
		groups = append(groups, group)
	}
	return groups
}

func (e *EmbyService) seasonsForSeries(series embySeriesGroup) []embySeasonGroup {
	bySeason := map[int]*embySeasonGroup{}
	order := []int{}
	for _, episode := range series.Episodes {
		seasonNum := embySeasonNumForMedia(&episode)
		season, ok := bySeason[seasonNum]
		if !ok {
			season = &embySeasonGroup{
				ID:        seasonID(series.ID, seasonNum),
				SeriesID:  series.ID,
				LibraryID: series.LibraryID,
				Name:      seasonName(seasonNum),
				SeasonNum: seasonNum,
				Series:    series,
			}
			bySeason[seasonNum] = season
			order = append(order, seasonNum)
		}
		season.Episodes = append(season.Episodes, episode)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return embySeasonSortOrder(order[i]) < embySeasonSortOrder(order[j])
	})
	out := make([]embySeasonGroup, 0, len(order))
	for _, seasonNum := range order {
		out = append(out, *bySeason[seasonNum])
	}
	return out
}
