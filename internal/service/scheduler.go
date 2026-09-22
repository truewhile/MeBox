// Package service — periodic scheduled jobs.
//
// SchedulerService runs recurring background jobs that keep the
// library up-to-date without operator intervention:
//
//	library_scan      every 24 h  — optional full re-scan for local libraries;
//	                                  filesystem watchers handle normal changes.
//	organize_source   opt-in        — organize the configured staging folder.
//	transcode_cleanup every 24 h   — purge HLS transcode artefacts
//	                                  older than 24 h.
//
// Each job runs at most once at a time (an in-flight run blocks the
// next tick). All work happens on a long-lived background context so
// the operator can keep clicking around the UI while the watchdog runs.
package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/repository"
)

// SchedulerService runs the periodic jobs.
type SchedulerService struct {
	log              *zap.Logger
	repo             *repository.Container
	scanner          *ScannerService
	transcoder       *TranscoderService
	organizer        *OrganizerService
	organizePipeline *OrganizePipelineService
	hub              *Hub
	tasks            *TaskTrackerService
	expiryWatcher    *TelegramExpiryWatcher
	cacheDir         string
	now              func() time.Time

	imagesPolicyProvider func() ImageCachePolicy

	mu     sync.Mutex
	stopCh chan struct{}
	jobs   []*scheduledJob
}

var (
	ErrSchedulerJobNotFound       = errors.New("scheduled job not found")
	ErrSchedulerJobAlreadyRunning = errors.New("scheduled job already running")
)

func (s *SchedulerService) SetTaskTracker(tasks *TaskTrackerService) {
	s.tasks = tasks
}

func (s *SchedulerService) SetOrganizePipeline(pipeline *OrganizePipelineService) {
	s.organizePipeline = pipeline
}

// SetExpiryWatcher 注入账号到期巡检。未注入时（例如测试）该任务不注册。
func (s *SchedulerService) SetExpiryWatcher(watcher *TelegramExpiryWatcher) {
	s.expiryWatcher = watcher
}

// ImageCachePolicy 是一次图片缓存清理要用的策略（全部为 0 表示不做任何清理）。
type ImageCachePolicy struct {
	TotalBytes     int64
	OriginalsBytes int64
	OriginalsAge   time.Duration
}

func (s *SchedulerService) SetImageCachePolicyProvider(fn func() ImageCachePolicy) {
	s.imagesPolicyProvider = fn
}

func (s *SchedulerService) imageCachePolicy() ImageCachePolicy {
	if s.imagesPolicyProvider != nil {
		return s.imagesPolicyProvider()
	}
	return ImageCachePolicy{}
}

// scheduledJob is one recurring task.
type scheduledJob struct {
	name     string
	interval time.Duration
	run      func(ctx context.Context) error
	lastRun  time.Time
	lastErr  string
	running  bool
	started  time.Time
}

type schedulerManualRunKey struct{}

const localLastPeriodicScanDateKey = "scan.last_periodic_date"

// NewSchedulerService is the constructor.
func NewSchedulerService(
	log *zap.Logger,
	repo *repository.Container,
	scanner *ScannerService,
	transcoder *TranscoderService,
	organizer *OrganizerService,
	hub *Hub,
	cacheDir string,
) *SchedulerService {
	return &SchedulerService{
		log:        log,
		repo:       repo,
		scanner:    scanner,
		transcoder: transcoder,
		organizer:  organizer,
		hub:        hub,
		cacheDir:   cacheDir,
		now:        time.Now,
		stopCh:     make(chan struct{}),
	}
}

// Start kicks off every job in its own goroutine and returns immediately.
func (s *SchedulerService) Start(ctx context.Context) {
	s.jobs = []*scheduledJob{
		{
			name:     "library_scan",
			interval: 24 * time.Hour,
			run:      s.jobScanLibraries,
		},
		{
			name:     "organize_source",
			interval: s.organizeSourceInterval(ctx),
			run:      s.jobOrganizeSource,
		},
		{
			name:     "transcode_cleanup",
			interval: 24 * time.Hour,
			run:      s.jobCleanTranscodeCache,
		},
		{
			name:     "image_cache_cleanup",
			interval: 1 * time.Hour,
			run:      s.jobCleanImageCache,
		},
	}
	// 到期提醒只在配置了巡检器时注册，避免测试与未启用通知的部署跑空转任务。
	if s.expiryWatcher != nil {
		s.jobs = append(s.jobs, &scheduledJob{
			name:     "telegram_expiry_warning",
			interval: 24 * time.Hour,
			run:      s.jobTelegramExpiryWarning,
		})
	}
	for _, j := range s.jobs {
		initialDelay := 15 * time.Second
		if j.name == "library_scan" || j.name == "organize_source" {
			// 重启后不立即整库重扫/整理下载目录：更新窗口恰是登录高峰，
			// 15 秒即全量 walk + ffprobe 曾把 CPU/磁盘打满导致无法登录。
			// 首轮等满一个完整周期再跑，平时节奏不变。
			initialDelay = j.interval
		}
		helper.Go(s.log, "scheduler.loop."+j.name, func() { s.loopWithInitialDelay(ctx, j, initialDelay) })
	}
}

// Stop signals every job loop to exit on the next tick.
func (s *SchedulerService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stopCh:
		// already closed
	default:
		close(s.stopCh)
	}
}
