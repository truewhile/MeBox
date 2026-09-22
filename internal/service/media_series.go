package service

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

type seriesCardsCacheValue struct {
	Cards []SeriesCard `json:"cards"`
	Total int64        `json:"total"`
}

// libraryRowsCacheValue 整库可见行 + 预计算索引的对象缓存。填充时一次性完成
// 整库 SQL 加载与 series key 解析，之后剧集卡片、剧集列表、媒体详情剧集
// 共享这份结果：命中路径零 SQL、零逐行 key 解析。Rows/Episodes 视为不可变。
type libraryRowsCacheValue struct {
	Rows     []model.Media
	Resolver mediaSeriesKeyResolver
	Episodes map[string][]model.Media
	Cards    []SeriesCard
}

type SeriesCard struct {
	Key         string      `json:"key"`
	Rep         model.Media `json:"rep"`
	LinkMedia   model.Media `json:"linkMedia"`
	Count       int         `json:"count"`
	IsSeries    bool        `json:"is_series,omitempty"`
	LastAddedAt *time.Time  `json:"last_added_at,omitempty"`
}

// SeriesCardView is the compact payload used by homepage and library preview
// endpoints. LinkMedia is only needed to resolve the target library ID, so
// sending the full media row twice roughly doubles the preview JSON for no UI
// benefit.
type SeriesCardView struct {
	Key           string      `json:"key"`
	Rep           model.Media `json:"rep"`
	LinkLibraryID string      `json:"linkLibraryId,omitempty"`
	Count         int         `json:"count"`
	IsSeries      bool        `json:"is_series,omitempty"`
	LastAddedAt   *time.Time  `json:"last_added_at,omitempty"`
}

func NewSeriesCardViews(cards []SeriesCard) []SeriesCardView {
	if len(cards) == 0 {
		return []SeriesCardView{}
	}
	out := make([]SeriesCardView, len(cards))
	for i, card := range cards {
		out[i] = SeriesCardView{
			Key:           card.Key,
			Rep:           card.Rep,
			LinkLibraryID: mediaTargetLibraryID(card.LinkMedia),
			Count:         card.Count,
			IsSeries:      card.IsSeries,
			LastAddedAt:   card.LastAddedAt,
		}
	}
	return out
}

type seriesCardGroup struct {
	card   SeriesCard
	latest time.Time
}

// libraryRowsWithIndex 返回整库行与预计算剧集索引（带对象缓存）。
func (s *MediaService) libraryRowsWithIndex(ctx context.Context, libraryID string, visibility MediaVisibility) (*libraryRowsCacheValue, error) {
	visibility = ExpandMediaVisibilityForMergedCloudLibraries(ctx, s.repo, visibility)
	cacheKey := s.libraryRowsCacheKey(libraryID, visibility)
	value, err, _ := s.libraryRowsFlight.Do(cacheKey, func() (any, error) {
		if s.cache != nil {
			if obj, ok := s.cache.GetObject(cacheKey); ok {
				if cached, ok := obj.(*libraryRowsCacheValue); ok {
					return cached, nil
				}
			}
		}
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		rows, _, err := s.listAllMediaVisible(loadCtx, libraryID, visibility)
		if err != nil {
			return nil, err
		}
		// listAllMediaVisible 走 ListMediaVisible，行已带库元数据（resolver 的
		// key 计算依赖 DisplayLibraryPath/ID）。
		resolver, keys := resolveMediaSeriesKeys(rows)
		episodes := make(map[string][]model.Media, len(rows)/4+1)
		for i, row := range rows {
			k := keys[i]
			if k == "" {
				continue
			}
			episodes[k] = append(episodes[k], row)
		}
		cards := groupMediaSeriesCardsByKeys(rows, keys)
		value := &libraryRowsCacheValue{Rows: rows, Resolver: resolver, Episodes: episodes, Cards: cards}
		if s.cache != nil {
			s.cache.SetObject(cacheKey, value, s.derivedReadCacheTTL())
		}
		return value, nil
	})
	if err != nil {
		return nil, err
	}
	if cached, ok := value.(*libraryRowsCacheValue); ok {
		return cached, nil
	}
	return nil, nil
}

