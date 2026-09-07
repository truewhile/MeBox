package repository

import (
	"context"
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

var strmClaimMu sync.Mutex

// ─── StrmAccount ───────────────────────────────────────────────────────────────

// StrmAccountRepository persists model.StrmAccount.
type StrmAccountRepository struct{ db *gorm.DB }

func (r *StrmAccountRepository) Create(ctx context.Context, a *model.StrmAccount) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Create(a).Error
	})
}

func (r *StrmAccountRepository) FindByID(ctx context.Context, id string) (*model.StrmAccount, error) {
	var a model.StrmAccount
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *StrmAccountRepository) List(ctx context.Context) ([]model.StrmAccount, error) {
	var rows []model.StrmAccount
	err := r.db.WithContext(ctx).Order("created_at desc").Find(&rows).Error
	return rows, err
}

func (r *StrmAccountRepository) Update(ctx context.Context, a *model.StrmAccount) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.StrmAccount{}).Where("id = ?", a.ID).Updates(map[string]any{
			"name":             a.Name,
			"provider":         a.Provider,
			"config":           a.Config,
			"enabled":          a.Enabled,
			"last_test_at":     a.LastTestAt,
			"last_test_result": a.LastTestResult,
			"last_test_ok":     a.LastTestOK,
			"updated_at":       time.Now(),
		}).Error
	})
}

func (r *StrmAccountRepository) Delete(ctx context.Context, id string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Unscoped().Where("id = ?", id).Delete(&model.StrmAccount{}).Error
	})
}

// ─── StrmSyncPath ──────────────────────────────────────────────────────────────

// StrmSyncPathRepository persists model.StrmSyncPath.
type StrmSyncPathRepository struct{ db *gorm.DB }

func (r *StrmSyncPathRepository) Create(ctx context.Context, p *model.StrmSyncPath) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Create(p).Error
	})
}

func (r *StrmSyncPathRepository) FindByID(ctx context.Context, id string) (*model.StrmSyncPath, error) {
	var p model.StrmSyncPath
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *StrmSyncPathRepository) List(ctx context.Context) ([]model.StrmSyncPath, error) {
	var rows []model.StrmSyncPath
	err := r.db.WithContext(ctx).Order("created_at desc").Find(&rows).Error
	return rows, err
}

func (r *StrmSyncPathRepository) Update(ctx context.Context, p *model.StrmSyncPath) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.StrmSyncPath{}).Where("id = ?", p.ID).Updates(map[string]any{
			"name":                p.Name,
			"account_id":          p.AccountID,
			"provider":            p.Provider,
			"remote_path":         p.RemotePath,
			"remote_display_path": p.RemoteDisplayPath,
			"local_path":          p.LocalPath,
			"strm_base_url":       p.StrmBaseURL,
			"video_ext":           p.VideoExt,
			"meta_ext":            p.MetaExt,
			"exclude_name":        p.ExcludeName,
			"min_video_size_mb":   p.MinVideoSizeMB,
			"add_path":            p.AddPath,
			"download_meta":       p.DownloadMeta,
			"upload_meta":         p.UploadMeta,
			"delete_dir":          p.DeleteDir,
			"keep_ext":            p.KeepExt,
			"cron":                p.Cron,
			"enable_cron":         p.EnableCron,
			"sync_mode":           p.SyncMode,
			"enabled":             p.Enabled,
			"last_sync_at":        p.LastSyncAt,
			"last_sync_status":    p.LastSyncStatus,
			"last_sync_message":   p.LastSyncMessage,
			"updated_at":          time.Now(),
		}).Error
	})
}

func (r *StrmSyncPathRepository) Delete(ctx context.Context, id string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Unscoped().Where("id = ?", id).Delete(&model.StrmSyncPath{}).Error
	})
}

// ─── StrmSyncRecord ────────────────────────────────────────────────────────────

// StrmSyncRecordRepository persists model.StrmSyncRecord.
type StrmSyncRecordRepository struct{ db *gorm.DB }

