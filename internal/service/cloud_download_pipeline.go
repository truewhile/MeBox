// 云下载后处理管线：下载成功 → STRM 增量同步 → 媒体库扫描 → 自动刮削。
//
// 与 STRM 同步的耦合方式：
//   - 这里只负责「发起同步」，不知道同步内部怎么走；
//   - StrmService.finishSync 通过 SetSyncFinishHook 回调 onStrmSyncFinished，
//     由后者把 syncing 状态推进到 scanning/done。
//
// 去抖：bench 上常见一场「一次提交 20 条链接」，如果每条完成都触发一次全树
// 同步（walk115Flat 是整树自适应拉取），115 限流与本地 IO 都会被打满。
// 因此同一同步目录在 debounce 窗口内只发起一次同步，其余任务停在 waiting_sync。
package service

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
)

// runPipelineOnce 推进所有待处理任务。由 Start 里的 ticker 周期调用，也可手动触发。
func (s *CloudDownloadService) runPipelineOnce(ctx context.Context) {
	if s.repo == nil || s.repo.CloudDownload == nil {
		return
	}
	candidates, err := s.repo.CloudDownload.ListPipelineCandidates(ctx)
	if err != nil {
		s.log.Warn("cloud download pipeline: list candidates failed", zap.Error(err))
		return
	}
	if len(candidates) == 0 {
		return
	}
	groups := map[string][]model.CloudDownloadTask{}
	for _, t := range candidates {
		groups[t.SyncPathID] = append(groups[t.SyncPathID], t)
	}
	for syncPathID, tasks := range groups {
		s.advanceSyncPath(ctx, syncPathID, tasks)
	}
}

// advanceSyncPath 推进一个同步目录下的待处理任务。
func (s *CloudDownloadService) advanceSyncPath(ctx context.Context, syncPathID string, tasks []model.CloudDownloadTask) {
	path, err := s.repo.StrmSyncPath.FindByID(ctx, syncPathID)
	if err != nil {
		s.log.Warn("cloud download pipeline: load sync path failed",
			zap.String("path_id", syncPathID), zap.Error(err))
		return
	}
	if path == nil || !path.Enabled || path.Provider != model.StrmProvider115 {
		s.failStage(ctx, tasks, "同步目录不存在、已禁用或不是 115 网盘目录", model.CloudDownloadStageNA)
		return
	}

	// 同步进行中：本轮只标记等待，避免把 StartSync 的「正在同步中」错误打成失败。
	if s.strm != nil && s.strm.IsSyncRunning(syncPathID) {
		s.markWaiting(ctx, tasks)
		return
	}

	// 去抖窗口内不重复发起同步。
	if !s.debounceElapsed(syncPathID) {
		s.markWaiting(ctx, tasks)
		return
	}

	if s.strm == nil {
		s.failStage(ctx, tasks, "STRM 同步服务不可用", model.CloudDownloadStageFailed)
		return
	}
	if err := s.strm.StartSync(ctx, syncPathID); err != nil {
		// 最常见的竞态：刚判断完不忙，另一个人（或 cron）抢先启动。
		if strings.Contains(err.Error(), "正在同步中") {
			s.markWaiting(ctx, tasks)
			return
		}
		s.failStage(ctx, tasks, "触发 STRM 同步失败："+err.Error(), model.CloudDownloadStageFailed)
		return
	}
	s.noteTrigger(syncPathID)
	for _, t := range tasks {
		_ = s.repo.CloudDownload.UpdateFields(ctx, t.ID, map[string]any{
			"stage":       model.CloudDownloadStageSyncing,
			"stage_error": "",
		})
	}
}

// onStrmSyncFinished 是 StrmService 的同步完成回调。
// 只处理「因云下载而发起且当前处于 syncing」的任务：手动/Cron 同步也会触发
// 这个回调，没有云下载任务时直接返回。
func (s *CloudDownloadService) onStrmSyncFinished(ctx context.Context, syncPathID, status string) {
	if s.repo == nil || s.repo.CloudDownload == nil {
		return
	}
	tasks, err := s.repo.CloudDownload.ListBySyncPathAndStage(ctx, syncPathID, model.CloudDownloadStageSyncing)
	if err != nil {
		s.log.Warn("cloud download pipeline: list syncing tasks failed",
			zap.String("path_id", syncPathID), zap.Error(err))
		return
	}
	if len(tasks) == 0 {
		return
	}
	if status != model.StrmSyncRecordDone {
		for _, t := range tasks {
			_ = s.repo.CloudDownload.UpdateFields(ctx, t.ID, map[string]any{
				"stage":       model.CloudDownloadStageFailed,
				"stage_error": "STRM 同步" + strmStatusLabel(status),
			})
		}
		// 同步失败也要记一次触发时间，给用户一个冷静期，避免立即重试形成惊吓式循环。
		s.noteTrigger(syncPathID)
		return
	}

	path, err := s.repo.StrmSyncPath.FindByID(ctx, syncPathID)
	if err != nil || path == nil {
		s.failStage(ctx, tasks, "同步目录已被删除", model.CloudDownloadStageNA)
		return
	}
	root := s.matchLibraryRoot(ctx, path.LocalPath)
	if root == nil {
		s.failStage(ctx, tasks, "STRM 输出目录未加入任何媒体库（本地目录："+path.LocalPath+"）", model.CloudDownloadStageNA)
		return
	}

	for _, t := range tasks {
		_ = s.repo.CloudDownload.UpdateFields(ctx, t.ID, map[string]any{
			"stage":      model.CloudDownloadStageScanning,
			"library_id": root.LibraryID,
		})
	}

	scanAdded, scanErr := s.scanLibrary(ctx, root.LibraryID)
	if scanErr != nil {
		s.failStage(ctx, tasks, "扫描媒体库失败："+scanErr.Error(), model.CloudDownloadStageFailed)
		return
	}

	scrapeQueued := 0
	if s.autoScrape() && s.scraper != nil && s.scraper.AnyEnabled() {
		// EnqueueLibrary 默认只挑 scrape_status 为空的 pending 行，
		// 因此全库入队不会重刷已刮好的条目。
		if n, err := s.scraper.EnqueueLibrary(ctx, root.LibraryID, ScrapeOptions{}); err != nil {
			s.log.Warn("cloud download pipeline: enqueue scrape failed",
				zap.String("library_id", root.LibraryID), zap.Error(err))
		} else {
			scrapeQueued = n
		}
	}

	stageError := ""
	if s.autoScrape() && (s.scraper == nil || !s.scraper.AnyEnabled()) {
		stageError = "未配置任何刮削源，已跳过自动刮削"
	}

	for _, t := range tasks {
		_ = s.repo.CloudDownload.UpdateFields(ctx, t.ID, map[string]any{
			"stage":         model.CloudDownloadStageDone,
			"stage_error":   stageError,
			"library_id":    root.LibraryID,
			"scan_added":    scanAdded,
			"scrape_queued": scrapeQueued,
		})
	}
	s.log.Info("cloud download pipeline finished",
		zap.String("path_id", syncPathID),
		zap.String("library_id", root.LibraryID),
		zap.Int("tasks", len(tasks)),
		zap.Int("scan_added", scanAdded),
		zap.Int("scrape_queued", scrapeQueued))
}