func (s *MediaService) ListLibrarySeriesCards(ctx context.Context, libraryID string, visibility MediaVisibility) ([]SeriesCard, int64, error) {
	cacheKey := s.libraryCardsObjectKey(libraryID, visibility)
	if s.cache != nil {
		if obj, ok := s.cache.GetObject(cacheKey); ok {
			if cached, ok := obj.(*seriesCardsCacheValue); ok {
				return cached.Cards, cached.Total, nil
			}
		}
	}
	rows, err := s.libraryRowsWithIndex(ctx, libraryID, visibility)
	if err != nil {
		return nil, 0, err
	}
	cards := rows.Cards
	if cards == nil && len(rows.Rows) > 0 {
		// Fallback keeps rolling-cache compatibility if an older runtime cache
		// value was created before Cards was added.
		cards = groupMediaSeriesCards(rows.Rows)
	}
	if cards == nil {
		cards = []SeriesCard{}
	}
	total := int64(len(cards))
	if s.cache != nil {
		s.cache.SetObject(cacheKey, &seriesCardsCacheValue{Cards: cards, Total: total}, s.derivedReadCacheTTL())
	}
	return cards, total, nil
}

// ListLibrarySeriesCardsFiltered 在系列卡片上应用筛选。
//
// 语义：先对原始剧集行做筛选，再分组 —— 于是「剧里任意一集命中条件」即可保留
// 该剧。这比只筛代表行更符合直觉（用户勾选「动作」是想要动作剧，而不是
// 「第一集恰好是动作的剧」）。
//
// 无筛选时直接走带缓存的原路径，避免平白多一次全库分组。
func (s *MediaService) ListLibrarySeriesCardsFiltered(
	ctx context.Context,
	libraryID string,
	visibility MediaVisibility,
	filters MediaListFilters,
) ([]SeriesCard, int64, error) {
	if filters.empty() {
		return s.ListLibrarySeriesCards(ctx, libraryID, visibility)
	}
	rows, err := s.libraryRowsWithIndex(ctx, libraryID, visibility)
	if err != nil {
		return nil, 0, err
	}
	completed := s.completedMediaIDSet(ctx, filters)
	kept := make([]model.Media, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if !mediaRowMatchesFilters(&row, filters, completed) {
			continue
		}
		kept = append(kept, row)
	}
	cards := groupMediaSeriesCards(kept)
	if cards == nil {
		cards = []SeriesCard{}
	}
	return cards, int64(len(cards)), nil
}