func (r *StrmSyncRecordRepository) Create(ctx context.Context, rec *model.StrmSyncRecord) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Create(rec).Error
	})
}

func (r *StrmSyncRecordRepository) Update(ctx context.Context, rec *model.StrmSyncRecord) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.StrmSyncRecord{}).Where("id = ?", rec.ID).Updates(map[string]any{
			"sync_type":   rec.SyncType,
			"status":      rec.Status,
			"total":       rec.Total,
			"new_strm":    rec.NewStrm,
			"new_meta":    rec.NewMeta,
			"uploaded":    rec.Uploaded,
			"pruned":      rec.Pruned,
			"skipped":     rec.Skipped,
			"message":     rec.Message,
			"started_at":  rec.StartedAt,
			"finished_at": rec.FinishedAt,
			"updated_at":  time.Now(),
		}).Error
	})
}

func (r *StrmSyncRecordRepository) List(ctx context.Context, syncPathID string, limit int) ([]model.StrmSyncRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []model.StrmSyncRecord
	q := r.db.WithContext(ctx)
	if syncPathID != "" {
		q = q.Where("sync_path_id = ?", syncPathID)
	}
	err := q.Order("created_at desc").Limit(limit).Find(&rows).Error
	return rows, err
}

// Delete 删除单条同步记录（物理删除）。
func (r *StrmSyncRecordRepository) Delete(ctx context.Context, id string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Unscoped().Where("id = ?", id).Delete(&model.StrmSyncRecord{}).Error
	})
}

// DeleteBySyncPathID 删除某同步目录下的全部同步记录（删除同步目录时级联清理）。
func (r *StrmSyncRecordRepository) DeleteBySyncPathID(ctx context.Context, syncPathID string) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("sync_path_id = ?", syncPathID).Delete(&model.StrmSyncRecord{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// ─── StrmDownloadTask ──────────────────────────────────────────────────────────

// StrmDownloadTaskRepository persists model.StrmDownloadTask.
type StrmDownloadTaskRepository struct{ db *gorm.DB }

func (r *StrmDownloadTaskRepository) Create(ctx context.Context, t *model.StrmDownloadTask) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Create(t).Error
	})
}

func (r *StrmDownloadTaskRepository) CreateInBatches(ctx context.Context, tasks []*model.StrmDownloadTask, batchSize int) error {
	if len(tasks) == 0 {
		return nil
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).CreateInBatches(tasks, batchSize).Error
	})
}

func (r *StrmDownloadTaskRepository) FindByID(ctx context.Context, id string) (*model.StrmDownloadTask, error) {
	var t model.StrmDownloadTask
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *StrmDownloadTaskRepository) List(ctx context.Context, status string, page, pageSize int) ([]model.StrmDownloadTask, int64, error) {
	page, pageSize = normalizeTaskPage(page, pageSize)
	var total int64
	if err := taskStatusScope(r.db.WithContext(ctx), status).Model(&model.StrmDownloadTask{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.StrmDownloadTask
	err := taskStatusScope(r.db.WithContext(ctx), status).
		Order("created_at desc, id desc").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&rows).Error
	return rows, total, err
}

func (r *StrmDownloadTaskRepository) CountByStatus(ctx context.Context) (map[string]int64, error) {
	var rows []struct {
		Status string
		Count  int64
	}
	err := r.db.WithContext(ctx).Model(&model.StrmDownloadTask{}).
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

// ClaimPendingDownload picks the oldest pending task and marks it running.
// Returns (nil, nil) when the queue is empty.
func (r *StrmDownloadTaskRepository) ClaimPendingDownload(ctx context.Context, limit int) ([]model.StrmDownloadTask, error) {
	strmClaimMu.Lock()
	defer strmClaimMu.Unlock()

	var rows []model.StrmDownloadTask
	err := withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("status = ? AND (next_try_at IS NULL OR next_try_at <= ?)", model.StrmTaskPending, time.Now()).
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
				rows[i].Status = model.StrmTaskRunning
				rows[i].StartedAt = &now
			}
			return tx.Model(&model.StrmDownloadTask{}).Where("id IN ?", ids).
				Updates(map[string]any{"status": model.StrmTaskRunning, "started_at": now}).Error
		})
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *StrmDownloadTaskRepository) Update(ctx context.Context, t *model.StrmDownloadTask) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.StrmDownloadTask{}).Where("id = ?", t.ID).Updates(map[string]any{
			"status":      t.Status,
			"error":       t.Error,
			"retry_count": t.RetryCount,
			"next_try_at": t.NextTryAt,
			"started_at":  t.StartedAt,
			"finished_at": t.FinishedAt,
			"updated_at":  time.Now(),
		}).Error
	})
}

