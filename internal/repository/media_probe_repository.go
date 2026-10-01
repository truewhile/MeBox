package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/truewhile/MeBox/internal/model"
)

// MediaProbeRepository 持久化 ffprobe 全量探测的结果缓存。
type MediaProbeRepository struct{ db *gorm.DB }

// Get 返回某媒体的探测缓存，未探测过时返回 (nil, nil)。
func (r *MediaProbeRepository) Get(ctx context.Context, mediaID string) (*model.MediaProbe, error) {
	var row model.MediaProbe
	err := r.db.WithContext(ctx).
		Where("media_id = ?", mediaID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Upsert 写入一次成功的探测结果（含裁剪后的媒体信息）。
func (r *MediaProbeRepository) Upsert(ctx context.Context, row *model.MediaProbe) error {
	onConflict := clause.OnConflict{
		Columns: []clause.Column{{Name: "media_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"signature", "source",
			"container", "duration_sec", "bit_rate",
			"width", "height", "video_codec", "audio_codec",
			"video_streams", "audio_streams", "subtitle_streams", "chapter_count",
			"payload", "probed_at", "last_error",
			"deleted_at",
		}),
	}
	return r.db.WithContext(ctx).Clauses(onConflict).Create(row).Error
}

// MarkFailure 记录一次失败的探测。它只更新「时间 + 错误信息」，刻意不碰
// payload 与其它字段：一次失败的重探不该把上一次成功拿到的媒体信息抹掉。
func (r *MediaProbeRepository) MarkFailure(ctx context.Context, mediaID, message string, probedAt time.Time) error {
	row := model.MediaProbe{MediaID: mediaID, ProbedAt: probedAt, LastError: message}
	onConflict := clause.OnConflict{
		Columns: []clause.Column{{Name: "media_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"probed_at":  probedAt,
			"last_error": message,
			"deleted_at": nil,
		}),
	}
	return r.db.WithContext(ctx).Clauses(onConflict).Create(&row).Error
}