// mediaRowMatchesFilters 在内存里复现 SQL 层的筛选语义。
//
// 系列路径无法直接复用仓储过滤（它基于整库共享缓存），因此这里必须与
// applyMediaQueryFilter 保持同一套判定，否则会出现「电影库能筛、剧集库不同」
// 的行为差异。
func mediaRowMatchesFilters(row *model.Media, filters MediaListFilters, completed map[string]bool) bool {
	if row == nil {
		return false
	}
	if filters.YearMin > 0 && row.Year < filters.YearMin {
		return false
	}
	if filters.YearMax > 0 && row.Year > filters.YearMax {
		return false
	}
	if filters.RatingMin > 0 && float64(row.Rating) < filters.RatingMin {
		return false
	}
	if filters.Unwatched && completed[row.ID] {
		return false
	}
	if len(filters.Genres) > 0 {
		rowGenres := genreSet(row.Genres)
		matched := false
		for _, want := range filters.Genres {
			if _, ok := rowGenres[strings.ToLower(strings.TrimSpace(want))]; ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// completedMediaIDSet 返回该用户已标记看完的媒体 ID 集合（未启用未观看筛选时
// 返回 nil，避免无谓查询）。
func (s *MediaService) completedMediaIDSet(ctx context.Context, filters MediaListFilters) map[string]bool {
	userID := strings.TrimSpace(filters.UserID)
	if !filters.Unwatched || userID == "" {
		return nil
	}
	var rows []model.PlaybackHistory
	if err := s.repo.DB.WithContext(ctx).
		Where("user_id = ? AND completed = ?", userID, true).
		Find(&rows).Error; err != nil {
		return nil
	}
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[row.MediaID] = true
	}
	return out
}

func (s *MediaService) ListRecentSeriesCards(ctx context.Context, limit int, visibility MediaVisibility) ([]SeriesCard, error) {
	if limit <= 0 {
		limit = 24
	} else if limit > 100 {
		limit = 100
	}
	rows, err := s.SearchMediaVisible(ctx, "", maxMediaSearchLimit, visibility)
	if err != nil {
		return nil, err
	}
	cards := groupMediaSeriesCards(rows)
	if len(cards) == 0 {
		return []SeriesCard{}, nil
	}
	if len(cards) > limit {
		cards = cards[:limit]
	}
	return cards, nil
}

func (s *MediaService) ListLibrarySeriesEpisodes(ctx context.Context, libraryID, key string, visibility MediaVisibility) ([]model.Media, error) {
	rows, err := s.libraryRowsWithIndex(ctx, libraryID, visibility)
	if err != nil {
		return nil, err
	}
	// 预计算索引命中：O(1) 查找，拷贝后排序避免改动共享缓存。
	if episodes, ok := rows.Episodes[key]; ok && len(episodes) > 0 {
		out := make([]model.Media, len(episodes))
		copy(out, episodes)
		sortEpisodesForDisplay(out)
		return out, nil
	}

	// 兜底：索引未命中（如卡片 key 与行缓存短暂跨代），保持原线性匹配逻辑。
	all := rows.Rows
	out := make([]model.Media, 0)
	for _, row := range all {
		if rows.Resolver.key(row) == key {
			out = append(out, row)
		}
	}
	if len(out) == 0 {
		return []model.Media{}, nil
	}
	sortEpisodesForDisplay(out)
	return out, nil
}

// sortEpisodesForDisplay 与历史行为一致：季/集号升序，再按入库时间兜底。
func sortEpisodesForDisplay(out []model.Media) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SeasonNum != out[j].SeasonNum {
			return out[i].SeasonNum < out[j].SeasonNum
		}
		if out[i].EpisodeNum != out[j].EpisodeNum {
			return out[i].EpisodeNum < out[j].EpisodeNum
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
}

func (s *MediaService) ListMediaEpisodes(ctx context.Context, mediaID string, visibility MediaVisibility) ([]model.Media, error) {
	target, err := s.repo.Media.FindByID(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, errors.New("media not found")
	}
	if !visibility.Allows(target) {
		return nil, errors.New("media not found")
	}
	if target.LibraryID == "" {
		return []model.Media{*target}, nil
	}
	// 电影、音乐等单条目库不需要为了返回自身而加载整库。详情页会并行
	// 请求 /media/:id/episodes，未短路时每次冷缓存都会触发一次全库分组。
	if lib, err := s.repo.Library.FindByID(ctx, target.LibraryID); err == nil && lib != nil &&
		libraryUsesSingleMediaRows(lib.Type) &&
		strings.TrimSpace(target.SeriesID) == "" &&
		!mediaLooksEpisodicForGrouping(*target) {
		return []model.Media{*target}, nil
	}
	if seriesID := strings.TrimSpace(target.SeriesID); seriesID != "" {
		libraryIDs, err := MergedLibraryIDsForLibrary(ctx, s.repo, target.LibraryID)
		if err == nil && len(libraryIDs) > 0 {
			rows, queryErr := s.repo.Media.ListByLibrariesFilteredNoCount(ctx, libraryIDs, 0, maxMediaSearchLimit, repository.MediaQueryFilter{
				IncludeNSFW:       visibility.IncludeNSFW,
				AllowedLibraryIDs: visibility.AllowedLibraryIDs,
				HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
				SeriesID:          seriesID,
			})
			if queryErr == nil && len(rows) > 1 {
				s.attachLibraryMetadata(ctx, rows)
				sortEpisodesForDisplay(rows)
				return rows, nil
			}
		}
	}
	cache, err := s.libraryRowsWithIndex(ctx, target.LibraryID, visibility)
	if err != nil {
		return nil, err
	}
	rows := cache.Rows
	if len(rows) == 0 {
		return []model.Media{*target}, nil
	}

	var out []model.Media
	targetKey := cache.Resolver.key(*target)
	if targetKey != "" {
		// 预计算索引命中时拷贝，避免改动共享缓存。
		if episodes, ok := cache.Episodes[targetKey]; ok {
			out = make([]model.Media, len(episodes))
			copy(out, episodes)
		}
	}

	// 如果没有聚合到多集，尝试同父目录匹配（排除合集目录和公共分类目录，且同目录文件不能是互不相同的独立电影）
	if len(out) <= 1 && target.Path != "" {
		targetDir := filepath.Dir(strings.ReplaceAll(target.Path, "\\", "/"))
		parentBase := filepath.Base(targetDir)
		if !mediaParentLooksLikeCollection(target.Path) && !seriesTitleIsGenericContainer(parentBase, *target) {
			targetTitleNorm := normalizeSeriesTitle(target.Title)
			targetDirNorm := normalizeSeriesTitle(parentBase)
			dirMatches := make([]model.Media, 0)
			for _, row := range rows {
				if row.Path == "" || filepath.Dir(strings.ReplaceAll(row.Path, "\\", "/")) != targetDir {
					continue
				}
				if row.ID == target.ID {
					dirMatches = append(dirMatches, row)
					continue
				}
				rowTitleNorm := normalizeSeriesTitle(row.Title)
				allowMatch := false
				if isGenericMovieTitle(rowTitleNorm) || isGenericMovieTitle(targetTitleNorm) {
					allowMatch = true
				} else if rowTitleNorm != "" && rowTitleNorm == targetTitleNorm {
					allowMatch = true
				} else if rowTitleNorm != "" && targetDirNorm != "" && rowTitleNorm == targetDirNorm {
					allowMatch = true
				}
				if allowMatch {
					dirMatches = append(dirMatches, row)
				}
			}
			if len(dirMatches) > 1 {
				out = dirMatches
			}
		}
	}

	if len(out) == 0 {
		out = []model.Media{*target}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SeasonNum != out[j].SeasonNum {
			return out[i].SeasonNum < out[j].SeasonNum
		}
		if out[i].EpisodeNum != out[j].EpisodeNum {
			return out[i].EpisodeNum < out[j].EpisodeNum
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})

	return out, nil
}

func libraryUsesSingleMediaRows(libraryType string) bool {
	switch strings.ToLower(strings.TrimSpace(libraryType)) {
	case "movie", "movies", "music", "adult":
		return true
	default:
		return false
	}
}

func (s *MediaService) listAllMediaVisible(ctx context.Context, libraryID string, visibility MediaVisibility) ([]model.Media, int64, error) {
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
	rows, err := s.repo.Media.ListAllByLibrariesFilteredNoCount(ctx, libraryIDs, filter)
	if err != nil {
		return nil, 0, err
	}
	s.attachLibraryMetadata(ctx, rows)
	return rows, int64(len(rows)), nil
}

func groupMediaSeriesCards(items []model.Media) []SeriesCard {
	if len(items) == 0 {
		return nil
	}
	_, keys := resolveMediaSeriesKeys(items)
	return groupMediaSeriesCardsByKeys(items, keys)
}

// groupMediaSeriesCardsByKeys performs the card fold using already-resolved
// keys. Full-library caches need the same keys for the episode index and the
// series-card list; resolving them once avoids several regex-heavy passes over
// every episode row.
func groupMediaSeriesCardsByKeys(items []model.Media, keys []string) []SeriesCard {
	if len(items) == 0 {
		return nil
	}
	if len(keys) != len(items) {
		return groupMediaSeriesCards(items)
	}
	groups := make([]seriesCardGroup, 0)
	byKey := make(map[string]int, len(items))
	for i, item := range items {
		key := keys[i]
		if key == "" {
			continue
		}
		itemAdded := item.CreatedAt
		if itemAdded.IsZero() {
			itemAdded = item.UpdatedAt
		}
		if idx, ok := byKey[key]; ok {
			group := &groups[idx]
			if latest := seriesMediaTime(item); latest.After(group.latest) {
				group.latest = latest
			}
			card := &group.card
			if card.LastAddedAt == nil || (!itemAdded.IsZero() && itemAdded.After(*card.LastAddedAt)) {
				t := itemAdded
				card.LastAddedAt = &t
			}
			// A shared external ID means duplicate encodes/locations for movies,
			// not multiple episodes. Keep a single movie card without presenting
			// its versions as an "N episodes" collection.
			if mediaLooksEpisodicForGrouping(item) || mediaLooksEpisodicForGrouping(card.LinkMedia) {
				card.Count++
			}
			if betterSeriesLinkMedia(item, card.LinkMedia) {
				card.LinkMedia = item
			}
			if betterSeriesRepresentative(item, card.Rep) {
				card.Rep = item
			}
			continue
		}
		var initialLastAdded *time.Time
		if !itemAdded.IsZero() {
			t := itemAdded
			initialLastAdded = &t
		}
		byKey[key] = len(groups)
		groups = append(groups, seriesCardGroup{
			card:   SeriesCard{Key: key, Rep: item, LinkMedia: item, Count: 1, LastAddedAt: initialLastAdded},
			latest: seriesMediaTime(item),
		})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].latest.After(groups[j].latest)
	})
	cards := make([]SeriesCard, 0, len(groups))
	for _, group := range groups {
		cards = append(cards, group.card)
	}
	return cards
}

