package service

import (
	"context"
	"sort"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// MediaDiscoveryService 提供「发现类」查询：类型聚合、下一集、相似内容。
//
// 它只负责候选集的选取，不产出 Emby DTO —— DTO 形状必须由 EmbyService 统一
// 提供，否则同一部剧在 /Items 与 /Shows/NextUp 上会长得不一样。同理，这里
// 只接受调用方传入的 MediaVisibility，不自己解析用户权限。
type MediaDiscoveryService struct {
	log  *zap.Logger
	repo *repository.Container
}

// GenreCount 是类型聚合结果。Name 保留首次出现时的写法（大小写与全半角
// 均按原样展示），计数则不区分大小写。
type GenreCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// NewMediaDiscoveryService 构建发现服务。
func NewMediaDiscoveryService(log *zap.Logger, repo *repository.Container) *MediaDiscoveryService {
	return &MediaDiscoveryService{log: log, repo: repo}
}

// mediaFilterFromVisibility 把可见性翻译成仓储过滤条件。所有发现类查询都必须
// 经过这里，避免某一处忘记过滤 NSFW 或受限媒体库。
func mediaFilterFromVisibility(visibility MediaVisibility) repository.MediaQueryFilter {
	return repository.MediaQueryFilter{
		IncludeNSFW:       visibility.IncludeNSFW,
		AllowedLibraryIDs: visibility.AllowedLibraryIDs,
		HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
	}
}

// AggregateGenres 统计可见媒体的类型分布。libraryID 非空时只统计该库，
// 供媒体库页的筛选项与 Emby /Genres 共用。
func (s *MediaDiscoveryService) AggregateGenres(ctx context.Context, visibility MediaVisibility, libraryID string) ([]GenreCount, error) {
	if s == nil || s.repo == nil || s.repo.Media == nil {
		return nil, nil
	}
	filter := mediaFilterFromVisibility(visibility)
	filter.LibraryID = strings.TrimSpace(libraryID)

	raw, err := s.repo.Media.ListGenreValues(ctx, filter)
	if err != nil {
		return nil, err
	}
	return countGenres(raw), nil
}

// countGenres 切分并计数。大小写不同的同名类型合并计数，展示名取首次出现的
// 写法；结果按 count 降序、同数按名称升序，保证输出稳定可测。
func countGenres(values []string) []GenreCount {
	counts := make(map[string]int)
	display := make(map[string]string)
	for _, value := range values {
		for _, name := range SplitGenreList(value) {
			key := strings.ToLower(name)
			if _, seen := display[key]; !seen {
				display[key] = name
			}
			counts[key]++
		}
	}
	out := make([]GenreCount, 0, len(counts))
	for key, count := range counts {
		out = append(out, GenreCount{Name: display[key], Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// SplitGenreList 切分逗号分隔的类型字段。
//
// 刮削来源既有英文逗号也有中文全角逗号，且常见首尾空格，因此三种分隔符都要
// 处理，并丢弃空段。
func SplitGenreList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == '、' || r == ';' || r == '；'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// genreSet 把类型字段转成小写集合，用于相似度计算。
func genreSet(value string) map[string]struct{} {
	names := SplitGenreList(value)
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		out[strings.ToLower(name)] = struct{}{}
	}
	return out
}

// genreOverlap 返回两个类型集合的交集大小。
func genreOverlap(a, b map[string]struct{}) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// 遍历较小的集合，减少比较次数。
	if len(b) < len(a) {
		a, b = b, a
	}
	count := 0
	for name := range a {
		if _, ok := b[name]; ok {
			count++
		}
	}
	return count
}

// mediaIsEpisode 判断一行 media 是否属于「剧集」维度。
//
// 与 Emby 的判定保持一致（季号或集号大于 0），但不依赖 library type：同一个
// 库既可能放电影也可能放剧集，用编号判断更贴近实际数据。
func mediaIsEpisode(m *model.Media) bool {
	return m != nil && (m.SeasonNum > 0 || m.EpisodeNum > 0)
}

// seriesGroupKey 是「同一部剧」的归并键。SeriesID 优先；缺省时退回
// (库, 标题)，这样未刮削的剧集也能归到一组而不是每条历史各算一部剧。
func seriesGroupKey(m *model.Media) string {
	if m == nil {
		return ""
	}
	if key := strings.TrimSpace(m.SeriesID); key != "" {
		return "sid:" + key
	}
	if !mediaIsEpisode(m) {
		return ""
	}
	return "lib:" + m.LibraryID + "|title:" + strings.ToLower(strings.TrimSpace(m.Title))
}

// nextUpHistoryScanLimit 是扫描播放历史的上限。历史按最近观看倒序取，
// 因此截断只会丢掉「很久没看且排在很后面」的剧，不会影响首页前排。
const nextUpHistoryScanLimit = 100

// NextUpCandidates 返回「每部在看的剧的下一个待看集」，按最近观看时间排序。
//
// 语义要点：
//   - 只处理剧集，电影由 Resume 接口负责，避免两个接口内容重复。
//   - 同一部剧最多一条：取最近看过的集合之后、编号最小的那集。
//   - 已标记看完的集跳过；追到最后一集则该剧不出现在结果里。
func (s *MediaDiscoveryService) NextUpCandidates(ctx context.Context, userID string, limit int, visibility MediaVisibility) ([]model.Media, error) {
	if s == nil || s.repo == nil || s.repo.Media == nil {
		return nil, nil
	}
	if strings.TrimSpace(userID) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	// Bug 1 fix: include completed histories as anchors so a finished episode
	// still anchors its series and the next unwatched episode is picked.
	var histories []model.PlaybackHistory
	if err := s.repo.DB.WithContext(ctx).
		Where("user_id = ? AND position_ms > 0", userID).
		Order("watched_at desc").
		Limit(nextUpHistoryScanLimit).
		Find(&histories).Error; err != nil {
		return nil, err
	}
	if len(histories) == 0 {
		return nil, nil
	}

	mediaIDs := make([]string, 0, len(histories))
	for _, h := range histories {
		mediaIDs = append(mediaIDs, h.MediaID)
	}
	filter := mediaFilterFromVisibility(visibility)
	var watchedRows []model.Media
	q := s.repo.DB.WithContext(ctx).Where("id IN ?", mediaIDs)
	q = applyDiscoveryVisibility(q, filter)
	if err := q.Find(&watchedRows).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]*model.Media, len(watchedRows))
	for i := range watchedRows {
		byID[watchedRows[i].ID] = &watchedRows[i]
	}

	// 按最近观看顺序归并到「剧」维度，同时记住该剧最近看的那一集以及它是否看完。
	type seriesState struct {
		key       string
		current   *model.Media
		completed bool
	}
	states := make([]seriesState, 0, len(histories))
	seen := make(map[string]bool, len(histories))
	for _, h := range histories {
		m := byID[h.MediaID]
		if m == nil || !mediaIsEpisode(m) {
			continue
		}
		key := seriesGroupKey(m)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		states = append(states, seriesState{key: key, current: m, completed: h.Completed})
	}
	if len(states) == 0 {
		return nil, nil
	}

	// 一次性把涉及的剧集全部取回，避免按剧逐条查询。
	//
	// Bug 4 fix: group by library_id when fetching by series_id so episodes
	// from a different library with the same series_id don't bleed in.
	// Bug 5 fix: batch unscraped (library_id, title) lookups per library
	// instead of one query per series.
	byLibSeries := make(map[string][]string) // libID -> []seriesID
	var fallback []seriesState
	for _, st := range states {
		if id := strings.TrimSpace(st.current.SeriesID); id != "" {
			byLibSeries[st.current.LibraryID] = append(byLibSeries[st.current.LibraryID], id)
		} else {
			fallback = append(fallback, st)
		}
	}

	episodes := make([]model.Media, 0, len(states)*8)
	// 按库批量加载刮削剧集，避免跨库混入同名 series_id 的剧集。
	for libID, sids := range byLibSeries {
		var rows []model.Media
		eq := s.repo.DB.WithContext(ctx).Where("series_id IN ? AND library_id = ?", sids, libID)
		eq = applyDiscoveryVisibility(eq, filter)
		if err := eq.Find(&rows).Error; err != nil {
			return nil, err
		}
		episodes = append(episodes, rows...)
	}
	// 未刮削剧集按 (库, 标题) 批量兜底查询，每库一条 SQL 避免 N+1。
	byLibTitles := make(map[string][]string) // libID -> []title
	for _, st := range fallback {
		byLibTitles[st.current.LibraryID] = append(byLibTitles[st.current.LibraryID], st.current.Title)
	}
	for libID, titles := range byLibTitles {
		var rows []model.Media
		fq := s.repo.DB.WithContext(ctx).Where("library_id = ? AND title IN ?", libID, titles)
		fq = applyDiscoveryVisibility(fq, filter)
		if err := fq.Find(&rows).Error; err != nil {
			return nil, err
		}
		episodes = append(episodes, rows...)
	}

	// 按剧归并候选集，便于 O(1) 查找下一集。
	bySeries := make(map[string][]model.Media, len(states))
	for _, row := range episodes {
		key := seriesGroupKey(&row)
		if key == "" {
			continue
		}
		bySeries[key] = append(bySeries[key], row)
	}
	completed := s.completedMediaIDs(ctx, userID, episodes)

	out := make([]model.Media, 0, limit)
	for _, st := range states {
		if len(out) >= limit {
			break
		}
		next, ok := pickNextEpisode(bySeries[st.key], st.current, st.completed, completed)
		if !ok {
			continue
		}
		out = append(out, next)
	}
	return out, nil
}

// applyDiscoveryVisibility 把可见性过滤应用到查询上。
func applyDiscoveryVisibility(q *gorm.DB, filter repository.MediaQueryFilter) *gorm.DB {
	if !filter.IncludeNSFW {
		q = q.Where("nsfw = ?", false)
	}
	if len(filter.HiddenLibraryIDs) > 0 {
		q = q.Where("library_id NOT IN ?", filter.HiddenLibraryIDs)
	}
	if len(filter.AllowedLibraryIDs) > 0 {
		q = q.Where("library_id IN ?", filter.AllowedLibraryIDs)
	}
	if libraryID := strings.TrimSpace(filter.LibraryID); libraryID != "" {
		q = q.Where("library_id = ?", libraryID)
	}
	return q
}

// completedMediaIDs 找出这些候选里该用户已标记看完的集。
func (s *MediaDiscoveryService) completedMediaIDs(ctx context.Context, userID string, rows []model.Media) map[string]bool {
	out := make(map[string]bool)
	if len(rows) == 0 {
		return out
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	var done []model.PlaybackHistory
	if err := s.repo.DB.WithContext(ctx).
		Where("user_id = ? AND completed = ? AND media_id IN ?", userID, true, ids).
		Find(&done).Error; err != nil {
		return out
	}
	for _, h := range done {
		out[h.MediaID] = true
	}
	return out
}

// pickNextEpisode 选出这部剧「接下来该看的那一集」。
//
// anchor 是这部剧最近一次播放的那一集，anchorCompleted 表示那一集是否已看完：
//   - 没看完（只播了几秒就退出、或中途暂停）时，接下来该看的仍是这一集本身。
//     否则详情页的「继续播放」会直接跳到下一集，用户刚看的那一集被静默跳过。
//   - 已看完时，才在候选集里取严格晚于它的、编号最小的一集；比较顺序为
//     (季, 集)，因此跨季时自然落到下一季第一集。
func pickNextEpisode(candidates []model.Media, anchor *model.Media, anchorCompleted bool, completed map[string]bool) (model.Media, bool) {
	if anchor == nil {
		return model.Media{}, false
	}
	if !anchorCompleted {
		return *anchor, true
	}
	var best model.Media
	found := false
	for _, candidate := range candidates {
		if candidate.ID == anchor.ID || completed[candidate.ID] {
			continue
		}
		if !episodeAfter(candidate, *anchor) {
			continue
		}
		if !found || episodeBefore(candidate, best) {
			best = candidate
			found = true
		}
	}
	return best, found
}

// episodeAfter 报告 a 是否严格晚于 b。
func episodeAfter(a, b model.Media) bool {
	if a.SeasonNum != b.SeasonNum {
		return a.SeasonNum > b.SeasonNum
	}
	return a.EpisodeNum > b.EpisodeNum
}

// episodeBefore 报告 a 是否严格早于 b。
func episodeBefore(a, b model.Media) bool {
	if a.SeasonNum != b.SeasonNum {
		return a.SeasonNum < b.SeasonNum
	}
	return a.EpisodeNum < b.EpisodeNum
}

// similarCandidateLimit 是每个来源池（同库 / 同类型其他库）的候选上限。
//
// 相似度需要在内存里按类型/年份/评分算分，因此不能把整库拉出来；按评分倒序
// 取前 N 条是「好的片子更可能被推荐」与「查询有界」之间的折中。
const similarCandidateLimit = 400

// SimilarCandidates 返回与源条目相似的本地媒体。
//
// 打分口径（不依赖任何外部 API，离线可用）：
//   - 类型重合数 × 10：最强信号，同类内容通常才谈得上相似；
//   - 年份接近度：相差 5 年内给分，差得越远越低；
//   - 评分接近度：同为高分片算加分，避免「8 分片旁边推 3 分片」。
//
// 同库优先；不足时才从同类型的其它可见库里补齐。同剧其它集与自身一律排除。
func (s *MediaDiscoveryService) SimilarCandidates(ctx context.Context, mediaID string, limit int, visibility MediaVisibility) ([]model.Media, error) {
	if s == nil || s.repo == nil || s.repo.Media == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 12
	}
	source, err := s.repo.Media.FindByID(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	if source == nil || !visibility.Allows(source) {
		return nil, nil
	}

	filter := mediaFilterFromVisibility(visibility)

	pool, err := s.similarPool(ctx, filter, source, false)
	if err != nil {
		return nil, err
	}
	if len(pool) < limit {
		// 同库不够时再扩到同类型库，保持「电影配电影、剧集配剧集」的直觉。
		more, err := s.similarPool(ctx, filter, source, true)
		if err != nil {
			return nil, err
		}
		pool = append(pool, more...)
	}

	return rankSimilar(source, pool, limit), nil
}

// similarPool 取一批候选。expand=true 时排除源所在的库（用于补齐阶段），
// 否则只取源所在的库（首选阶段）。
func (s *MediaDiscoveryService) similarPool(ctx context.Context, filter repository.MediaQueryFilter, source *model.Media, expand bool) ([]model.Media, error) {
	q := s.repo.DB.WithContext(ctx).Model(&model.Media{})
	q = applyDiscoveryVisibility(q, filter)
	q = q.Where("id <> ?", source.ID)

	libraryIDs := []string{source.LibraryID}
	if expand {
		ids, err := s.compatibleLibraryIDs(ctx, source)
		if err != nil {
			return nil, err
		}
		filtered := make([]string, 0, len(ids))
		for _, id := range ids {
			if id != source.LibraryID {
				filtered = append(filtered, id)
			}
		}
		if len(filtered) == 0 {
			return nil, nil
		}
		libraryIDs = filtered
	}
	q = q.Where("library_id IN ?", libraryIDs)

	// 电影与剧集不互相推荐：用集号判定，和 NextUp 保持同一套口径。
	if mediaIsEpisode(source) {
		q = q.Where("(season_num > 0 OR episode_num > 0)")
	} else {
		q = q.Where("season_num = 0 AND episode_num = 0")
	}

	var rows []model.Media
	if err := q.Order("rating desc, updated_at desc").Limit(similarCandidateLimit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// compatibleLibraryIDs 返回与源条目同类型的库 ID（可能包含源库自身）。
// 查不到类型时退回源库，保证补齐阶段不会跨类型乱推。
func (s *MediaDiscoveryService) compatibleLibraryIDs(ctx context.Context, source *model.Media) ([]string, error) {
	if s.repo.Library == nil {
		return []string{source.LibraryID}, nil
	}
	libs, err := s.repo.Library.List(ctx)
	if err != nil {
		return nil, err
	}
	sourceType := ""
	for _, lib := range libs {
		if lib.ID == source.LibraryID {
			sourceType = strings.ToLower(strings.TrimSpace(lib.Type))
			break
		}
	}
	if sourceType == "" {
		return []string{source.LibraryID}, nil
	}
	out := make([]string, 0, len(libs))
	for _, lib := range libs {
		if strings.ToLower(strings.TrimSpace(lib.Type)) == sourceType {
			out = append(out, lib.ID)
		}
	}
	return out, nil
}

// rankSimilar 按相似度排序并截断。
func rankSimilar(source *model.Media, pool []model.Media, limit int) []model.Media {
	if len(pool) == 0 {
		return nil
	}
	sourceGenres := genreSet(source.Genres)
	sourceKey := seriesGroupKey(source)

	type scored struct {
		media model.Media
		score float64
	}
	scoredRows := make([]scored, 0, len(pool))
	seen := make(map[string]bool, len(pool))
	for _, candidate := range pool {
		if candidate.ID == source.ID || seen[candidate.ID] {
			continue
		}
		// 同剧其它集不参与：「相似」不是在推荐本剧的下一集。
		if key := seriesGroupKey(&candidate); key != "" && key == sourceKey {
			continue
		}
		seen[candidate.ID] = true
		scoredRows = append(scoredRows, scored{
			media: candidate,
			score: similarScore(source, &candidate, sourceGenres),
		})
	}
	sort.SliceStable(scoredRows, func(i, j int) bool {
		if scoredRows[i].score != scoredRows[j].score {
			return scoredRows[i].score > scoredRows[j].score
		}
		if scoredRows[i].media.Rating != scoredRows[j].media.Rating {
			return scoredRows[i].media.Rating > scoredRows[j].media.Rating
		}
		return scoredRows[i].media.Title < scoredRows[j].media.Title
	})

	if len(scoredRows) > limit {
		scoredRows = scoredRows[:limit]
	}
	out := make([]model.Media, 0, len(scoredRows))
	for _, row := range scoredRows {
		out = append(out, row.media)
	}
	return out
}

// similarScore 计算单个候选的相似度。
func similarScore(source, candidate *model.Media, sourceGenres map[string]struct{}) float64 {
	score := float64(genreOverlap(sourceGenres, genreSet(candidate.Genres))) * 10

	if source.Year > 0 && candidate.Year > 0 {
		diff := source.Year - candidate.Year
		if diff < 0 {
			diff = -diff
		}
		if diff <= 5 {
			score += float64(5 - diff)
		}
	}

	if source.Rating > 0 && candidate.Rating > 0 {
		diff := float64(source.Rating - candidate.Rating)
		if diff < 0 {
			diff = -diff
		}
		// 评分差 2 分以内才给分，最多 3 分。
		if diff < 2 {
			score += 3 * (2 - diff) / 2
		}
	}

	return score
}
