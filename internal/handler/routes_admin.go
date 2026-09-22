// Package handler — admin-only routes.
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/service"
)

func registerAdminRoutes(api *gin.RouterGroup, cfg *config.Config, svc *service.Container) {
	admin := api.Group("/admin")
	admin.Use(middleware.AuthRequired(cfg.Secrets.JWTSecret), middleware.AdminRequired())
	registerAdminUserRoutes(admin, svc)
	registerAdminPermissionRoutes(admin, svc)
	registerAdminSystemRoutes(admin, svc)
	registerAdminBackupRoutes(admin, svc)
	registerAdminOrganizerRoutes(admin, svc)
	registerAdminAPIConfigRoutes(admin, svc)
	registerAdminRecognitionWordRoutes(admin, svc)
	registerAdminStrmRoutes(admin, svc)
	registerAdminScraperRoutes(admin, svc)
	registerAdminDatabaseRoutes(admin, svc)

	// FFmpeg/FFprobe 工具：状态查询 + 一键下载安装（自动匹配当前平台）。
	admin.GET("/tools/ffmpeg/status", ffToolsStatusHandler(svc))
	admin.POST("/tools/ffmpeg/install", ffToolsInstallHandler(svc))
}

func registerAdminScraperRoutes(admin *gin.RouterGroup, svc *service.Container) {
	admin.GET("/scraper/queue", listScrapeQueueHandler(svc))
	admin.POST("/scraper/queue/:id/cancel", cancelScrapeTaskHandler(svc))
	admin.POST("/scraper/queue/:id/retry", retryScrapeTaskHandler(svc))
	admin.DELETE("/scraper/queue/:id", deleteScrapeTaskHandler(svc))
	admin.POST("/scraper/queue/batch", batchActionScrapeTasksHandler(svc))
	admin.POST("/scraper/queue/clear-done", clearDoneScrapeTasksHandler(svc))
	admin.POST("/scraper/queue/clear-finished", clearFinishedScrapeTasksHandler(svc))
	admin.POST("/scraper/queue/clear-canceled", clearCanceledScrapeTasksHandler(svc))
	admin.POST("/scraper/queue/clear-failed", clearFailedScrapeTasksHandler(svc))
	admin.POST("/scraper/queue/retry-failed", retryAllFailedScrapeTasksHandler(svc))
	admin.POST("/scraper/queue/cancel-pending", cancelPendingScrapeTasksHandler(svc))
	admin.POST("/scraper/queue/enqueue-library/:id", enqueueLibraryScrapeHandler(svc))
	admin.POST("/scraper/queue/enqueue-all", enqueueAllScrapeHandler(svc))
	admin.POST("/media/repair-rescrape", enqueueAllScrapeHandler(svc))
	admin.POST("/libraries/:id/repair-rescrape", enqueueLibraryScrapeHandler(svc))
}

