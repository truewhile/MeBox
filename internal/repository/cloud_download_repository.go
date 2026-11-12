package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// CloudDownloadRepository persists model.CloudDownloadTask（115 云下载任务）。
type CloudDownloadRepository struct{ db *gorm.DB }

// CloudDownloadQuery 是任务列表的过滤条件。
type CloudDownloadQuery struct {
	Status     string // 精确匹配；空表示全部
	SyncPathID string
	AccountID  string
	Page       int
	PageSize   int
}

// Create 插入一条云下载任务。
func (r *CloudDownloadRepository) Create(ctx context.Context, t *model.CloudDownloadTask) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Create(t).Error
	})
}

// Update 更新任务的可变字段（不含主键与创建时间）。
func (r *CloudDownloadRepository) Update(ctx context.Context, t *model.CloudDownloadTask) error {
	return r.UpdateFields(ctx, t.ID, map[string]any{
		"account_id":     t.AccountID,
		"sync_path_id":   t.SyncPathID,
		"library_id":     t.LibraryID,
		"provider":       t.Provider,
		"source_type":    t.SourceType,
		"source_url":     t.SourceURL,
		"source_key":     t.SourceKey,
		"info_hash":      t.InfoHash,
		"torrent_name":   t.TorrentName,
		"torrent_sha1":   t.TorrentSha1,
		"pick_code":      t.PickCode,
		"wp_path_id":     t.WpPathID,
		"save_path":      t.SavePath,
		"status":         t.Status,
		"percent":        t.Percent,
		"size_bytes":     t.SizeBytes,
		"error":          t.Error,
		"stage":          t.Stage,
		"stage_error":    t.StageError,
		"strm_record_id": t.StrmRecordID,
		"scan_added":     t.ScanAdded,
		"scrape_queued":  t.ScrapeQueued,
		"retry_count":    t.RetryCount,
		"last_poll_at":   t.LastPollAt,
		"next_poll_at":   t.NextPollAt,
		"completed_at":   t.CompletedAt,
	})
}

// UpdateFields 局部更新：调用方只写自己关心的列，避免覆盖并发写入的其它阶段字段
// （轮询线程写 Status，后处理线程写 Stage，两者必须互不覆盖）。
func (r *CloudDownloadRepository) UpdateFields(ctx context.Context, id string, fields map[string]any) error {
	if id == "" || len(fields) == 0 {
		return nil
	}
	fields["updated_at"] = time.Now()
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.CloudDownloadTask{}).Where("id = ?", id).Updates(fields).Error
	})
}

