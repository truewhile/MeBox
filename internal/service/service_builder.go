package service

import (
	"context"
	"errors"
	"strings"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

type serviceContainerBuilder struct {
	cfg     *config.Config
	log     *zap.Logger
	repos   *repository.Container
	version string
	c       *Container
}

func newServiceContainer(cfg *config.Config, log *zap.Logger, repos *repository.Container, version string) *Container {
	ApplyRuntimeSettings(context.Background(), cfg, repos, log)

	builder := &serviceContainerBuilder{
		cfg:     cfg,
		log:     log,
		repos:   repos,
			version: normalizeSystemUpdateVersion(version),
			c: &Container{
				Version: normalizeSystemUpdateVersion(version),
				Cfg:     cfg,
				Log:     log,
				Repo:    repos,
			},
	}
	builder.startRealtimeServices()
	builder.initProviderServices()
	builder.initContentServices()
	builder.initAccessAndStorageServices()
	builder.initIdentityServices()
	builder.initImageProxy()
	builder.attachRuntimeContext()
	return builder.c
}

func (b *serviceContainerBuilder) startRealtimeServices() {
	b.c.WSHub = NewHub(b.log)
	helper.Go(b.log, "ws.hub", b.c.WSHub.Run)
	b.c.Tasks = NewTaskTrackerService(b.log, b.c.WSHub)
	b.c.SystemUpdate = NewSystemUpdateService(b.cfg, b.log, b.repos, b.c.Tasks, b.version)

	b.c.SSEHub = NewSSEHub(b.log)
	helper.Go(b.log, "sse.hub", b.c.SSEHub.Run)
}

func (b *serviceContainerBuilder) initProviderServices() {
	b.c.FFprobe = NewFFprobeService(b.cfg, b.log)
	b.c.Cache = NewRuntimeCacheService(b.cfg, b.log)
	b.configureMediaSearchBackend()

	b.c.Crypto = NewCryptoService(b.cfg.Secrets.JWTSecret, b.log)
	b.c.APIConfig = NewAPIConfigService(b.log, b.repos, b.c.Crypto)
	b.c.TMDb = NewTMDbProvider(b.cfg, b.log, b.c.APIConfig)
	b.c.Bangumi = NewBangumiProvider(b.cfg, b.log)
	b.c.TheTVDB = NewTheTVDBProvider(b.cfg, b.log)
	b.c.Douban = NewDoubanProvider(b.cfg, b.log)
	b.c.Fanart = NewFanartProvider(b.cfg, b.log)
	b.c.RecognitionWords = NewRecognitionWordsService(b.log, b.repos)
	b.c.Danmaku = NewDanmakuService(b.log, b.repos)

	adult := NewAdultProvider(b.log, b.c.APIConfig, b.repos)
	b.c.Scraper = NewScraperService(
		b.cfg, b.log, b.repos,
		b.c.TMDb, b.c.Bangumi, b.c.TheTVDB, b.c.Fanart,
		b.c.WSHub, adult,
	)
	b.c.Scraper.SetRuntimeCache(b.c.Cache)
	b.c.Scraper.SetDouban(b.c.Douban)
}

func (b *serviceContainerBuilder) configureMediaSearchBackend() {
	searchBackend := repository.NewOpenSearchMediaBackend(b.cfg.Search)
	if searchBackend == nil || b.repos == nil || b.repos.Media == nil {
		return
	}
	b.repos.Media.SetSearchBackend(searchBackend)
	if b.log != nil {
		b.log.Info("opensearch media search enabled", zap.String("index", b.cfg.Search.Index), zap.String("url", b.cfg.Search.OpenSearchURL))
	}
}

func (b *serviceContainerBuilder) initContentServices() {
	b.c.Organizer = NewOrganizerService(b.cfg, b.log, b.repos)
	b.c.Organizer.SetProbe(b.c.FFprobe)
	b.c.Organizer.SetScraper(b.c.Scraper)
	b.c.Transcoder = NewTranscoderService(b.cfg, b.log, b.repos, b.c.WSHub)
	b.c.Scan = NewScannerService(b.cfg, b.log, b.repos, b.c.WSHub, b.c.FFprobe, b.c.Scraper)
	b.c.Scan.SetOrganizer(b.c.Organizer)
	b.c.Scan.SetRuntimeCache(b.c.Cache)
	b.c.OrganizePipeline = NewOrganizePipelineService(b.log, b.repos, b.c.Organizer, b.c.Scan, b.c.Tasks)
	b.c.Watcher = NewWatcherService(b.log, b.repos, b.c.Scan)
	b.c.NFO = NewNFOService(b.log, b.repos)
	b.c.FileManager = NewFileManagerService(b.cfg, b.log, b.repos)
	b.c.DLNA = NewDLNAService(b.log)
	b.c.Storage = NewStorageService(b.log, b.repos)
	b.c.Emby = NewEmbyService(b.cfg, b.log, b.repos).SetTMDbProvider(b.c.TMDb).SetAdultProvider(b.c.Scraper.adult)
	b.c.EmbyRemote = NewEmbyRemoteService(b.cfg, b.log, b.repos, b.c.Crypto).SetRuntimeCache(b.c.Cache)
	b.c.Emby.SetEmbyRemote(b.c.EmbyRemote)
	b.c.Backup = NewBackupService(b.cfg, b.log, b.repos.DB)
	b.c.Media = NewMediaService(b.cfg, b.log, b.repos).SetRuntimeCache(b.c.Cache)
	b.c.Stream = NewStreamService(b.cfg, b.log, b.repos, b.c.Transcoder)
	b.c.Playback = NewPlaybackService(b.log, b.repos).SetEmbyRemote(b.c.EmbyRemote)
	b.c.Subtitle = NewSubtitleService(b.cfg, b.log, b.repos)
	b.c.Profile = NewProfileService(b.log, b.repos)
	b.c.Audit = NewAuditService(b.log, b.repos)
	b.c.Strm = NewStrmService(b.cfg, b.log, b.repos, b.c.Crypto)
	// ffmpeg/ffprobe 一键下载安装（data/tools/ffmpeg/）。
	b.c.FFTools = NewFFmpegToolsService(b.cfg, b.log, b.repos)
	// 弹幕 hash 识别需要把 strm 指向解析成可拉取的直链/本地路径。
	b.c.Danmaku.SetStrmResolver(b.c.Strm.ResolvePlay)
	// STRM 直连失败后的 HLS 转码：把 .strm 解析成 ffmpeg 可读取的本地路径或 HTTP 直链。
	b.c.Transcoder.SetStrmPlayTargetResolver(b.c.Strm.ResolvePlayTarget)
	b.c.Transcoder.SetProbe(b.c.FFprobe)
	b.c.Subtitle.SetStrmPlayTargetResolver(b.c.Strm.ResolvePlayTarget)
	// 弹幕识别需要把远程 Emby 条目解析为 Media 元数据及可拉取前 16MB 的直链 URL。
	if b.c.EmbyRemote != nil {
		b.c.Danmaku.SetRemoteMediaResolver(func(ctx context.Context, encodedID string) (*model.Media, string, error) {
			mountID, remoteID, ok := DecodeEmbyRemoteID(encodedID)
			if !ok {
				return nil, "", errors.New("invalid emby remote id")
			}
			mount, acct, err := b.c.EmbyRemote.ResolveMount(ctx, mountID)
			if err != nil || mount == nil || acct == nil {
				if err != nil {
					return nil, "", err
				}
				return nil, "", errors.New("emby mount or account not found")
			}
			m, err := b.c.EmbyRemote.RemoteMediaDetail(ctx, mount, acct, remoteID)
			if err != nil || m == nil {
				return nil, "", err
			}
			streamURL, _ := b.c.EmbyRemote.WebStreamURL(ctx, acct, remoteID)
			return m, streamURL, nil
		})
	}
}

func (b *serviceContainerBuilder) initAccessAndStorageServices() {
	b.c.PlayProfiles = NewPlayProfileService(b.log, b.repos)
	b.c.Permissions = NewPermissionService(b.log, b.repos)
	b.c.Database = NewDatabaseAdminService(b.cfg, b.log, b.repos, b.repos.DB)
	b.c.Emby.SetRuntimeCache(b.c.Cache)
	b.c.Emby.SetSubtitleService(b.c.Subtitle)
	b.c.Scheduler = NewSchedulerService(
		b.log, b.repos, b.c.Scan, b.c.Transcoder,
		b.c.Organizer, b.c.WSHub, b.cfg.Cache.CacheDir,
	)
	b.c.Scheduler.SetTaskTracker(b.c.Tasks)
	b.c.Scheduler.SetOrganizePipeline(b.c.OrganizePipeline)
	b.c.Scheduler.SetImagesMaxSizeMBProvider(func() int {
		if b.cfg == nil {
			return 0
		}
		return b.cfg.Cache.ImagesMaxSizeMB
	})
}

func (b *serviceContainerBuilder) initIdentityServices() {
	b.c.Token = NewTokenService(b.cfg, b.log, b.repos)
	b.c.Auth = NewAuthService(b.cfg, b.log, b.repos, b.c.Token, b.c.Permissions)
	b.c.Sessions = NewSessionTrackerService(b.log)
	b.c.Device = NewDeviceService(b.log, b.repos)
	b.c.Device.SetSessionTracker(b.c.Sessions)
	b.c.ApiConfig = NewApiConfigService(b.cfg, b.log, b.repos, b.c.Crypto)
}

func (b *serviceContainerBuilder) initImageProxy() {
	b.c.ImageProxy = NewImageProxy(b.cfg, b.log)
	b.c.ImageProxy.SetLibraryRootsProvider(b.libraryRoots)
	if b.c.EmbyRemote != nil {
		b.c.ImageProxy.SetAllowedRemoteHostsProvider(func() []string {
			return b.c.EmbyRemote.ConfiguredRemoteHosts(context.Background())
		})
	}
	b.c.Scan.SetImageProxy(b.c.ImageProxy)
	b.c.Scraper.SetImageProxy(b.c.ImageProxy)
}

func (b *serviceContainerBuilder) libraryRoots() []string {
	libs, err := b.repos.Library.List(context.Background())
	if err != nil {
		return nil
	}
	roots := make([]string, 0, len(libs))
	for _, l := range libs {
		if len(l.Roots) > 0 {
			for _, root := range l.Roots {
				if !root.Enabled || strings.TrimSpace(root.Path) == "" {
					continue
				}
				roots = append(roots, resolveMappedDestinationPath(root.Path))
			}
			continue
		}
		if strings.TrimSpace(l.Path) != "" {
			roots = append(roots, resolveMappedDestinationPath(l.Path))
		}
	}
	return roots
}

func (b *serviceContainerBuilder) attachRuntimeContext() {
	b.c.stopCtx, b.c.stopCancel = context.WithCancel(context.Background())
}
