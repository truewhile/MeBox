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
		return e.findVirtualSeriesGroup(ctx, id, userID)
	}
	var rows []model.Media
	q := e.repo.DB.WithContext(ctx).Model(&model.Media{}).Where("season_num > 0 OR episode_num > 0")
	q = e.applyUserMediaVisibility(ctx, q, userID)
	q = q.Where("series_id = ?", id)
	if err := q.Order("media.season_num asc, media.episode_num asc, media.episode_fraction asc, media.created_at asc").Limit(embySeriesGroupingLimit).Find(&rows).Error; err != nil {
		return embySeriesGroup{}, false, err
	}
	groups := e.seriesGroupsFromMedia(ctx, rows)
	bulkCached := e.rememberScannedSeriesGroups(groups)
	for _, group := range groups {
		if group.ID == id {
			if !bulkCached {
				e.rememberSeriesGroup(group)
			}
			return group, true, nil
		}
	}
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
	return embySeriesGroup{}, false, nil
}

// findVirtualSeriesGroup 处理 series_id 为空的剧集分组（虚拟 series ID）。
//
// 虚拟 ID 是 hash(库名 + 剧名)，无法反解，只能整库算出分组后按 ID 取用。
// 这里走 loadVirtualSeriesGroups，把同一用户的并发请求折叠成一次扫描：
// 首页一次刷新会同时要 73 个库的卡片和海报，各扫一遍在 2 核主机上要 14-17s。
func (e *EmbyService) findVirtualSeriesGroup(ctx context.Context, id, userID string) (embySeriesGroup, bool, error) {
	if group, ok := e.cachedSeriesGroup(id); ok {
		return group, true, nil
	}
	if e.virtualScanRecently(userID) {
		return embySeriesGroup{}, false, nil
	}
	groups, err := e.loadVirtualSeriesGroups(ctx, userID)
	if err != nil {
		return embySeriesGroup{}, false, err
	}
	for _, group := range groups {
		if group.ID == id {
			return group, true, nil
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
	// 复用共享扫描：首页并发刷新时整库只扫一次。
	groups, err := e.loadVirtualSeriesGroups(ctx, userID)
	if err != nil {
		return embySeasonGroup{}, false, err
	}
	for _, series := range groups {
		for _, season := range e.seasonsForSeries(series) {
			if season.ID == id {
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
		Order("media.season_num asc, media.episode_num asc, media.episode_fraction asc, media.created_at asc").
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

// rememberScannedSeriesGroups 批量记住一次扫描得到的全部分组，返回是否真的
// 批量写入（超过剧集预算时不做，调用方需自行缓存命中的那一部）。
//
// 只缓存命中的一部时，同一部剧的多张卡片/海报仍要各扫一次；整批写入后，
// 同一次扫描算出来的其他剧也直接命中，于是整页 16 张卡片 + 16 张海报共用
// 一次全表扫描。
func (e *EmbyService) rememberScannedSeriesGroups(groups []embySeriesGroup) bool {
	if e == nil || len(groups) == 0 {
		return false
	}
	episodes := 0
	for i := range groups {
		episodes += len(groups[i].Episodes)
		if episodes > embySeriesGroupBulkCacheEpisodes {
			return false
		}
	}
	e.rememberSeriesGroups(groups)
	return true
}

// loadVirtualSeriesGroups 返回 series_id 为空的整库剧集分组。
//
// 同一用户的并发调用共享同一次扫描：客户端刷新首页时 73 个库的 Latest 同时
// 进来，每个库的卡片和海报又各自要自己的虚拟 series 分组。分组缓存键各不
// 相同，singleflight 也就不会合并，于是第一波请求把整张 media 表并发重算
// 了 73 次（生产环境实测 14-17s）。这里按用户折叠成一次，结果整批进缓存，
// 调用方拿到切片后按 ID 取用即可。
func (e *EmbyService) loadVirtualSeriesGroups(ctx context.Context, userID string) ([]embySeriesGroup, error) {
	if e == nil || e.repo == nil {
		return nil, nil
	}
	v, err, _ := e.virtualGroups.Do("virtual:"+userID, func() (any, error) {
		// 与 loadLatestItemsCached 一致：一个客户端断开不应取消正在为其他
		// 客户端填充的共享扫描。
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		var rows []model.Media
		q := e.repo.DB.WithContext(loadCtx).Model(&model.Media{}).
			Where("(series_id IS NULL OR series_id = '') AND (season_num > 0 OR episode_num > 0)")
		q = e.applyUserMediaVisibility(loadCtx, q, userID)
		if err := q.
			Order("media.season_num asc, media.episode_num asc, media.episode_fraction asc, media.created_at asc").
			Limit(embySeriesGroupingLimit).
			Find(&rows).Error; err != nil {
			return nil, err
		}
		groups := e.seriesGroupsFromMedia(loadCtx, rows)
		e.rememberSeriesGroups(groups)
		e.markVirtualScan(userID)
		return groups, nil
	})
	if err != nil {
		return nil, err
	}
	groups, _ := v.([]embySeriesGroup)
	return groups, nil
}

// markVirtualScan 记录该用户最近一次整库扫描的时间。
func (e *EmbyService) markVirtualScan(userID string) {
	e.virtualMu.Lock()
	if e.virtualScanAt == nil {
		e.virtualScanAt = make(map[string]time.Time, 4)
	}
	e.virtualScanAt[userID] = time.Now()
	e.virtualMu.Unlock()
}

// virtualScanRecently 报告该用户是否刚做过一次整库扫描。窗口内仍不在缓存里
// 的目标 ID 按不存在处理，避免为同一个未知 ID 反复重扫整库。
func (e *EmbyService) virtualScanRecently(userID string) bool {
	e.virtualMu.RLock()
	scannedAt := e.virtualScanAt[userID]
	e.virtualMu.RUnlock()
	return !scannedAt.IsZero() && time.Since(scannedAt) < embyVirtualScanGrace
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
			if group.Episodes[i].EpisodeFraction != group.Episodes[j].EpisodeFraction {
				return group.Episodes[i].EpisodeFraction < group.Episodes[j].EpisodeFraction
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