// GroupMediaSeriesItems folds episode-level rows into one representative media
// row per series. It is intended for search surfaces where applying a small
// limit before series grouping would otherwise return several episodes from
// the same show.
func GroupMediaSeriesItems(items []model.Media) []model.Media {
	cards := groupMediaSeriesCards(items)
	if len(cards) == 0 {
		return []model.Media{}
	}
	out := make([]model.Media, 0, len(cards))
	for _, card := range cards {
		out = append(out, card.Rep)
	}
	return out
}

func betterSeriesRepresentative(candidate, current model.Media) bool {
	// A theatrical feature can have local poster.jpg/background.jpg files and
	// therefore a higher artwork score than its TV episodes. Keep the TV row as
	// the visible identity of a mixed series card so a movie cannot hijack the
	// series title, overview, and artwork.
	candidateTheatrical := mediaLooksLikeTheatricalFeature(&candidate)
	currentTheatrical := mediaLooksLikeTheatricalFeature(&current)
	if candidateTheatrical != currentTheatrical {
		return !candidateTheatrical
	}

	currentArtwork := seriesArtworkScore(candidate)
	representativeArtwork := seriesArtworkScore(current)
	if currentArtwork != representativeArtwork {
		return currentArtwork > representativeArtwork
	}
	cur := candidate.SeasonNum*10000 + candidate.EpisodeNum
	rep := current.SeasonNum*10000 + current.EpisodeNum
	return cur > 0 && (rep == 0 || cur < rep)
}