// markWaiting 把任务置为等待同步。
func (s *CloudDownloadService) markWaiting(ctx context.Context, tasks []model.CloudDownloadTask) {
	for _, t := range tasks {
		if t.Stage == model.CloudDownloadStageWaitingSync {
			continue
		}
		_ = s.repo.CloudDownload.UpdateFields(ctx, t.ID, map[string]any{
			"stage":       model.CloudDownloadStageWaitingSync,
			"stage_error": "",
		})
	}
}

// failStage 把任务置为阶段失败/跳过。
func (s *CloudDownloadService) failStage(ctx context.Context, tasks []model.CloudDownloadTask, msg, stage string) {
	for _, t := range tasks {
		_ = s.repo.CloudDownload.UpdateFields(ctx, t.ID, map[string]any{
			"stage":       stage,
			"stage_error": msg,
		})
	}
}

// matchLibraryRoot 按 STRM 本地输出目录反查媒体库根路径。
// 命中条件：路径相同，或 localPath 位于 root.Path 之下（同一块盘的子目录也算）。
func (s *CloudDownloadService) matchLibraryRoot(ctx context.Context, localPath string) *model.LibraryRoot {
	localPath = strings.TrimSpace(localPath)
	if localPath == "" || s.repo.Library == nil {
		return nil
	}
	roots, err := s.repo.Library.ListEnabledRoots(ctx)
	if err != nil {
		s.log.Warn("cloud download pipeline: list library roots failed", zap.Error(err))
		return nil
	}
	local := filepath.Clean(localPath)
	for i := range roots {
		root := roots[i]
		candidate := filepath.Clean(strings.TrimSpace(root.Path))
		if candidate == "" {
			continue
		}
		if sameLibraryPath(candidate, local) {
			return &root
		}
		// 子目录判断也走同一套路径归一化（Windows 盘符映射/容器前缀）。
		withSep := candidate
		if !strings.HasSuffix(withSep, string(filepath.Separator)) {
			withSep += string(filepath.Separator)
		}
		if strings.HasPrefix(local, withSep) || strings.HasPrefix(cleanPathForVolumeMapping(local), cleanPathForVolumeMapping(withSep)) {
			return &root
		}
	}
	return nil
}

// scanLibrary 扫描媒体库并返回新增条目数。
func (s *CloudDownloadService) scanLibrary(ctx context.Context, libraryID string) (int, error) {
	if s.scanner == nil {
		return 0, nil
	}
	res, err := s.scanner.ScanLibrary(ctx, libraryID)
	if err != nil {
		return 0, err
	}
	if res == nil {
		return 0, nil
	}
	return res.Added, nil
}

// debounceElapsed 判断距上次触发是否已过去一个去抖窗口。
func (s *CloudDownloadService) debounceElapsed(syncPathID string) bool {
	window := s.debounceWindow()
	if window <= 0 {
		return true
	}
	s.debounceMu.Lock()
	defer s.debounceMu.Unlock()
	last, ok := s.lastTrigger[syncPathID]
	if !ok {
		return true
	}
	return time.Since(last) >= window
}

// noteTrigger 记录本次触发时间。
func (s *CloudDownloadService) noteTrigger(syncPathID string) {
	s.debounceMu.Lock()
	s.lastTrigger[syncPathID] = time.Now()
	s.debounceMu.Unlock()
}

// strmStatusLabel 把同步记录状态翻译成中文。
func strmStatusLabel(status string) string {
	switch status {
	case model.StrmSyncRecordFailed:
		return "失败"
	case model.StrmSyncRecordCanceled:
		return "被取消"
	case model.StrmSyncRecordDone:
		return "完成"
	default:
		return status
	}
}
