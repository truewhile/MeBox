package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// MediaRepository persists model.Media records.
type MediaRepository struct {
	db *gorm.DB

	searchIndexOnce      sync.Once
	searchIndexAvailable bool
	searchBackend        MediaSearchBackend
}

type MediaSearchBackend interface {
	SearchMediaIDs(ctx context.Context, query string, offset, limit int, filter MediaQueryFilter) ([]string, int64, error)
}

type MediaSearchSyncBackend interface {
	MediaSearchBackend
	EnsureIndex(ctx context.Context) error
	IndexMedia(ctx context.Context, rows []model.Media) error
}

func (r *MediaRepository) SetSearchBackend(backend MediaSearchBackend) {
	if r != nil {
		r.searchBackend = backend
	}
}

// MediaQueryFilter is applied to user-facing media queries so NSFW items and
// profile-restricted libraries are filtered in SQL instead of only in React.
type MediaQueryFilter struct {
	IncludeNSFW       bool
	AllowedLibraryIDs []string
	HiddenLibraryIDs  []string
	SeriesID          string
	// LibraryID 是精确匹配的单个库过滤，用于库内场景（例如媒体库页筛选）。
	// 它与 AllowedLibraryIDs 是「与」关系：可见性仍由后者兜底，避免越权。
	LibraryID string
	// Genres 是类型多选，之间为「或」。按整词匹配（见 genreMatchClause）。
	Genres []string
	// YearMin / YearMax 为 0 表示该端不限。
	YearMin int
	YearMax int
	// RatingMin 为 0 表示不限。
	RatingMin float64
	// UnwatchedOnly 排除 UnwatchedUserID 已标记看完的条目。
	// 「未观看」定义为「没有 completed=true 的记录」：看到一半的仍会出现，
	// 与「继续观看」互补而不是重复。
	UnwatchedOnly   bool
	UnwatchedUserID string
}

func applyMediaQueryFilter(q *gorm.DB, filter MediaQueryFilter) *gorm.DB {
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
	if seriesID := strings.TrimSpace(filter.SeriesID); seriesID != "" {
		q = q.Where("series_id = ?", seriesID)
	}
	if len(filter.Genres) > 0 {
		q = q.Where(genreMatchClause(filter.Genres), genreMatchArgs(filter.Genres)...)
	}
	if filter.YearMin > 0 {
		q = q.Where("year >= ?", filter.YearMin)
	}
	if filter.YearMax > 0 {
		q = q.Where("year <= ?", filter.YearMax)
	}
	if filter.RatingMin > 0 {
		q = q.Where("rating >= ?", filter.RatingMin)
	}
	if filter.UnwatchedOnly {
		userID := strings.TrimSpace(filter.UnwatchedUserID)
		// 没有用户上下文时忽略该条件：否则会把整个库筛成空，看起来像「坏了」。
		if userID != "" {
			q = q.Where(
				"id NOT IN (SELECT media_id FROM playback_histories WHERE user_id = ? AND completed = ?)",
				userID, true,
			)
		}
	}
	return q
}

// genreMatchClause 生成类型整词匹配条件。
//
// genres 列是逗号分隔字符串，直接 LIKE '%Action%' 会把 "ActionComedy" 也命中。
// 这里统一补上首尾逗号（并用空格容错）后再按 "%,Action,%" 匹配，实现整词语义；
// 该写法在 SQLite 与 PostgreSQL 上行为一致，因此不需要方言分支。
//
// 注意写法：参数本身带上首尾逗号，SQL 里只做一次 REPLACE 来保证列值两端也有
// 分隔符，避免 OR 链里重复拼接列表达式。
func genreMatchClause(genres []string) string {
	clauses := make([]string, 0, len(genres))
	for range genres {
		clauses = append(clauses, "',' || REPLACE(REPLACE(TRIM(genres), ' ', ''), '，', ',') || ',' LIKE ?")
	}
	return "(" + strings.Join(clauses, " OR ") + ")"
}