// UpdateIfRunning 仅当任务在 DB 中仍为 running 时写入给定字段。
// 返回 false 表示任务已被外部改变状态（如用户取消），收尾不得覆盖。
func (r *StrmDownloadTaskRepository) UpdateIfRunning(ctx context.Context, id string, updates map[string]any) (bool, error) {
	var ok bool
	err := withSQLiteBusyRetry(ctx, func() error {
		updates["updated_at"] = time.Now()
		res := r.db.WithContext(ctx).Model(&model.StrmDownloadTask{}).
			Where("id = ? AND status = ?", id, model.StrmTaskRunning).Updates(updates)
		ok = res.RowsAffected > 0
		return res.Error
	})
	return ok, err
}

// ResetRunningToPending 启动自愈：进程中断遗留的 running 任务全部重置为
// pending（清空退避时间以便立即可被认领），否则任务永久卡死且会阻塞
// 该文件的重复下载。
func (r *StrmDownloadTaskRepository) ResetRunningToPending(ctx context.Context) (int64, error) {
	var n int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.StrmDownloadTask{}).
			Where("status = ?", model.StrmTaskRunning).
			Updates(map[string]any{
				"status":     model.StrmTaskPending,
				"error":      "服务重启，任务已重置",
				"started_at": nil,
			})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

func (r *StrmDownloadTaskRepository) Delete(ctx context.Context, id string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Unscoped().Where("id = ?", id).Delete(&model.StrmDownloadTask{}).Error
	})
}

