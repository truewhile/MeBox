// Package service 包含 MeBox 的业务逻辑。
// Handler 反序列化 HTTP 请求，调用 Service 方法，然后序列化响应。
// Services 拥有所有横切策略（认证、扫描、转码等）且不直接处理 HTTP 类型。
package service

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/repository"
)

// Container 持有在启动时初始化的每个服务。Handler 接收指向它的指针并选择相关字段。
type Container struct {
	Version          string
	Cfg              *config.Config
	Log              *zap.Logger
	Repo             *repository.Container
	WSHub            *Hub
	SSEHub           *SSEHub
	Tasks            *TaskTrackerService
	Auth             *AuthService
	Media            *MediaService
	Scan             *ScannerService
	Stream           *StreamService
	Transcoder       *TranscoderService
	FFprobe          *FFprobeService
	TMDb             *TMDbProvider
	Bangumi          *BangumiProvider
	TheTVDB          *TheTVDBProvider
	Fanart           *FanartProvider
	Scraper          *ScraperService
	Playback         *PlaybackService
	Segments         *MediaSegmentService
	ImageProxy       *ImageProxy
	Watcher          *WatcherService
	Subtitle         *SubtitleService
	Profile          *ProfileService
	Audit            *AuditService
	NFO              *NFOService
	APIConfig        *APIConfigService
	Crypto           *CryptoService
	FileManager      *FileManagerService
	DLNA             *DLNAService
	Scheduler        *SchedulerService
	Storage          *StorageService
	Emby             *EmbyService
	EmbyRemote       *EmbyRemoteService
	Backup           *BackupService
	PlayProfiles     *PlayProfileService
	Permissions      *PermissionService
	SystemUpdate     *SystemUpdateService
	Organizer        *OrganizerService
	OrganizePipeline *OrganizePipelineService
	Douban           *DoubanProvider
	Token            *TokenService
	ApiConfig        *ApiConfigService
	Device           *DeviceService
	Telegram         *TelegramService
	TelegramExpiry   *TelegramExpiryWatcher
	Discovery        *MediaDiscoveryService
	Cache            *RuntimeCacheService
	Sessions         *SessionTrackerService
	RecognitionWords *RecognitionWordsService
	Danmaku          *DanmakuService
	Strm             *StrmService
	Cloud115         *Cloud115PlaybackService
	Database         *DatabaseAdminService
	FFTools          *FFmpegToolsService

	stopCtx    context.Context
	stopCancel context.CancelFunc

	// ReloadHTTPServer 由 cmd/server 注入。HTTPS 相关设置保存后，handler
	// 会调用它把 HTTP/HTTPS 监听热切换到最新配置；nil 表示未注入（测试环境）。
	ReloadHTTPServer func() error
}

// New 构建服务容器。
func New(cfg *config.Config, log *zap.Logger, repos *repository.Container) *Container {
	return NewWithVersion(cfg, log, repos, "dev")
}

// NewWithVersion 构建带应用版本信息的服务容器。
func NewWithVersion(cfg *config.Config, log *zap.Logger, repos *repository.Container, version string) *Container {
	return newServiceContainer(cfg, log, repos, version)
}

// Boot 启动后台工作进程（watcher, downloads poller, subscription scheduler）。
// 在 AutoMigrate 后调用一次。
func (c *Container) Boot() {
	if err := c.NormalizeLocalLibraryPaths(c.stopCtx); err != nil {
		c.Log.Warn("normalize local library paths failed", zap.Error(err))
	}
	if err := c.Watcher.Start(c.stopCtx); err != nil {
		c.Log.Warn("watcher start failed", zap.Error(err))
	}
	if err := c.APIConfig.SeedDefaults(c.stopCtx); err != nil {
		c.Log.Warn("api config seed failed", zap.Error(err))
	}
	helper.Go(c.Log, "service.warmMediaSearchIndex", func() { c.warmMediaSearchIndex(c.stopCtx) })

	// 启动调度器定时任务
	c.Scheduler.Start(c.stopCtx)

	// Telegram 通知轮询（未启用或未配置 Token 时直接返回）
	if c.Telegram != nil {
		c.Telegram.Start(c.stopCtx)
	}

	// 远程 Emby 挂载兼容迁移：清理已删账号的残留挂载；旧账号无挂载时自动全量挂载
	if c.EmbyRemote != nil {
		c.EmbyRemote.CleanupOrphanMounts(c.stopCtx)
		c.EmbyRemote.AutoSeedMounts(c.stopCtx)
	}

	// STRM 元数据下载/上传队列与定时同步巡检
	if c.Strm != nil {
		c.Strm.Start(c.stopCtx)
	}

	// 启动刮削队列后台消费者
	if c.Scraper != nil {
		c.Scraper.Start(c.stopCtx)
	}

	// Mgo 保号规则巡检：默认关闭，由管理员通过 Telegram Bot 命令开启。
	// 每天触发一次评估；规则里的窗口可随机，不固定。
	if c.Device != nil {
		helper.Go(c.Log, "service.inactivitySweeper", func() { c.runInactivitySweeper(c.stopCtx) })
	}
}

// Context is canceled when the service container is closing.
func (c *Container) Context() context.Context {
	if c == nil || c.stopCtx == nil {
		return context.Background()
	}
	return c.stopCtx
}

// runInactivitySweeper periodically runs the account-cleanup policy. Kept with
// the historical name to avoid churn in callers.
func (c *Container) runInactivitySweeper(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := c.Device.SweepAccountCleanup(ctx); err != nil {
				c.Log.Warn("account cleanup sweep failed", zap.Error(err))
			} else if n > 0 {
				c.Log.Info("account cleanup sweep removed accounts", zap.Int("count", n))
			}
		}
	}
}

// Close 释放 services 持有的任何资源（websocket hub, ffmpeg 转码, fsnotify, 后台轮询器）。
func (c *Container) Close() {
	if c.stopCancel != nil {
		c.stopCancel()
	}
	if c.Scheduler != nil {
		c.Scheduler.Stop()
	}
	if c.Strm != nil {
		c.Strm.Stop()
	}
	if c.Watcher != nil {
		c.Watcher.Stop()
	}
	if c.Transcoder != nil {
		c.Transcoder.StopAll()
	}
	if c.Cache != nil {
		_ = c.Cache.Close()
	}
	if c.WSHub != nil {
		c.WSHub.Stop()
	}
	if c.SSEHub != nil {
		c.SSEHub.Stop()
	}
	if c.Scheduler != nil {
		c.Scheduler.Stop()
	}
}
