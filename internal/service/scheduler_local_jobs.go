package service

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// jobScanLibraries re-walks every enabled library.
//
// 默认关闭：文件变更由 WatcherService 增量入库，无需周期性全量重扫。
// 仅当用户在设置中显式开启 scan.periodic_enabled 时才执行整库重扫，
// 避免对硬盘的高频反复读取造成损伤（用户明确要求）。
func (s *SchedulerService) jobScanLibraries(ctx context.Context) error {
	manual, _ := ctx.Value(schedulerManualRunKey{}).(bool)
	now := s.currentTime()
	if !manual && !s.periodicScanDue(ctx, now) {
		return nil
	}
	libs, err := s.repo.Library.List(ctx)
	if err != nil {
		return err
	}
	for _, l := range libs {
		if !l.Enabled {
			continue
		}
		if _, err := s.scanner.ScanLibrary(ctx, l.ID); err != nil {
			s.log.Warn("scheduled scan failed",
				zap.String("library", l.ID), zap.Error(err))
		}
	}
	if !manual {
		_ = s.markPeriodicScanCompleted(ctx, now)
	}
	return nil
}

// periodicScanEnabled reports whether the operator opted into periodic full
// library re-scans. Defaults to false so the incremental watcher is the only
// thing touching the disk under normal operation.
func (s *SchedulerService) periodicScanEnabled(ctx context.Context) bool {
	if s.repo == nil || s.repo.Setting == nil {
		return false
	}
	v, err := s.repo.Setting.Get(ctx, "scan.periodic_enabled")
	if err != nil {
		return false
	}
	return parseBoolSetting(v, false)
}

func (s *SchedulerService) periodicScanDue(ctx context.Context, now time.Time) bool {
	if !s.periodicScanEnabled(ctx) {
		return false
	}
	if s.repo == nil || s.repo.Setting == nil {
		return true
	}
	last, err := s.repo.Setting.Get(ctx, localLastPeriodicScanDateKey)
	if err != nil {
		return true
	}
	return strings.TrimSpace(last) != now.In(time.Local).Format("2006-01-02")
}

func (s *SchedulerService) markPeriodicScanCompleted(ctx context.Context, now time.Time) error {
	if s.repo == nil || s.repo.Setting == nil {
		return nil
	}
	return s.repo.Setting.Set(ctx, localLastPeriodicScanDateKey, now.In(time.Local).Format("2006-01-02"))
}

// jobOrganizeSource periodically organizes the configured staging/download
// source directory into the configured media destination. It is intentionally
// opt-in: manual file management remains available, but background disk walking
// only starts after the operator enables organize.auto.
func (s *SchedulerService) jobOrganizeSource(ctx context.Context) error {
	manual, _ := ctx.Value(schedulerManualRunKey{}).(bool)
	if s.organizer == nil || (!manual && !s.autoOrganizeSourceEnabled(ctx)) {
		return nil
	}
	taskName := "自动整理重命名刮削入库"
	if manual {
		taskName = "手动触发自动整理重命名刮削入库"
	}
	resWrap, err := s.ensureOrganizePipeline().Run(ctx, OrganizePipelineRequest{
		Scope:    OrganizeScopeDirectory,
		Trigger:  OrganizeTriggerScheduled,
		TaskName: taskName,
	})
	if err != nil {
		return err
	}
	res := resWrap.Result
	if res == nil {
		res = &OrganizeResult{}
	}
	if s.log != nil && res != nil {
		s.log.Info("scheduled source organize finished",
			zap.String("source", res.SourcePath),
			zap.String("dest", res.DestPath),
			zap.Int("organized", res.Organized),
			zap.Int("replaced", res.Replaced),
			zap.Int("skipped", res.Skipped),
			zap.Int("scrapes", len(res.Scrapes)),
			zap.Int("errors", len(res.Errors)),
		)
	}
	return nil
}

func (s *SchedulerService) ensureOrganizePipeline() *OrganizePipelineService {
	if s.organizePipeline != nil {
		return s.organizePipeline
	}
	return NewOrganizePipelineService(s.log, s.repo, s.organizer, s.scanner, s.tasks)
}

func (s *SchedulerService) startScheduledOrganizeTask(ctx context.Context, manual bool) *TaskHandle {
	if s == nil || s.tasks == nil {
		return nil
	}
	name := "自动整理重命名入库"
	message := "正在执行计划自动整理/重命名/入库"
	if manual {
		name = "手动触发自动整理重命名入库"
		message = "正在执行手动触发的自动整理/重命名/入库"
	}
	return s.tasks.Start(TaskKindOrganize, name, TaskUpdate{
		Stage:      "organize",
		SourcePath: s.organizer.defaultSourceRoot(ctx, ""),
		DestPath:   s.organizer.defaultDestRoot(ctx, ""),
		Message:    message,
	})
}

