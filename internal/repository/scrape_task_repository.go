package repository

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

var scrapeClaimMu sync.Mutex

// ScrapeTaskRepository persists model.ScrapeTask.
type ScrapeTaskRepository struct{ db *gorm.DB }

func (r *ScrapeTaskRepository) Create(ctx context.Context, t *model.ScrapeTask) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Create(t).Error
	})
}

func (r *ScrapeTaskRepository) CreateBatch(ctx context.Context, tasks []model.ScrapeTask) error {
	if len(tasks) == 0 {
		return nil
	}
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).CreateInBatches(tasks, 100).Error
	})
}

func (r *ScrapeTaskRepository) FindByID(ctx context.Context, id string) (*model.ScrapeTask, error) {
	var t model.ScrapeTask
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &t, err
}

func (r *ScrapeTaskRepository) FindActiveByMediaID(ctx context.Context, mediaID string) (*model.ScrapeTask, error) {
	var t model.ScrapeTask
	err := r.db.WithContext(ctx).
		Where("media_id = ? AND status IN ?", mediaID, []string{model.ScrapeTaskPending, model.ScrapeTaskRunning}).
		First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &t, err
}

// FindActiveByMediaIDs 批量查询仍处于 pending/running 的任务媒体 ID 集合，
// 供整库入队时去重（防止同一媒体被重复入队并被并发双刮）。
func (r *ScrapeTaskRepository) FindActiveByMediaIDs(ctx context.Context, mediaIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(mediaIDs))
	if len(mediaIDs) == 0 {
		return out, nil
	}
	var rows []model.ScrapeTask
	err := r.db.WithContext(ctx).
		Select("media_id").
		Where("media_id IN ? AND status IN ?", mediaIDs, []string{model.ScrapeTaskPending, model.ScrapeTaskRunning}).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.MediaID] = true
	}
	return out, nil
}

// ResetRunningToPending 启动自愈：进程中断遗留的 running 任务重置为 pending，
// 否则任务永久卡死（ClaimPending 只认 pending，重试按钮也拒绝 running）。
func (r *ScrapeTaskRepository) ResetRunningToPending(ctx context.Context) (int64, error) {
	res := r.db.WithContext(ctx).Model(&model.ScrapeTask{}).
		Where("status = ?", model.ScrapeTaskRunning).
		Updates(map[string]any{
			"status":     model.ScrapeTaskPending,
			"error":      "服务重启，任务已重置",
			"started_at": nil,
		})
	return res.RowsAffected, res.Error
}

func (r *ScrapeTaskRepository) List(ctx context.Context, status string, page, pageSize int) ([]model.ScrapeTask, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	q := r.db.WithContext(ctx).Model(&model.ScrapeTask{})
	if strings.TrimSpace(status) != "" && status != "all" {
		q = q.Where("status = ?", strings.TrimSpace(status))
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.ScrapeTask
	err := q.Order("created_at desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rows).Error
	return rows, total, err
}

func (r *ScrapeTaskRepository) CountByStatus(ctx context.Context) (map[string]int64, error) {
	var rows []struct {
		Status string
		Count  int64
	}
	err := r.db.WithContext(ctx).Model(&model.ScrapeTask{}).
		Select("status, count(*) as count").
		Group("status").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, row := range rows {
		out[row.Status] = row.Count
	}
	return out, nil
}

// ClaimPending picks pending scrape tasks and marks them running.
func (r *ScrapeTaskRepository) ClaimPending(ctx context.Context, limit int) ([]model.ScrapeTask, error) {
	scrapeClaimMu.Lock()
	defer scrapeClaimMu.Unlock()

	var rows []model.ScrapeTask
	err := withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("status = ?", model.ScrapeTaskPending).
				Order("created_at asc").Limit(limit).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			ids := make([]string, 0, len(rows))
			now := time.Now()
			for i := range rows {
				ids = append(ids, rows[i].ID)
				rows[i].Status = model.ScrapeTaskRunning
				rows[i].StartedAt = &now
			}
			return tx.Model(&model.ScrapeTask{}).Where("id IN ?", ids).
				Updates(map[string]any{"status": model.ScrapeTaskRunning, "started_at": now}).Error
		})
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *ScrapeTaskRepository) Update(ctx context.Context, t *model.ScrapeTask) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.ScrapeTask{}).Where("id = ?", t.ID).Updates(map[string]any{
			"status":        t.Status,
			"error":         t.Error,
			"provider":      t.Provider,
			"matched_title": t.MatchedTitle,
			"matched_year":  t.MatchedYear,
			"poster_url":    t.PosterURL,
			"backdrop_url":  t.BackdropURL,
			"retry_count":   t.RetryCount,
			"started_at":    t.StartedAt,
			"finished_at":   t.FinishedAt,
			"updated_at":    time.Now(),
		}).Error
	})
}

func (r *ScrapeTaskRepository) Delete(ctx context.Context, id string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Unscoped().Where("id = ?", id).Delete(&model.ScrapeTask{}).Error
	})
}

func (r *ScrapeTaskRepository) DeleteBatch(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("id IN ?", ids).Delete(&model.ScrapeTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

func (r *ScrapeTaskRepository) RetryBatch(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.ScrapeTask{}).
			Where("id IN ? AND status IN ?", ids, []string{model.ScrapeTaskFailed, model.ScrapeTaskCanceled}).
			Updates(map[string]any{
				"status":      model.ScrapeTaskPending,
				"error":       "",
				"retry_count": 0,
				"started_at":  nil,
				"finished_at": nil,
				"updated_at":  time.Now(),
			})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

func (r *ScrapeTaskRepository) CancelBatch(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	now := time.Now()
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.ScrapeTask{}).
			Where("id IN ? AND status IN ?", ids, []string{model.ScrapeTaskPending, model.ScrapeTaskRunning}).
			Updates(map[string]any{
				"status":      model.ScrapeTaskCanceled,
				"error":       "已批量取消",
				"finished_at": now,
				"updated_at":  now,
			})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

func (r *ScrapeTaskRepository) ClearDone(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("status = ?", model.ScrapeTaskDone).Delete(&model.ScrapeTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

func (r *ScrapeTaskRepository) ClearFinished(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("status IN ?", []string{model.ScrapeTaskDone, model.ScrapeTaskFailed, model.ScrapeTaskCanceled}).
			Delete(&model.ScrapeTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

func (r *ScrapeTaskRepository) ClearCanceled(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("status = ?", model.ScrapeTaskCanceled).Delete(&model.ScrapeTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

func (r *ScrapeTaskRepository) ClearFailed(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("status = ?", model.ScrapeTaskFailed).Delete(&model.ScrapeTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

func (r *ScrapeTaskRepository) RetryAllFailed(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.ScrapeTask{}).
			Where("status = ?", model.ScrapeTaskFailed).
			Updates(map[string]any{
				"status":      model.ScrapeTaskPending,
				"error":       "",
				"retry_count": 0,
				"started_at":  nil,
				"finished_at": nil,
				"updated_at":  time.Now(),
			})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

func (r *ScrapeTaskRepository) CancelPending(ctx context.Context) (int64, error) {
	now := time.Now()
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.ScrapeTask{}).
			Where("status IN ?", []string{model.ScrapeTaskPending, model.ScrapeTaskRunning}).
			Updates(map[string]any{
				"status":      model.ScrapeTaskCanceled,
				"error":       "已批量取消",
				"finished_at": now,
				"updated_at":  now,
			})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}