// FindByID 按主键查询，未找到返回 (nil, nil)。
func (r *CloudDownloadRepository) FindByID(ctx context.Context, id string) (*model.CloudDownloadTask, error) {
	var t model.CloudDownloadTask
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// List 分页查询任务列表，同时返回总数。
func (r *CloudDownloadRepository) List(ctx context.Context, q CloudDownloadQuery) ([]model.CloudDownloadTask, int64, error) {
	db := r.db.WithContext(ctx).Model(&model.CloudDownloadTask{})
	if q.Status != "" {
		db = db.Where("status = ?", q.Status)
	}
	if q.SyncPathID != "" {
		db = db.Where("sync_path_id = ?", q.SyncPathID)
	}
	if q.AccountID != "" {
		db = db.Where("account_id = ?", q.AccountID)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	size := q.PageSize
	if size <= 0 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	var rows []model.CloudDownloadTask
	err := db.Order("created_at desc").
		Offset((page - 1) * size).
		Limit(size).
		Find(&rows).Error
	return rows, total, err
}

// ListByStatuses 取指定状态的任务（轮询用）。
func (r *CloudDownloadRepository) ListByStatuses(ctx context.Context, statuses []string) ([]model.CloudDownloadTask, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	var rows []model.CloudDownloadTask
	err := r.db.WithContext(ctx).
		Where("status IN ?", statuses).
		Order("created_at asc").
		Find(&rows).Error
	return rows, err
}

// ListBySyncPathAndStage 取某个同步目录下处于指定阶段的任务（后处理推进用）。
func (r *CloudDownloadRepository) ListBySyncPathAndStage(ctx context.Context, syncPathID, stage string) ([]model.CloudDownloadTask, error) {
	var rows []model.CloudDownloadTask
	err := r.db.WithContext(ctx).
		Where("sync_path_id = ? AND stage = ?", syncPathID, stage).
		Order("created_at asc").
		Find(&rows).Error
	return rows, err
}

// ListPipelineCandidates 取「下载已成功、后处理尚未完成」的任务。
func (r *CloudDownloadRepository) ListPipelineCandidates(ctx context.Context) ([]model.CloudDownloadTask, error) {
	var rows []model.CloudDownloadTask
	err := r.db.WithContext(ctx).
		Where("status = ? AND stage IN ?", model.CloudDownloadSucceeded,
			[]string{model.CloudDownloadStagePending, model.CloudDownloadStageWaitingSync}).
		Order("created_at asc").
		Find(&rows).Error
	return rows, err
}

// ResetStaleStages 把上次进程退出时停在中间阶段的任务重置为待推进。
// STRM 同步/扫描/刮削都是幂等操作，重启后重新推进是安全的。
func (r *CloudDownloadRepository) ResetStaleStages(ctx context.Context) (int64, error) {
	var affected int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Model(&model.CloudDownloadTask{}).
			Where("stage IN ?", []string{
				model.CloudDownloadStageSyncing,
				model.CloudDownloadStageScanning,
				model.CloudDownloadStageScraping,
			}).
			Updates(map[string]any{
				"stage":       model.CloudDownloadStagePending,
				"stage_error": "",
				"updated_at":  time.Now(),
			})
		affected = res.RowsAffected
		return res.Error
	})
	return affected, err
}

// FindActiveBySourceKeys 返回给定匹配键中仍处于活跃状态的任务键集合。
// 用于提交时去重：同一个磁力重复提交只会浪费 115 配额。
func (r *CloudDownloadRepository) FindActiveBySourceKeys(ctx context.Context, keys []string) (map[string]bool, error) {
	out := make(map[string]bool, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	var found []string
	err := r.db.WithContext(ctx).Model(&model.CloudDownloadTask{}).
		Where("source_key IN ?", keys).
		Where("status IN ?", []string{
			model.CloudDownloadQueued,
			model.CloudDownloadSubmitted,
			model.CloudDownloadDownloading,
		}).
		Pluck("source_key", &found).Error
	if err != nil {
		return out, err
	}
	for _, k := range found {
		out[k] = true
	}
	return out, nil
}

// Delete 硬删除一条任务记录。
func (r *CloudDownloadRepository) Delete(ctx context.Context, id string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Unscoped().Where("id = ?", id).Delete(&model.CloudDownloadTask{}).Error
	})
}

// ClearFinished 删除所有「无后续价值」的终态任务（失败/已取消）。
// 成功的任务保留：它们承载「本地已生成 strm / 已刮削」的证据，供用户回查。
func (r *CloudDownloadRepository) ClearFinished(ctx context.Context) (int64, error) {
	var affected int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Unscoped().
			Where("status IN ?", []string{
				model.CloudDownloadFailed,
				model.CloudDownloadCanceled,
			}).
			Delete(&model.CloudDownloadTask{})
		affected = res.RowsAffected
		return res.Error
	})
	return affected, err
}

// CountBySyncPath 统计某同步目录下仍在活跃的任务数（前端展示用）。
func (r *CloudDownloadRepository) CountBySyncPath(ctx context.Context, syncPathID string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.CloudDownloadTask{}).
		Where("sync_path_id = ?", syncPathID).
		Where("status IN ?", []string{
			model.CloudDownloadQueued,
			model.CloudDownloadSubmitted,
			model.CloudDownloadDownloading,
		}).
		Count(&n).Error
	return n, err
}
