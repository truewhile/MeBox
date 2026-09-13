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
	return q
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