// genreMatchArgs 生成与 genreMatchClause 对应的参数，形如 "%,Action,%"。
//
// 必须与 genreMatchClause 的列端处理完全对称：
//   - TRIM                   → TrimSpace
//   - REPLACE(…, ' ', '')    → ReplaceAll(…, " ", "")   ← 多词类型（如 "Science Fiction"）
//   - REPLACE(…, '，', ',')  → ReplaceAll(…, "，", ",")
func genreMatchArgs(genres []string) []any {
	args := make([]any, 0, len(genres))
	for _, genre := range genres {
		name := strings.ReplaceAll(strings.TrimSpace(genre), "，", ",")
		name = strings.ReplaceAll(name, " ", "") // mirror REPLACE(…,' ','') in genreMatchClause
		if name == "" {
			name = "\x00" // 空类型不会命中任何行
		}
		args = append(args, "%,"+name+",%")
	}
	return args
}

// ListGenreValues 返回符合过滤条件的 media.genres 原始值（逗号分隔字符串）。
//
// 只取单列：类型聚合不需要整行 media，而一台大库的整行扫描会把海报 URL、
// 简介等大字段一起读进内存。切分与去重交给调用方，SQL 层保持方言无关。
func (r *MediaRepository) ListGenreValues(ctx context.Context, filter MediaQueryFilter) ([]string, error) {
	var values []string
	q := r.db.WithContext(ctx).
		Model(&model.Media{}).
		Where("genres IS NOT NULL AND genres <> ''")
	q = applyMediaQueryFilter(q, filter)
	if err := q.Pluck("genres", &values).Error; err != nil {
		return nil, err
	}
	return values, nil
}

// YearRange 返回符合过滤条件的年份区间（两端都为 0 表示没有可用年份）。
// 供媒体库筛选面板生成年份上下限，避免前端硬编码或先取全量再自己算。
func (r *MediaRepository) YearRange(ctx context.Context, filter MediaQueryFilter) (int, int, error) {
	var bounds struct {
		MinYear *int
		MaxYear *int
	}
	q := r.db.WithContext(ctx).
		Model(&model.Media{}).
		Where("year > 0").
		Select("MIN(year) AS min_year, MAX(year) AS max_year")
	q = applyMediaQueryFilter(q, filter)
	if err := q.Scan(&bounds).Error; err != nil {
		return 0, 0, err
	}
	min, max := 0, 0
	if bounds.MinYear != nil {
		min = *bounds.MinYear
	}
	if bounds.MaxYear != nil {
		max = *bounds.MaxYear
	}
	return min, max, nil
}

func (r *MediaRepository) indexMediaBestEffort(ctx context.Context, media model.Media) {
	backend, ok := r.searchBackend.(MediaSearchSyncBackend)
	if !ok {
		return
	}
	_ = backend.IndexMedia(ctx, []model.Media{media})
}

