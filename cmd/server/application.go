package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/database"
	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

// application owns the long-running MeBox server and all resources created
// during startup. Platform entry points decide how the application is
// controlled: a console signal loop or a Windows notification-area icon.
type application struct {
	cfg              *config.Config
	logger           *zap.Logger
	embyCompatLogger *zap.Logger
	serverManager    *serverManager
	services         *service.Container

	closeMu     sync.Mutex
	closeFuncs  []func()
	shutdown    sync.Once
	shutdownErr error
}

func newApplication() (*application, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("config load failed: %w", err)
	}

	logger, closeLogger, err := newLoggerWithCloser(cfg)
	if err != nil {
		return nil, fmt.Errorf("logger init failed: %w", err)
	}

	app := &application{
		cfg:        cfg,
		logger:     logger,
		closeFuncs: []func(){closeLogger},
	}
	if err := app.start(); err != nil {
		app.closeLoggers()
		return nil, err
	}
	return app, nil
}

func (a *application) start() error {
	embyCompatLogger, closeEmbyCompatLogger, err := newEmbyCompatLogger(a.cfg)
	if err != nil {
		a.logger.Error("Emby compatibility logger init failed", zap.Error(err))
		return fmt.Errorf("Emby compatibility logger init failed: %w", err)
	}
	a.embyCompatLogger = embyCompatLogger
	a.addCloser(closeEmbyCompatLogger)

	appVersion := effectiveVersion(version)
	a.logger.Info("starting MeBox",
		zap.String("version", appVersion),
		zap.Int("port", a.cfg.App.Port),
		zap.String("data_dir", a.cfg.App.DataDir),
		zap.String("emby_compat_log", embyCompatLogPath(a.cfg)),
	)

	for _, dir := range []string{a.cfg.App.DataDir, a.cfg.Cache.CacheDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			a.logger.Error("create dir failed", zap.String("dir", dir), zap.Error(err))
			return fmt.Errorf("create dir %s failed: %w", dir, err)
		}
	}

	db, err := database.Open(a.cfg, a.logger)
	if err != nil {
		a.logger.Error("database open failed", zap.Error(err))
		return fmt.Errorf("database open failed: %w", err)
	}
	if err := waitForDatabase(db, a.logger); err != nil {
		a.logger.Error("database not ready", zap.Error(err))
		return fmt.Errorf("database not ready: %w", err)
	}
	if err := database.AutoMigrate(db); err != nil {
		a.logger.Error("auto-migrate failed", zap.Error(err))
		return fmt.Errorf("auto-migrate failed: %w", err)
	}
	if err := database.MigrateSQLiteToCurrentIfNeeded(a.cfg, db, a.logger); err != nil {
		a.logger.Error("sqlite to postgres migration failed", zap.Error(err))
		return fmt.Errorf("sqlite to postgres migration failed: %w", err)
	}

	repos := repository.New(db)
	service.ApplyRuntimeSettings(context.Background(), a.cfg, repos, a.logger)
	applyCPUThreadLimit(a.cfg, a.logger)
	a.services = service.NewWithVersion(a.cfg, a.logger, repos, appVersion)

	// 一次性清洗历史脏数据: 老版本把单集 episode id / 单集名写进整剧字段, 导致
	// 同一部剧被拆成多张单集卡。清空被污染的字段并重置为 pending(借后续重刮修正)。
	if cleaned, err := a.services.NormalizePollutedEpisodeMetadata(context.Background()); err != nil {
		a.logger.Warn("polluted episode metadata cleanup failed", zap.Error(err))
	} else if cleaned > 0 {
		a.logger.Info("polluted episode metadata cleanup completed", zap.Int("media_count", cleaned))
	}

	if err := a.services.Auth.SeedAdmin(context.Background()); err != nil {
		a.logger.Warn("seed admin failed", zap.Error(err))
	}

	router := buildRouter(a.cfg, a.logger, a.embyCompatLogger, a.services)
	a.serverManager = newServerManager(a.cfg, a.logger, router)
	a.services.ReloadHTTPServer = a.serverManager.Reload
	if err := a.serverManager.Start(); err != nil {
		a.logger.Error("listen failed", zap.Error(err))
		return fmt.Errorf("listen failed: %w", err)
	}

	go func() {
		scheme := "http"
		if a.cfg.App.HTTPSEnabled {
			scheme = "https"
		}
		if publicIP := getPublicIP(3 * time.Second); publicIP != "" {
			a.logger.Info("server public endpoint",
				zap.String("public", fmt.Sprintf("%s://%s:%d", scheme, publicIP, a.cfg.App.Port)),
			)
		}
	}()
	helper.Go(a.logger, "services.boot", a.services.Boot)
	return nil
}

// Shutdown is idempotent and safe to call from both the tray handler and the
// systray exit callback.
func (a *application) Shutdown() error {
	a.shutdown.Do(func() {
		a.logger.Info("shutdown requested")

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if a.serverManager != nil {
			if err := a.serverManager.Shutdown(ctx); err != nil {
				a.shutdownErr = err
				a.logger.Error("graceful shutdown failed", zap.Error(err))
			}
		}
		cancel()

		if a.services != nil {
			a.services.Close()
		}
		a.logger.Info("MeBox stopped")
		_ = a.logger.Sync()
		if a.embyCompatLogger != nil {
			_ = a.embyCompatLogger.Sync()
		}
		a.closeLoggers()
	})
	return a.shutdownErr
}

func (a *application) addCloser(fn func()) {
	if fn == nil {
		return
	}
	a.closeMu.Lock()
	a.closeFuncs = append(a.closeFuncs, fn)
	a.closeMu.Unlock()
}

func (a *application) closeLoggers() {
	a.closeMu.Lock()
	closers := append([]func(){}, a.closeFuncs...)
	a.closeFuncs = nil
	a.closeMu.Unlock()

	for i := len(closers) - 1; i >= 0; i-- {
		closers[i]()
	}
}

func (a *application) localURL() string {
	config.RuntimeMu.RLock()
	scheme := "http"
	if a.cfg.App.HTTPSEnabled {
		scheme = "https"
	}
	port := a.cfg.App.Port
	config.RuntimeMu.RUnlock()
	return fmt.Sprintf("%s://127.0.0.1:%d", scheme, port)
}
