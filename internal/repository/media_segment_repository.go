package repository

import (
	"context"
	"errors"

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