// DeleteBatch 批量删除指定 ID 的下载任务。
func (r *StrmDownloadTaskRepository) DeleteBatch(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("id IN ?", ids).Delete(&model.StrmDownloadTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// RetryBatch 批量重试指定 ID 的失败/已取消下载任务。
func (r *StrmDownloadTaskRepository) RetryBatch(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.StrmDownloadTask{}).
			Where("id IN ? AND status IN ?", ids, []string{model.StrmTaskFailed, model.StrmTaskCanceled}).
			Updates(map[string]any{
				"status":      model.StrmTaskPending,
				"error":       "",
				"retry_count": 0,
				"next_try_at": nil,
				"started_at":  nil,
				"finished_at": nil,
				"updated_at":  time.Now(),
			})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// CancelBatch 批量取消指定 ID 的排队/进行中下载任务。
func (r *StrmDownloadTaskRepository) CancelBatch(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	now := time.Now()
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.StrmDownloadTask{}).
			Where("id IN ? AND status IN ?", ids, []string{model.StrmTaskPending, model.StrmTaskRunning}).
			Updates(map[string]any{
				"status":      model.StrmTaskCanceled,
				"error":       "已批量取消",
				"finished_at": now,
				"updated_at":  now,
			})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// ClearDone 清空全部已完成下载任务。
func (r *StrmDownloadTaskRepository) ClearDone(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("status = ?", model.StrmTaskDone).Delete(&model.StrmDownloadTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// ClearFinished 清空全部已完成与失败下载任务。
func (r *StrmDownloadTaskRepository) ClearFinished(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("status IN ?", []string{model.StrmTaskDone, model.StrmTaskFailed, model.StrmTaskCanceled}).
			Delete(&model.StrmDownloadTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// ClearCanceled 清空全部已取消下载任务。
func (r *StrmDownloadTaskRepository) ClearCanceled(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("status = ?", model.StrmTaskCanceled).Delete(&model.StrmDownloadTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// ClearFailed 清空全部已失败下载任务。
func (r *StrmDownloadTaskRepository) ClearFailed(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("status = ?", model.StrmTaskFailed).Delete(&model.StrmDownloadTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// RetryAllFailed 把所有失败任务重置回待处理，清空错误与重试计数。
func (r *StrmDownloadTaskRepository) RetryAllFailed(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.StrmDownloadTask{}).
			Where("status = ?", model.StrmTaskFailed).
			Updates(map[string]any{
				"status":      model.StrmTaskPending,
				"error":       "",
				"retry_count": 0,
				"next_try_at": nil,
				"started_at":  nil,
				"finished_at": nil,
				"updated_at":  time.Now(),
			})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// CancelPending 批量取消所有排队中和进行中的任务。
func (r *StrmDownloadTaskRepository) CancelPending(ctx context.Context) (int64, error) {
	now := time.Now()
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.StrmDownloadTask{}).
			Where("status IN ?", []string{model.StrmTaskPending, model.StrmTaskRunning}).
			Updates(map[string]any{
				"status":      model.StrmTaskCanceled,
				"error":       "已批量取消",
				"finished_at": now,
				"updated_at":  now,
			})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// CountActive 统计某同步目录下目标仍在排队/进行的任务数（用于去重）。
func (r *StrmDownloadTaskRepository) CountActive(ctx context.Context, syncPathID, localPath string) int64 {
	var count int64
	r.db.WithContext(ctx).Model(&model.StrmDownloadTask{}).
		Where("sync_path_id = ? AND local_path = ? AND status IN ?",
			syncPathID, localPath, []string{model.StrmTaskPending, model.StrmTaskRunning}).
		Count(&count)
	return count
}

// GetActiveLocalPathMap 一次性获取某同步目录下正在排队或执行中的 local_path 集合，供同步时 O(1) 内存去重。
func (r *StrmDownloadTaskRepository) GetActiveLocalPathMap(ctx context.Context, syncPathID string) (map[string]bool, error) {
	var paths []string
	err := r.db.WithContext(ctx).Model(&model.StrmDownloadTask{}).
		Where("sync_path_id = ? AND status IN ?", syncPathID, []string{model.StrmTaskPending, model.StrmTaskRunning}).
		Pluck("local_path", &paths).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		out[p] = true
	}
	return out, nil
}

func (r *StrmDownloadTaskRepository) DeleteFinishedOlderThan(ctx context.Context, before time.Time) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Unscoped().Where("status IN ? AND finished_at < ?",
			[]string{model.StrmTaskDone, model.StrmTaskFailed, model.StrmTaskCanceled}, before).
			Delete(&model.StrmDownloadTask{}).Error
	})
}

// ─── StrmUploadTask ────────────────────────────────────────────────────────────

// StrmUploadTaskRepository persists model.StrmUploadTask.
type StrmUploadTaskRepository struct{ db *gorm.DB }

func (r *StrmUploadTaskRepository) Create(ctx context.Context, t *model.StrmUploadTask) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Create(t).Error
	})
}

func (r *StrmUploadTaskRepository) CreateInBatches(ctx context.Context, tasks []*model.StrmUploadTask, batchSize int) error {
	if len(tasks) == 0 {
		return nil
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).CreateInBatches(tasks, batchSize).Error
	})
}

func (r *StrmUploadTaskRepository) FindByID(ctx context.Context, id string) (*model.StrmUploadTask, error) {
	var t model.StrmUploadTask
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *StrmUploadTaskRepository) List(ctx context.Context, status string, page, pageSize int) ([]model.StrmUploadTask, int64, error) {
	page, pageSize = normalizeTaskPage(page, pageSize)
	var total int64
	if err := taskStatusScope(r.db.WithContext(ctx), status).Model(&model.StrmUploadTask{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.StrmUploadTask
	err := taskStatusScope(r.db.WithContext(ctx), status).
		Order("created_at desc, id desc").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&rows).Error
	return rows, total, err
}

// normalizeTaskPage 钳制分页参数：页码至少 1，单页大小 1..200。
func normalizeTaskPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	return page, pageSize
}

// taskStatusScope 按状态过滤（空状态表示不过滤）。
func taskStatusScope(db *gorm.DB, status string) *gorm.DB {
	if status != "" {
		return db.Where("status = ?", status)
	}
	return db
}

func (r *StrmUploadTaskRepository) CountByStatus(ctx context.Context) (map[string]int64, error) {
	var rows []struct {
		Status string
		Count  int64
	}
	err := r.db.WithContext(ctx).Model(&model.StrmUploadTask{}).
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

// ClaimPendingUpload picks the oldest pending task and marks it running.
// Returns (nil, nil) when the queue is empty.
func (r *StrmUploadTaskRepository) ClaimPendingUpload(ctx context.Context, limit int) ([]model.StrmUploadTask, error) {
	strmClaimMu.Lock()
	defer strmClaimMu.Unlock()

	var rows []model.StrmUploadTask
	err := withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("status = ? AND (next_try_at IS NULL OR next_try_at <= ?)", model.StrmTaskPending, time.Now()).
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
				rows[i].Status = model.StrmTaskRunning
				rows[i].StartedAt = &now
			}
			return tx.Model(&model.StrmUploadTask{}).Where("id IN ?", ids).
				Updates(map[string]any{"status": model.StrmTaskRunning, "started_at": now}).Error
		})
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *StrmUploadTaskRepository) Update(ctx context.Context, t *model.StrmUploadTask) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.StrmUploadTask{}).Where("id = ?", t.ID).Updates(map[string]any{
			"status":      t.Status,
			"error":       t.Error,
			"retry_count": t.RetryCount,
			"next_try_at": t.NextTryAt,
			"started_at":  t.StartedAt,
			"finished_at": t.FinishedAt,
			"updated_at":  time.Now(),
		}).Error
	})
}