func seriesMediaTime(media model.Media) time.Time {
	if releaseDate := strings.TrimSpace(media.ReleaseDate); releaseDate != "" {
		if parsed, err := time.Parse("2006-01-02", releaseDate); err == nil {
			return parsed
		}
	}
	if media.Year > 0 {
		return time.Date(media.Year, time.December, 31, 0, 0, 0, 0, time.UTC)
	}
	if media.UpdatedAt.After(media.CreatedAt) {
		return media.UpdatedAt
	}
	return media.CreatedAt
}

func betterSeriesLinkMedia(candidate, current model.Media) bool {
	candidateScore := librarySpecificityScore(candidate)
	currentScore := librarySpecificityScore(current)
	if candidateScore != currentScore {
		return candidateScore > currentScore
	}
	return seriesArtworkScore(candidate) > seriesArtworkScore(current)
}

func librarySpecificityScore(media model.Media) int {
	rawPath := strings.TrimSpace(firstNonEmpty(media.DisplayLibraryPath, media.LibraryPath))
	if rawPath == "" {
		return 0
	}
	normalized := strings.TrimRight(strings.ReplaceAll(rawPath, "\\", "/"), "/")
	lower := strings.ToLower(normalized)
	if strings.HasPrefix(lower, "cloud://") {
		rest := normalized[len("cloud://"):]
		slash := strings.Index(rest, "/")
		if slash < 0 || slash == len(rest)-1 {
			return 0
		}
		return 100 + len(nonEmptySlashParts(rest[slash+1:]))
	}
	return 200 + len(nonEmptySlashParts(normalized))
}

func nonEmptySlashParts(value string) []string {
	parts := strings.Split(value, "/")
	out := parts[:0]
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return out
}

var (
	posterArtworkRE = regexp.MustCompile(`(poster|folder|cover|movie|show|pl)(?:[._-]|\.[a-z0-9]+$|$)`)
	badArtworkRE    = regexp.MustCompile(`(actor|actress|cast|avatar|sample|screenshot|screen|still|scene|fanart|backdrop|background|landscape|banner|logo|disc)`)
)

func seriesArtworkScore(media model.Media) int {
	poster := strings.ToLower(media.PosterURL)
	backdrop := strings.ToLower(media.BackdropURL)
	if poster == "" {
		if backdrop != "" {
			return 5
		}
		return 0
	}
	if posterArtworkRE.MatchString(poster) {
		return 40
	}
	if badArtworkRE.MatchString(poster) {
		return 10
	}
	if strings.Contains(poster, "thumb") {
		return 20
	}
	return 30
}

func minInt64(a int64, b int) int {
	if a <= 0 {
		return 0
	}
	if a > int64(b) {
		return b
	}
	return int(a)
}