func registerAdminStrmRoutes(admin *gin.RouterGroup, svc *service.Container) {
	// Emby 挂载管理：远程 Emby 媒体库挂载（账号复用 strm/accounts）
	admin.GET("/emby/accounts/:id/views", embyAccountViewsHandler(svc))
	admin.POST("/emby/accounts/:id/full-mount", fullMountEmbyAccountHandler(svc))
	admin.GET("/emby/mounts", listEmbyMountsHandler(svc))
	admin.POST("/emby/mounts", createEmbyMountsHandler(svc))
	admin.PUT("/emby/mounts/reorder", reorderEmbyMountsHandler(svc))
	admin.PUT("/emby/mounts/:id", updateEmbyMountHandler(svc))
	admin.DELETE("/emby/mounts/:id", deleteEmbyMountHandler(svc))

	admin.GET("/strm/accounts", listStrmAccountsHandler(svc))
	admin.POST("/strm/accounts", createStrmAccountHandler(svc))
	admin.PUT("/strm/accounts/:id", updateStrmAccountHandler(svc))
	admin.DELETE("/strm/accounts/:id", deleteStrmAccountHandler(svc))
	admin.POST("/strm/accounts/:id/test", testStrmAccountHandler(svc))
	admin.GET("/strm/accounts/:id/list", listStrmRemoteDirHandler(svc))
	admin.GET("/strm/accounts/:id/resolve", resolveStrmRemoteDirHandler(svc))
	admin.GET("/strm/115/sources", listStrm115SourcesHandler(svc))
	admin.POST("/strm/accounts/:id/oauth/start", startStrm115OAuthHandler(svc))
	admin.POST("/strm/accounts/:id/oauth/poll", pollStrm115OAuthHandler(svc))

	admin.GET("/strm/settings", getStrmSettingsHandler(svc))
	admin.PUT("/strm/settings", updateStrmSettingsHandler(svc))

	admin.GET("/strm/paths", listStrmSyncPathsHandler(svc))
	admin.POST("/strm/paths", createStrmSyncPathHandler(svc))
	admin.PUT("/strm/paths/:id", updateStrmSyncPathHandler(svc))
	admin.DELETE("/strm/paths/:id", deleteStrmSyncPathHandler(svc))
	admin.POST("/strm/paths/:id/sync", startStrmSyncHandler(svc))
	admin.POST("/strm/paths/:id/cancel", cancelStrmSyncHandler(svc))
	admin.GET("/strm/records", listStrmSyncRecordsHandler(svc))
	admin.DELETE("/strm/records/:id", deleteStrmSyncRecordHandler(svc))
	admin.DELETE("/strm/records", clearStrmSyncRecordsHandler(svc))
	admin.GET("/strm/local-dirs", listStrmLocalDirsHandler(svc))

	admin.GET("/strm/downloads", downloadQueueHandler(svc))
	admin.POST("/strm/downloads/:id/cancel", cancelStrmDownloadHandler(svc))
	admin.POST("/strm/downloads/:id/retry", retryStrmDownloadHandler(svc))
	admin.DELETE("/strm/downloads/:id", deleteStrmDownloadHandler(svc))
	admin.POST("/strm/downloads/batch", batchActionDownloadsHandler(svc))
	admin.POST("/strm/downloads/clear-done", clearDoneDownloadsHandler(svc))
	admin.POST("/strm/downloads/clear-finished", clearFinishedDownloadsHandler(svc))
	admin.POST("/strm/downloads/clear-canceled", clearCanceledDownloadsHandler(svc))
	admin.POST("/strm/downloads/clear-failed", clearFailedDownloadsHandler(svc))
	admin.POST("/strm/downloads/retry-failed", retryAllFailedDownloadsHandler(svc))
	admin.POST("/strm/downloads/cancel-pending", cancelPendingDownloadsHandler(svc))
	admin.GET("/strm/uploads", uploadQueueHandler(svc))
	admin.POST("/strm/uploads/:id/cancel", cancelStrmUploadHandler(svc))
	admin.POST("/strm/uploads/:id/retry", retryStrmUploadHandler(svc))
	admin.DELETE("/strm/uploads/:id", deleteStrmUploadHandler(svc))
	admin.POST("/strm/uploads/batch", batchActionUploadsHandler(svc))
	admin.POST("/strm/uploads/clear-done", clearDoneUploadsHandler(svc))
	admin.POST("/strm/uploads/clear-finished", clearFinishedUploadsHandler(svc))
	admin.POST("/strm/uploads/clear-canceled", clearCanceledUploadsHandler(svc))
	admin.POST("/strm/uploads/clear-failed", clearFailedUploadsHandler(svc))
	admin.POST("/strm/uploads/retry-failed", retryAllFailedUploadsHandler(svc))
	admin.POST("/strm/uploads/cancel-pending", cancelPendingUploadsHandler(svc))
}

