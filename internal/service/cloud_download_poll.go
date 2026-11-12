// 云下载轮询：把 115 任务列表的状态映射回本地任务表。
//
// 设计约束：
//   - 没有活跃任务时不发起任何 115 请求（避免无意义配额与限流风险）；
//   - 按账号分组，一个账号一轮只翻页一次，供该账号下所有任务复用；
//   - 只做状态映射，不触发后处理（后处理由 pipeline 负责，两者解耦）。
package service

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/cloud115"
)

// PollOnce 执行一轮 115 任务状态同步。导出以便测试与手动触发。
func (s *CloudDownloadService) PollOnce(ctx context.Context) error {
	active, err := s.repo.CloudDownload.ListByStatuses(ctx, []string{
		model.CloudDownloadSubmitted,
		model.CloudDownloadDownloading,
	})
	if err != nil {
		return err
	}
	if len(active) == 0 {
		return nil
	}

	// 按账号分组：同一账号只拉一次任务列表。
	byAccount := map[string][]model.CloudDownloadTask{}
	for _, t := range active {
		byAccount[t.AccountID] = append(byAccount[t.AccountID], t)
	}

	for accountID, tasks := range byAccount {
		s.pollAccount(ctx, accountID, tasks)
	}
	return nil
}

// pollAccount 拉取单个账号的任务列表并回写本地任务状态。
func (s *CloudDownloadService) pollAccount(ctx context.Context, accountID string, tasks []model.CloudDownloadTask) {
	client, err := s.strm.Open115Client(ctx, accountID)
	if err != nil {
		// 账号被禁用/删除时不改任务状态：等用户处理账号，任务保留可重试。
		s.log.Warn("cloud download poll: open 115 client failed",
			zap.String("account_id", accountID), zap.Error(err))
		return
	}

	byHash := map[string]cloud115.OfflineTask{}
	byURL := map[string]cloud115.OfflineTask{}
	pages := s.maxPollPages()
	for page := 1; page <= pages; page++ {
		list, err := client.GetOfflineTaskList(ctx, page)
		if err != nil {
			s.log.Warn("cloud download poll: list tasks failed",
				zap.String("account_id", accountID), zap.Int("page", page), zap.Error(err))
			break
		}
		if list == nil {
			break
		}
		for _, t := range list.Tasks {
			if h := strings.ToUpper(strings.TrimSpace(t.InfoHash)); h != "" {
				byHash[h] = t
			}
			if u := strings.TrimSpace(t.URL); u != "" {
				byURL[u] = t
			}
		}
		if list.PageCount <= page || len(list.Tasks) == 0 {
			break
		}
	}

	now := time.Now()
	timeout := s.taskTimeout()
	for _, t := range tasks {
		offline := matchOfflineTask(t, byHash, byURL)
		fields := map[string]any{"last_poll_at": now}

		if offline == nil {
			// 115 侧暂未出现（受理有延迟）或已不在列表（被清理）。
			// 只在超时后才判失败，避免把「刚提交」误判成失败。
			if !t.CreatedAt.IsZero() && now.Sub(t.CreatedAt) > timeout {
				fields["status"] = model.CloudDownloadFailed
				fields["error"] = "下载超时：115 侧长时间未完成（任务可能已被清理）"
			}
			s.applyPollFields(ctx, t.ID, fields)
			continue
		}

		switch offline.Status {
		case cloud115.OfflineTaskStatusDone:
			fields["status"] = model.CloudDownloadSucceeded
			fields["percent"] = 100
			if offline.Size > 0 {
				fields["size_bytes"] = offline.Size
			}
			if t.CompletedAt == nil {
				fields["completed_at"] = now
			}
			fields["error"] = ""
		case cloud115.OfflineTaskStatusFailed:
			fields["status"] = model.CloudDownloadFailed
			fields["error"] = "115 云下载失败（可能资源失效或无人做种）"
		default: // 0 分配中 / 1 下载中
			fields["status"] = model.CloudDownloadDownloading
			fields["percent"] = offline.PercentDone
			if offline.Size > 0 {
				fields["size_bytes"] = offline.Size
			}
		}
		s.applyPollFields(ctx, t.ID, fields)
	}
}

// applyPollFields 只写轮询关心的列，绝不能整行 Update：
// 后处理线程可能在并发写 stage/scan_added 等字段。
func (s *CloudDownloadService) applyPollFields(ctx context.Context, taskID string, fields map[string]any) {
	if err := s.repo.CloudDownload.UpdateFields(ctx, taskID, fields); err != nil {
		s.log.Warn("cloud download poll: update task failed",
			zap.String("task_id", taskID), zap.Error(err))
	}
}

// matchOfflineTask 匹配 115 任务：info_hash 优先，其次原始链接。
func matchOfflineTask(t model.CloudDownloadTask, byHash, byURL map[string]cloud115.OfflineTask) *cloud115.OfflineTask {
	if h := strings.ToUpper(strings.TrimSpace(t.InfoHash)); h != "" {
		if item, ok := byHash[h]; ok {
			return &item
		}
	}
	for _, key := range []string{t.SourceKey, t.SourceURL} {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if item, ok := byURL[key]; ok {
			return &item
		}
	}
	return nil
}