// UpdateIfRunning 仅当任务在 DB 中仍为 running 时写入给定字段。
func (r *StrmUploadTaskRepository) UpdateIfRunning(ctx context.Context, id string, updates map[string]any) (bool, error) {
	var ok bool
	err := withSQLiteBusyRetry(ctx, func() error {
		updates["updated_at"] = time.Now()
		res := r.db.WithContext(ctx).Model(&model.StrmUploadTask{}).
			Where("id = ? AND status = ?", id, model.StrmTaskRunning).Updates(updates)
		ok = res.RowsAffected > 0
		return res.Error
	})
	return ok, err
}

// ResetRunningToPending 启动自愈：进程中断遗留的 running 任务全部重置为 pending。
func (r *StrmUploadTaskRepository) ResetRunningToPending(ctx context.Context) (int64, error) {
	var n int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.StrmUploadTask{}).
			Where("status = ?", model.StrmTaskRunning).
			Updates(map[string]any{
				"status":     model.StrmTaskPending,
				"error":      "服务重启，任务已重置",
				"started_at": nil,
			})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

func (r *StrmUploadTaskRepository) Delete(ctx context.Context, id string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Unscoped().Where("id = ?", id).Delete(&model.StrmUploadTask{}).Error
	})
}

// DeleteBatch 批量删除指定 ID 的上传任务。
func (r *StrmUploadTaskRepository) DeleteBatch(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("id IN ?", ids).Delete(&model.StrmUploadTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// RetryBatch 批量重试指定 ID 的失败/已取消上传任务。
func (r *StrmUploadTaskRepository) RetryBatch(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.StrmUploadTask{}).
			Where("id IN ? AND status IN ?", ids, []string{model.StrmTaskFailed, model.StrmTaskCanceled}).
			Updates(map[string]any{
				"status":      model.StrmTaskPending,
				"error":       "",
				"retry_count": 0,
				"next_try_at": nil,
				"started_at":  nil,
				"finished_at": nil,
				"updated_at":  time.Now(),
			})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// CancelBatch 批量取消指定 ID 的排队/进行中上传任务。
func (r *StrmUploadTaskRepository) CancelBatch(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	now := time.Now()
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.StrmUploadTask{}).
			Where("id IN ? AND status IN ?", ids, []string{model.StrmTaskPending, model.StrmTaskRunning}).
			Updates(map[string]any{
				"status":      model.StrmTaskCanceled,
				"error":       "已批量取消",
				"finished_at": now,
				"updated_at":  now,
			})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// ClearDone 清空全部已完成上传任务。
func (r *StrmUploadTaskRepository) ClearDone(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("status = ?", model.StrmTaskDone).Delete(&model.StrmUploadTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// ClearFinished 清空全部已完成与失败上传任务（包括已完成、失败及取消）。
func (r *StrmUploadTaskRepository) ClearFinished(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("status IN ?", []string{model.StrmTaskDone, model.StrmTaskFailed, model.StrmTaskCanceled}).
			Delete(&model.StrmUploadTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// ClearCanceled 清空全部已取消上传任务。
func (r *StrmUploadTaskRepository) ClearCanceled(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("status = ?", model.StrmTaskCanceled).Delete(&model.StrmUploadTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// ClearFailed 清空全部已失败上传任务。
func (r *StrmUploadTaskRepository) ClearFailed(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().Where("status = ?", model.StrmTaskFailed).Delete(&model.StrmUploadTask{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// RetryAllFailed 把所有失败任务重置回待处理，清空错误与重试计数。
func (r *StrmUploadTaskRepository) RetryAllFailed(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.StrmUploadTask{}).
			Where("status = ?", model.StrmTaskFailed).
			Updates(map[string]any{
				"status":      model.StrmTaskPending,
				"error":       "",
				"retry_count": 0,
				"next_try_at": nil,
				"started_at":  nil,
				"finished_at": nil,
				"updated_at":  time.Now(),
			})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// CancelPending 批量取消所有排队中和进行中的任务。
func (r *StrmUploadTaskRepository) CancelPending(ctx context.Context) (int64, error) {
	now := time.Now()
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.StrmUploadTask{}).
			Where("status IN ?", []string{model.StrmTaskPending, model.StrmTaskRunning}).
			Updates(map[string]any{
				"status":      model.StrmTaskCanceled,
				"error":       "已批量取消",
				"finished_at": now,
				"updated_at":  now,
			})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// CountActive 统计某同步目录下目标仍在排队/进行的任务数（用于去重）。
func (r *StrmUploadTaskRepository) CountActive(ctx context.Context, syncPathID, localPath string) int64 {
	var count int64
	r.db.WithContext(ctx).Model(&model.StrmUploadTask{}).
		Where("sync_path_id = ? AND local_path = ? AND status IN ?",
			syncPathID, localPath, []string{model.StrmTaskPending, model.StrmTaskRunning}).
		Count(&count)
	return count
}

// GetActiveLocalPathMap 一次性获取某同步目录下正在排队或执行中的 local_path 集合，供同步时 O(1) 内存去重。
func (r *StrmUploadTaskRepository) GetActiveLocalPathMap(ctx context.Context, syncPathID string) (map[string]bool, error) {
	var paths []string
	err := r.db.WithContext(ctx).Model(&model.StrmUploadTask{}).
		Where("sync_path_id = ? AND status IN ?", syncPathID, []string{model.StrmTaskPending, model.StrmTaskRunning}).
		Pluck("local_path", &paths).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		out[p] = true
	}
	return out, nil
}

// GetRecentDoneUploadSizeMap 返回近期已成功上传的 local_path → size。
// 用于缩短「上传已 done 但 115 列表尚未反映」窗口内的重复入队：同路径且大小未变则跳过。
// 同一路径存在多条 done 时取最新一条（finished_at 降序）。
func (r *StrmUploadTaskRepository) GetRecentDoneUploadSizeMap(ctx context.Context, syncPathID string, since time.Time) (map[string]int64, error) {
	var rows []model.StrmUploadTask
	err := r.db.WithContext(ctx).Model(&model.StrmUploadTask{}).
		Select("local_path", "size", "finished_at").
		Where("sync_path_id = ? AND status = ? AND finished_at IS NOT NULL AND finished_at >= ?",
			syncPathID, model.StrmTaskDone, since).
		Order("finished_at DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		if row.LocalPath == "" {
			continue
		}
		// 已按 finished_at DESC；先写入的是最新，后续同路径跳过
		if _, exists := out[row.LocalPath]; exists {
			continue
		}
		out[row.LocalPath] = row.Size
	}
	return out, nil
}

func (r *StrmUploadTaskRepository) DeleteFinishedOlderThan(ctx context.Context, before time.Time) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Unscoped().Where("status IN ? AND finished_at < ?",
			[]string{model.StrmTaskDone, model.StrmTaskFailed, model.StrmTaskCanceled}, before).
			Delete(&model.StrmUploadTask{}).Error
	})
}

// ─── StrmDirCache ─────────────────────────────────────────────────────────────

// StrmDirCacheRepository persists model.StrmDirCache.
type StrmDirCacheRepository struct{ db *gorm.DB }

func (r *StrmDirCacheRepository) ListBySyncPathID(ctx context.Context, syncPathID string) ([]model.StrmDirCache, error) {
	var rows []model.StrmDirCache
	err := r.db.WithContext(ctx).Where("sync_path_id = ?", syncPathID).Find(&rows).Error
	return rows, err
}

func (r *StrmDirCacheRepository) Set(ctx context.Context, syncPathID, dirID, path string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		var row model.StrmDirCache
		err := r.db.WithContext(ctx).Where("sync_path_id = ? AND dir_id = ?", syncPathID, dirID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = model.StrmDirCache{
				SyncPathID: syncPathID,
				DirID:      dirID,
				Path:       path,
			}
			return r.db.WithContext(ctx).Create(&row).Error
		}
		if err != nil {
			return err
		}
		return r.db.WithContext(ctx).Model(&model.StrmDirCache{}).Where("id = ?", row.ID).Updates(map[string]any{
			"path":       path,
			"updated_at": time.Now(),
		}).Error
	})
}

// SetBatch 批量 upsert 目录缓存（dirID → 相对路径）。单个事务内先查出已存在
// 行再分流更新/插入，替代同步流程逐目录单条 Set，避免首次全量同步上万目录时
// 的 SQLite 写锁竞争。同一 dirID 的重复项以 map 语义取最后一次写入。
func (r *StrmDirCacheRepository) SetBatch(ctx context.Context, syncPathID string, paths map[string]string) error {
	if len(paths) == 0 {
		return nil
	}
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			ids := make([]string, 0, len(paths))
			for dirID := range paths {
				ids = append(ids, dirID)
			}
			var existing []model.StrmDirCache
			if err := tx.Where("sync_path_id = ? AND dir_id IN ?", syncPathID, ids).Find(&existing).Error; err != nil {
				return err
			}
			existingRowID := make(map[string]string, len(existing))
			for _, row := range existing {
				existingRowID[row.DirID] = row.ID
			}
			now := time.Now()
			var creates []model.StrmDirCache
			for dirID, path := range paths {
				if rowID, ok := existingRowID[dirID]; ok {
					if err := tx.Model(&model.StrmDirCache{}).Where("id = ?", rowID).Updates(map[string]any{
						"path":       path,
						"updated_at": now,
					}).Error; err != nil {
						return err
					}
					continue
				}
				creates = append(creates, model.StrmDirCache{
					SyncPathID: syncPathID,
					DirID:      dirID,
					Path:       path,
				})
			}
			if len(creates) > 0 {
				return tx.CreateInBatches(creates, 100).Error
			}
			return nil
		})
	})
}

func (r *StrmDirCacheRepository) DeleteBySyncPathID(ctx context.Context, syncPathID string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Unscoped().Where("sync_path_id = ?", syncPathID).Delete(&model.StrmDirCache{}).Error
	})
}