func (s *SchedulerService) autoOrganizeSourceEnabled(ctx context.Context) bool {
	if s.repo == nil || s.repo.Setting == nil {
		return false
	}
	v, err := s.repo.Setting.Get(ctx, "organize.auto")
	if err != nil {
		return false
	}
	return parseBoolSetting(v, false)
}

func (s *SchedulerService) organizeSourceInterval(ctx context.Context) time.Duration {
	const fallback = 5 * time.Minute
	if s.repo == nil || s.repo.Setting == nil {
		return fallback
	}
	v, err := s.repo.Setting.Get(ctx, "organize.interval_seconds")
	if err != nil {
		return fallback
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || seconds <= 0 {
		return fallback
	}
	if seconds < 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

// jobTelegramExpiryWarning 每日巡检即将到期的账号并提醒用户。
func (s *SchedulerService) jobTelegramExpiryWarning(ctx context.Context) error {
	if s.expiryWatcher == nil {
		return nil
	}
	return s.expiryWatcher.RunOnce(ctx)
}

// jobCleanReaderContentCache 触发阅读正文缓存的 TTL / 容量淘汰。
func (s *SchedulerService) jobCleanReaderContentCache(ctx context.Context) error {
	if s.readerContentCleaner == nil {
		return nil
	}
	s.readerContentCleaner(ctx)
	return nil
}

// readerFilesRetentionMax 书源文件缓存（java.cacheFile / downloadFile）的保底保留时长。
// 未配置 TTL 时用它兜底：字体、静态 JS 这类文件会被书源长期复用，不能删太早。
const readerFilesRetentionMax = 30 * 24 * time.Hour

// readerTempOrphanAge 临时产物的清理阈值：正常写入是「临时文件 + 立刻 rename」，
// 存活超过这个时长的只可能是进程被杀/断电留下的孤儿（正在写的文件 mtime 始终在刷新）。
const readerTempOrphanAge = 24 * time.Hour

// jobCleanReaderFiles 清理阅读相关缓存里没有其它机制管的部分：
//   - cache/reader/files：书源 java.cacheFile / java.downloadFile 落盘的文件
//     （字体、静态 JS 等），按保留时长淘汰——此前完全没有清理，会无限增长；
//   - 孤儿临时文件：转码的 *.mp3.part 与正文缓存的 .content-*，写入中断后会残留。
func (s *SchedulerService) jobCleanReaderFiles(ctx context.Context) error {
	if s.cacheDir == "" {
		return nil
	}
	retention := s.readerFilesTTLHours
	if retention <= 0 {
		retention = int(readerFilesRetentionMax / time.Hour)
	}
	if err := walkAndPrune(filepath.Join(s.cacheDir, "reader", "files"),
		time.Now().Add(-time.Duration(retention)*time.Hour)); err != nil {
		return err
	}
	// 转码缓存的 tmp 是 <目标>.mp3.part，与成品同目录。
	if err := pruneOrphanTempFiles(filepath.Join(s.cacheDir, "reader-audio")); err != nil {
		return err
	}
	// 正文缓存的临时文件有固定前缀。
	return pruneOrphanTempFiles(filepath.Join(s.cacheDir, "reader-content"))
}

// pruneOrphanTempFiles 删除缓存目录里超龄的临时文件（*.part / .content-*）。
func pruneOrphanTempFiles(root string) error {
	if root == "" {
		return nil
	}
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	cutoff := time.Now().Add(-readerTempOrphanAge)
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".part") && !strings.HasPrefix(name, ".content-") {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path) // #nosec G122 -- 缓存目录内的孤儿临时文件，尽力清理
		}
		return nil
	})
}

// jobCleanTranscodeCache deletes HLS artefacts older than 24h.
func (s *SchedulerService) jobCleanTranscodeCache(ctx context.Context) error {
	if s.cacheDir == "" {
		return nil
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	return walkAndPrune(s.cacheDir+"/hls", cutoff)
}

// jobCleanImageCache prunes the image proxy cache: 原图按保留时长与独立配额
// 优先淘汰，总量超限时再淘汰派生成品。
func (s *SchedulerService) jobCleanImageCache(ctx context.Context) error {
	if s.cacheDir == "" {
		return nil
	}
	policy := s.imageCachePolicy()
	if policy.TotalBytes <= 0 && policy.OriginalsBytes <= 0 && policy.OriginalsAge <= 0 {
		return nil
	}
	imagesDir := filepath.Join(s.cacheDir, "images")
	pools := ImageCachePools(imagesDir, policy.OriginalsBytes, policy.OriginalsAge)
	res, err := PruneImageCachePools(pools, policy.TotalBytes)
	if err != nil {
		if s.log != nil {
			s.log.Warn("scheduled image cache cleanup failed", zap.Error(err))
		}
		return err
	}
	if res.DeletedFiles > 0 && s.log != nil {
		s.log.Info("scheduled image cache cleanup completed",
			zap.Int("deleted_files", res.DeletedFiles),
			zap.Int64("freed_bytes", res.FreedBytes),
			zap.Int64("remaining_bytes", res.RemainingBytes),
		)
	}
	return nil
}