func registerAdminUserRoutes(admin *gin.RouterGroup, svc *service.Container) {
	admin.GET("/users/limit", getUserLimitHandler(svc))
	admin.PUT("/users/limit", updateUserLimitHandler(svc))
	admin.GET("/users", listUsersHandler(svc))
	admin.POST("/users", createUserHandler(svc))
	admin.PATCH("/users/:id", updateUserHandler(svc))
	admin.PATCH("/users/:id/password", resetUserPasswordHandler(svc))
	admin.PATCH("/users/:id/status", updateUserStatusHandler(svc))
	admin.PATCH("/users/:id/role", adminUpdateRoleHandler(svc))
	admin.PATCH("/users/:id/libraries", updateUserLibrariesHandler(svc))
	admin.DELETE("/users/:id", deleteUserHandler(svc))
	// 设备代管：管理员可查看并踢掉任意用户的设备。
	admin.GET("/users/:id/devices", adminUserDevicesHandler(svc))
	admin.POST("/users/:id/devices/kick-all", adminKickAllUserDevicesHandler(svc))
	admin.POST("/users/:id/devices/:deviceID/kick", adminKickUserDeviceHandler(svc))
	admin.GET("/settings", listSettingsHandler(svc))
	admin.PUT("/settings", updateSettingHandler(svc))
	admin.POST("/telegram/test", testTelegramHandler(svc))
	admin.POST("/adult/test-scraper", testAdultScraperHandler(svc))
	admin.GET("/logs", recentLogsHandler(svc))
}

func registerAdminPermissionRoutes(admin *gin.RouterGroup, svc *service.Container) {
	admin.GET("/users/:id/permissions", getUserPermissionsHandler(svc))
	admin.PUT("/users/:id/permissions", updateUserPermissionsHandler(svc))
	admin.POST("/users/:id/permissions/reset", resetUserPermissionsHandler(svc))
}

func registerAdminSystemRoutes(admin *gin.RouterGroup, svc *service.Container) {
	admin.GET("/system/update", systemUpdateStatusHandler(svc))
	admin.POST("/system/update/check", systemUpdateCheckHandler(svc))
	admin.POST("/system/update/apply", systemUpdateApplyHandler(svc))
}

func registerAdminBackupRoutes(admin *gin.RouterGroup, svc *service.Container) {
	admin.GET("/backups", listBackupsHandler(svc))
	admin.POST("/backups", createBackupHandler(svc))
	admin.DELETE("/backups", deleteBackupHandler(svc))
	admin.POST("/backups/restore", restoreBackupHandler(svc))
}

func registerAdminOrganizerRoutes(admin *gin.RouterGroup, svc *service.Container) {
	admin.POST("/media/:id/organize", organizeMediaHandler(svc))
	admin.POST("/libraries/:id/organize", organizeLibraryHandler(svc))
	admin.GET("/organize/sources", organizeSourcesHandler(svc))
	admin.POST("/organize/source", organizeDirectoryHandler(svc))
}

func registerAdminAPIConfigRoutes(admin *gin.RouterGroup, svc *service.Container) {
	admin.GET("/api-configs", listAPIConfigsHandler(svc))
	admin.GET("/api-configs/:provider", getAPIConfigHandler(svc))
	admin.PUT("/api-configs/:provider", updateAPIConfigHandler(svc))
	admin.DELETE("/api-configs/:provider", deleteAPIConfigHandler(svc))
	admin.POST("/api-configs/:provider/test", testAPIConfigHandler(svc))
}

func registerAdminRecognitionWordRoutes(admin *gin.RouterGroup, svc *service.Container) {
	admin.GET("/recognition-words", getRecognitionWordsHandler(svc))
	admin.PUT("/recognition-words", saveRecognitionWordsHandler(svc))
	admin.POST("/recognition-words/sync", syncRecognitionWordsHandler(svc))
	admin.POST("/recognition-words/test", testRecognitionWordsHandler(svc))
}

func registerAdminDatabaseRoutes(admin *gin.RouterGroup, svc *service.Container) {
	admin.GET("/database/status", getDatabaseStatusHandler(svc))
	admin.POST("/database/test", testDatabaseHandler(svc))
	admin.POST("/database/migrate", migrateDatabaseHandler(svc))
	admin.POST("/database/save-config", saveDatabaseConfigHandler(svc))
}
