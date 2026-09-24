package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/truewhile/MeBox/internal/model"
)

// MediaSegmentRepository persists intro/recap/credits/preview ranges.
type MediaSegmentRepository struct{ db *gorm.DB }

// ListByMedia returns every stored segment for a media item, ordered by start.
func (r *MediaSegmentRepository) ListByMedia(ctx context.Context, mediaID string) ([]model.MediaSegment, error) {
	rows := make([]model.MediaSegment, 0, 4)
	err := r.db.WithContext(ctx).
		Where("media_id = ?", mediaID).
		Order("start_ms asc").
		Find(&rows).Error
	return rows, err
}

// ListByMediaSource returns the segments contributed by one source only, ordered
// by start. Used to read the ffprobe-extracted chapters independently of the
// community-database rows, so the player can pick between them.
func (r *MediaSegmentRepository) ListByMediaSource(ctx context.Context, mediaID, source string) ([]model.MediaSegment, error) {
	rows := make([]model.MediaSegment, 0, 4)
	err := r.db.WithContext(ctx).
		Where("media_id = ? AND source = ?", mediaID, source).
		Order("start_ms asc").
		Find(&rows).Error
	return rows, err
}

// ReplaceForMedia swaps the segments contributed by one source in a single
// transaction, so a provider refresh can never leave a half-updated set.
//
// Rows are hard-deleted rather than soft-deleted: the unique index on
// (media_id, kind, start_ms) would otherwise collide with the tombstoned rows
// on the next insert.
func (r *MediaSegmentRepository) ReplaceForMedia(ctx context.Context, mediaID, source string, rows []model.MediaSegment) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().
			Where("media_id = ? AND source = ?", mediaID, source).
			Delete(&model.MediaSegment{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		return tx.Create(&rows).Error
	})
}

// GetFetch returns the fetch ledger row for (media, source), or (nil, nil).
func (r *MediaSegmentRepository) GetFetch(ctx context.Context, mediaID, source string) (*model.MediaSegmentFetch, error) {
	var row model.MediaSegmentFetch
	err := r.db.WithContext(ctx).
		Where("media_id = ? AND source = ?", mediaID, source).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// UpsertFetch records the outcome of a provider lookup. The `deleted_at: nil`
// assignment revives a previously deleted row instead of failing on the unique
// index, mirroring the playback history upsert.
func (r *MediaSegmentRepository) UpsertFetch(ctx context.Context, row *model.MediaSegmentFetch) error {
	onConflict := clause.OnConflict{
		Columns: []clause.Column{{Name: "media_id"}, {Name: "source"}},
		DoUpdates: clause.Assignments(map[string]any{
			"fetched_at": row.FetchedAt,
			"found":      row.Found,
			"deleted_at": nil,
		}),
	}
	return r.db.WithContext(ctx).Clauses(onConflict).Create(row).Error
}

// ListPrewarmCandidates returns media that should be refreshed from source:
// no ledger, expired miss (fetched_at < missBefore), or expired hit (fetched_at < hitBefore).
// Recently played items come first so hot titles recover coverage sooner.
func (r *MediaSegmentRepository) ListPrewarmCandidates(
	ctx context.Context,
	source string,
	missBefore, hitBefore time.Time,
	limit int,
) ([]model.Media, error) {
	if r == nil || limit <= 0 {
		return nil, nil
	}
	rows := make([]model.Media, 0, limit)
	// 可查询：有 TMDb，且是剧集（有季集）或电影（无季集）。
	err := r.db.WithContext(ctx).Raw(`
SELECT m.*
FROM media m
LEFT JOIN media_segment_fetches f
  ON f.media_id = m.id AND f.source = ? AND f.deleted_at IS NULL
LEFT JOIN (
  SELECT media_id, MAX(updated_at) AS last_played
  FROM playback_histories
  WHERE deleted_at IS NULL
  GROUP BY media_id
) ph ON ph.media_id = m.id
WHERE m.deleted_at IS NULL
  AND m.tm_db_id > 0
  AND (
    (m.season_num > 0 AND m.episode_num > 0)
    OR (COALESCE(m.season_num, 0) = 0 AND COALESCE(m.episode_num, 0) = 0)
  )
  AND (
    f.id IS NULL
    OR (f.found = 0 AND f.fetched_at < ?)
    OR (f.found = 1 AND f.fetched_at < ?)
  )
ORDER BY ph.last_played DESC
LIMIT ?
`, source, missBefore, hitBefore, limit).Scan(&rows).Error
	return rows, err
}