// FindByID returns the media row or (nil, nil).
func (r *MediaRepository) FindByID(ctx context.Context, id string) (*model.Media, error) {
	var m model.Media
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ExistsSiblingWithTMDbID reports whether another row of the same show carries
// the same tm_db_id as m.
//
// 它的用途是把「剧集级 id」和「单集自己的 id」区分开：一部剧的多集共用一个
// 剧集级 id，而单集各自的 id 不会重复。调用方据此决定能否把 Media.TMDbID
// 当作 Series.TMDbID 的替代品（见 MediaSegmentService.queryIDs）。
//
// 同一部剧的判定优先用 series_id；没有 series_id 的行（部分刮削路径不写它）
// 退回到「同一个库 + 同一个标题」。查询失败按「不共用」处理：宁可不查，
// 也不能拿一个可能是单集的 id 去查错片。
func (r *MediaRepository) ExistsSiblingWithTMDbID(ctx context.Context, m *model.Media) bool {
	if r == nil || m == nil || m.TMDbID <= 0 || m.ID == "" {
		return false
	}
	query := r.db.WithContext(ctx).Model(&model.Media{}).
		Where("tm_db_id = ? AND id <> ?", m.TMDbID, m.ID)
	if seriesID := strings.TrimSpace(m.SeriesID); seriesID != "" {
		query = query.Where("series_id = ?", seriesID)
	} else {
		libraryID := strings.TrimSpace(m.LibraryID)
		title := strings.TrimSpace(m.Title)
		if libraryID == "" || title == "" {
			return false
		}
		query = query.Where("library_id = ? AND title = ?", libraryID, title)
	}
	var count int64
	if err := query.Limit(1).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

// ListSeasonSiblings returns other episodes in the same season as m.
// Prefers series_id; falls back to shared library+title+tm_db_id (anime scrape path).
func (r *MediaRepository) ListSeasonSiblings(ctx context.Context, m *model.Media) ([]model.Media, error) {
	if r == nil || m == nil || m.SeasonNum <= 0 || m.ID == "" {
		return nil, nil
	}
	query := r.db.WithContext(ctx).Model(&model.Media{}).
		Where("season_num = ? AND id <> ?", m.SeasonNum, m.ID)
	if seriesID := strings.TrimSpace(m.SeriesID); seriesID != "" {
		query = query.Where("series_id = ?", seriesID)
	} else {
		libraryID := strings.TrimSpace(m.LibraryID)
		title := strings.TrimSpace(m.Title)
		if libraryID == "" || title == "" || m.TMDbID <= 0 {
			return nil, nil
		}
		query = query.Where("library_id = ? AND title = ? AND tm_db_id = ?", libraryID, title, m.TMDbID)
	}
	var rows []model.Media
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListByLibrary returns paginated media items for a library.
func (r *MediaRepository) ListByLibrary(ctx context.Context, libraryID string, offset, limit int) ([]model.Media, int64, error) {
	return r.ListByLibraryFiltered(ctx, libraryID, offset, limit, MediaQueryFilter{IncludeNSFW: true})
}

func (r *MediaRepository) ListByLibraryFiltered(ctx context.Context, libraryID string, offset, limit int, filter MediaQueryFilter) ([]model.Media, int64, error) {
	return r.ListByLibrariesFiltered(ctx, []string{libraryID}, offset, limit, filter)
}

func (r *MediaRepository) ListByLibrariesFiltered(ctx context.Context, libraryIDs []string, offset, limit int, filter MediaQueryFilter) ([]model.Media, int64, error) {
	items, total, err := r.listByLibrariesFiltered(ctx, libraryIDs, offset, limit, filter, true)
	return items, total, err
}

// ListByLibrariesFilteredNoCount skips the COUNT query when the caller already
// knows totals or only needs a bounded slice (e.g. home-page previews).
func (r *MediaRepository) ListByLibrariesFilteredNoCount(ctx context.Context, libraryIDs []string, offset, limit int, filter MediaQueryFilter) ([]model.Media, error) {
	items, _, err := r.listByLibrariesFiltered(ctx, libraryIDs, offset, limit, filter, false)
	return items, err
}

// ListAllByLibrariesFilteredNoCount loads every matching row without issuing a
// COUNT query. Full-library consumers such as series grouping must scan the
// whole library anyway, so calling it once is both cheaper and more consistent
// than issuing paginated queries with repeated counts.
func (r *MediaRepository) ListAllByLibrariesFilteredNoCount(ctx context.Context, libraryIDs []string, filter MediaQueryFilter) ([]model.Media, error) {
	items := make([]model.Media, 0)
	if len(libraryIDs) == 0 {
		return items, nil
	}
	q := r.db.WithContext(ctx).Model(&model.Media{})
	if len(libraryIDs) == 1 {
		q = q.Where("library_id = ?", libraryIDs[0])
	} else {
		q = q.Where("library_id IN ?", libraryIDs)
	}
	q = applyMediaQueryFilter(q, filter)
	err := q.Order("release_date DESC, year DESC, updated_at DESC, created_at DESC, id DESC").Find(&items).Error
	return items, err
}

// ListVersionCandidates loads a bounded candidate set for version grouping
// using the strongest identity stored on the row. Returning ok=false keeps the
// caller's full-library fallback for rows without external IDs or SeriesID.
func (r *MediaRepository) ListVersionCandidates(ctx context.Context, libraryIDs []string, media model.Media, limit int) ([]model.Media, bool, error) {
	items := make([]model.Media, 0)
	if len(libraryIDs) == 0 {
		return items, false, nil
	}
	if limit <= 0 {
		limit = 5000
	}
	q := r.db.WithContext(ctx).Model(&model.Media{})
	if len(libraryIDs) == 1 {
		q = q.Where("library_id = ?", libraryIDs[0])
	} else {
		q = q.Where("library_id IN ?", libraryIDs)
	}
	found := true
	switch {
	case strings.TrimSpace(media.SeriesID) != "":
		q = q.Where("series_id = ?", strings.TrimSpace(media.SeriesID))
	case media.TMDbID > 0:
		q = q.Where("tm_db_id = ?", media.TMDbID)
	case media.BangumiID > 0:
		q = q.Where("bangumi_id = ?", media.BangumiID)
	case strings.TrimSpace(media.DoubanID) != "":
		q = q.Where("douban_id = ?", strings.TrimSpace(media.DoubanID))
	case strings.TrimSpace(media.TheTVDBID) != "":
		q = q.Where("thetvdb_id = ?", strings.TrimSpace(media.TheTVDBID))
	default:
		found = false
	}
	if !found {
		return items, false, nil
	}
	err := q.Order("release_date DESC, year DESC, updated_at DESC, created_at DESC, id DESC").
		Limit(limit).
		Find(&items).Error
	return items, true, err
}

func (r *MediaRepository) listByLibrariesFiltered(ctx context.Context, libraryIDs []string, offset, limit int, filter MediaQueryFilter, withCount bool) ([]model.Media, int64, error) {
	var items []model.Media
	var total int64
	if len(libraryIDs) == 0 {
		return items, 0, nil
	}
	q := r.db.WithContext(ctx).Model(&model.Media{})
	if len(libraryIDs) == 1 {
		q = q.Where("library_id = ?", libraryIDs[0])
	} else {
		q = q.Where("library_id IN ?", libraryIDs)
	}
	q = applyMediaQueryFilter(q, filter)
	if withCount {
		if err := q.Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}
	// 多级排序消除"随机"观感:
	//  1. release_date desc — 精确上映/首播日期新→旧
	//  2. year desc         — 老数据没有完整日期时仍按年份新→旧
	//  3. updated_at desc   — 同日期/同年按最近更新兜底
	//  4. created_at desc   — 再按入库时间
	//  5. id desc           — 稳定 tie-breaker:云盘批量扫描同批 created_at 相同时,
	//                        没有它 DB 返回顺序不确定,正是"随机排序"的根因。
	err := q.Order("release_date DESC, year DESC, updated_at DESC, created_at DESC, id DESC").
		Offset(offset).Limit(limit).Find(&items).Error
	return items, total, err
}

type rankedMediaRow struct {
	model.Media
	MmtlRN int `gorm:"column:mebox_rn"`
}

// ListRecentByLibraries returns up to perLibrary recent items for each library
// in a single query using a window function (avoids N+1 on home preview).
func (r *MediaRepository) ListRecentByLibraries(ctx context.Context, libraryIDs []string, perLibrary int, filter MediaQueryFilter) (map[string][]model.Media, error) {
	out := make(map[string][]model.Media, len(libraryIDs))
	if len(libraryIDs) == 0 || perLibrary <= 0 {
		return out, nil
	}

	var libClause string
	var args []interface{}
	if len(libraryIDs) == 1 {
		libClause = "library_id = ?"
		args = append(args, libraryIDs[0])
	} else {
		libClause = "library_id IN ?"
		args = append(args, libraryIDs)
	}
	where := "deleted_at IS NULL AND " + libClause
	if filterSQL, filterArgs := mediaQueryFilterSQL(filter); filterSQL != "" {
		where += " AND " + filterSQL
		args = append(args, filterArgs...)
	}
	args = append(args, perLibrary)

	sql := fmt.Sprintf(`
		SELECT * FROM (
			SELECT *, ROW_NUMBER() OVER (
				PARTITION BY library_id
				ORDER BY release_date DESC, year DESC, updated_at DESC, created_at DESC, id DESC
			) AS mebox_rn
			FROM media
			WHERE %s
		) ranked
		WHERE mebox_rn <= ?
	`, where)

	var rows []rankedMediaRow
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.LibraryID] = append(out[row.LibraryID], row.Media)
	}
	return out, nil
}

func mediaQueryFilterSQL(filter MediaQueryFilter) (string, []interface{}) {
	var parts []string
	var args []interface{}
	if !filter.IncludeNSFW {
		parts = append(parts, "nsfw = ?")
		args = append(args, false)
	}
	if len(filter.HiddenLibraryIDs) > 0 {
		parts = append(parts, "library_id NOT IN ?")
		args = append(args, filter.HiddenLibraryIDs)
	}
	if len(filter.AllowedLibraryIDs) > 0 {
		parts = append(parts, "library_id IN ?")
		args = append(args, filter.AllowedLibraryIDs)
	}
	if seriesID := strings.TrimSpace(filter.SeriesID); seriesID != "" {
		parts = append(parts, "series_id = ?")
		args = append(args, seriesID)
	}
	return strings.Join(parts, " AND "), args
}

type libraryCountRow struct {
	LibraryID string `gorm:"column:library_id"`
	Total     int64  `gorm:"column:total"`
}

// CountByLibraries returns a map of library_id -> total media count for the given library IDs.
func (r *MediaRepository) CountByLibraries(ctx context.Context, libraryIDs []string, filter MediaQueryFilter) (map[string]int64, error) {
	out := make(map[string]int64, len(libraryIDs))
	if len(libraryIDs) == 0 {
		return out, nil
	}
	var rows []libraryCountRow
	q := r.db.WithContext(ctx).Model(&model.Media{}).
		Select("library_id, count(*) as total")
	if len(libraryIDs) == 1 {
		q = q.Where("library_id = ?", libraryIDs[0])
	} else {
		q = q.Where("library_id IN ?", libraryIDs)
	}
	q = applyMediaQueryFilter(q, filter)
	if err := q.Group("library_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.LibraryID] = row.Total
	}
	return out, nil
}

// DeleteByLibrary purges all media tied to a library.
func (r *MediaRepository) DeleteByLibrary(ctx context.Context, libraryID string) error {
	// FTS 行由 media 表上的触发器同步清理（物理删除触发 FTS 清理）。
	return r.db.WithContext(ctx).Unscoped().Where("library_id = ?", libraryID).Delete(&model.Media{}).Error
}

func (r *MediaRepository) DeleteByLibraryRoot(ctx context.Context, libraryID, rootID string) error {
	return r.db.WithContext(ctx).Unscoped().
		Where("library_id = ? AND library_root_id = ?", libraryID, rootID).
		Delete(&model.Media{}).Error
}

// PurgeByLibrary permanently removes media tied to a library. Used when
// removing a library or virtual mount so indexed rows are dropped immediately.
func (r *MediaRepository) PurgeByLibrary(ctx context.Context, libraryID string) error {
	return r.db.WithContext(ctx).Unscoped().Where("library_id = ?", libraryID).Delete(&model.Media{}).Error
}
