package repository

import (
	"context"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/truewhile/MeBox/internal/model"
)

// HistoryRepository persists model.PlaybackHistory entries. The application
// upserts on (UserID, MediaID) so resume always reads the latest position.
type HistoryRepository struct{ db *gorm.DB }

// Upsert atomically inserts/updates the resume position in a single statement.
// Callers without playback-session metadata keep the legacy last-write-wins
// semantics (for example an explicit "mark played" action).
func (r *HistoryRepository) Upsert(ctx context.Context, h *model.PlaybackHistory) error {
	return r.upsert(ctx, h, false)
}

// UpsertProgress writes a progress report only when it is newer than the row
// currently stored. Reports from an older playback session, or an older
// sequence in the same session, are ignored. Reports without session metadata
// fall back to Upsert for compatibility with older clients.
func (r *HistoryRepository) UpsertProgress(ctx context.Context, h *model.PlaybackHistory) error {
	h.SessionID = strings.TrimSpace(h.SessionID)
	if h.SessionID == "" || h.SessionStartedAtMs <= 0 {
		return r.Upsert(ctx, h)
	}
	return r.upsert(ctx, h, true)
}

func (r *HistoryRepository) upsert(ctx context.Context, h *model.PlaybackHistory, versioned bool) error {
	updates := playbackHistoryAssignments(h)
	if versioned {
		updates["session_id"] = h.SessionID
		updates["session_started_at_ms"] = h.SessionStartedAtMs
		updates["sequence"] = h.Sequence
	}

	onConflict := clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "media_id"}},
		DoUpdates: clause.Assignments(updates),
	}
	if versioned {
		onConflict.Where = clause.Where{Exprs: []clause.Expression{
			clause.Or(
				clause.Lt{
					Column: clause.Column{Name: "session_started_at_ms"},
					Value:  h.SessionStartedAtMs,
				},
				clause.And(
					clause.Eq{
						Column: clause.Column{Name: "session_started_at_ms"},
						Value:  h.SessionStartedAtMs,
					},
					clause.Lte{
						Column: clause.Column{Name: "sequence"},
						Value:  h.Sequence,
					},
				),
			),
		}}
	}
	return r.db.WithContext(ctx).Clauses(onConflict).Create(h).Error
}

func playbackHistoryAssignments(h *model.PlaybackHistory) map[string]any {
	return map[string]any{
		"position_ms": h.PositionMs,
		// 沿用旧语义：未知时长（0）不覆盖已记录的时长。
		"duration_ms": gorm.Expr(
			"CASE WHEN ? > 0 THEN ? ELSE playback_histories.duration_ms END",
			h.DurationMs, h.DurationMs,
		),
		"watched_at": h.WatchedAt,
		"completed":  h.Completed,
		"deleted_at": nil,
	}
}

// ListByUser returns the most recent history rows for the user.
func (r *HistoryRepository) ListByUser(ctx context.Context, userID string, limit int) ([]model.PlaybackHistory, error) {
	var rows []model.PlaybackHistory
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).
		Order("watched_at desc").Limit(limit).Find(&rows).Error
	return rows, err
}
