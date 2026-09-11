package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

func registerAuthedUISurfaceRoutes(authed *gin.RouterGroup, svc *service.Container) {
	authed.GET("/media/recent", recentMediaHandler(svc))
	authed.GET("/media/stats", mediaStatsHandler(svc))

	authed.GET("/danmaku/:id", getDanmakuHandler(svc))
	authed.GET("/danmaku/config", getDanmakuConfigHandler(svc))
	authed.PUT("/danmaku/settings", updateDanmakuSettingsHandler(svc))

	authed.GET("/watch-history", historyListHandler(svc))
	authed.GET("/watch-history/stats", historyStatsHandler(svc))
	authed.GET("/watch-history/continue", historyContinueHandler(svc))
	authed.DELETE("/watch-history", historyDeleteHandler(svc))
	authed.DELETE("/watch-history/:id", historyDeleteOneHandler(svc))

	authed.GET("/system/info", systemInfoHandler(svc))
	authed.GET("/system/status", systemStatusHandler(svc))

	authed.GET("/play-profiles", listPlayProfilesHandler(svc))
	authed.POST("/play-profiles", createPlayProfileHandler(svc))
	authed.PUT("/play-profiles/:id", updatePlayProfileHandler(svc))
	authed.POST("/play-profiles/:id/verify-pin", verifyPlayProfilePINHandler(svc))
	authed.DELETE("/play-profiles/:id", deletePlayProfileHandler(svc))
}

func registerAuthedSearchRoutes(authed *gin.RouterGroup, svc *service.Container) {
	authed.GET("/search", searchUnifiedHandler(svc))
	authed.GET("/search/advanced", searchAdvancedHandler(svc))
	authed.GET("/search/tmdb", searchTMDbHandler(svc))
}

func registerAuthedSystemExtraRoutes(authed *gin.RouterGroup, svc *service.Container) {
	authed.GET("/system/config", listSystemConfigHandler(svc))
	authed.GET("/settings/schema", schemaHandler(svc))
	authed.GET("/system/events/ticket", systemEventsTicketHandler(svc))
}

func registerAuthedStatsExtraRoutes(authed *gin.RouterGroup, svc *service.Container) {
	authed.GET("/stats/user/:id", statsUserHandler(svc))
	authed.GET("/stats/top-users", statsTopUsersHandler(svc))
	authed.POST("/stats/play", statsPlayHandler(svc))
}

func registerAuthedPlaylistExtraRoutes(authed *gin.RouterGroup, svc *service.Container) {
	authed.POST("/playlists/:id/reorder", reorderPlaylistHandler(svc))
	authed.DELETE("/playlists/:id/items/by-id/:item_id", deletePlaylistItemByIDHandler(svc))
}

func registerAuthedDLNAControlRoutes(authed *gin.RouterGroup, svc *service.Container) {
	authed.POST("/dlna/:uuid/play", dlnaPlayHandler(svc))
	authed.POST("/dlna/:uuid/pause", dlnaPauseHandler(svc))
	authed.POST("/dlna/:uuid/stop", dlnaStopHandler(svc))
	authed.GET("/dlna/:uuid/status", dlnaStatusHandler(svc))
}

func registerAuthedFavoriteAndMediaActionRoutes(authed *gin.RouterGroup, svc *service.Container) {
	authed.GET("/favorites", listFavoritesAliasHandler(svc))
	authed.POST("/media/:id/favorite", addMediaFavoriteHandler(svc))
	authed.DELETE("/media/:id/favorite", removeMediaFavoriteHandler(svc))
	authed.GET("/media/:id/favorite/status", getMediaFavoriteStatusHandler(svc))
	authed.POST("/media/:id/ai-scrape", requirePermission(svc, "can_rescrape"), aiScrapeMediaHandler(svc))
	authed.POST("/media/scrape/test", requirePermission(svc, "can_rescrape"), scrapeTestHandler(svc))
	authed.POST("/media/organize", requirePermission(svc, "can_manage_files"), organizeBulkHandler(svc))
}

func registerAuthedPlaybackExtraRoutes(authed *gin.RouterGroup, svc *service.Container) {
	authed.GET("/playback/:id/info", playbackInfoHandler(svc))
	authed.GET("/playback/:id/resume", playbackResumeHandler(svc))
	authed.POST("/playback/:id/progress", playbackProgressHandler(svc))
	authed.GET("/playback/:id/external-players", externalPlayersHandler(svc))
	authed.GET("/playback/:id/external-url", externalURLHandler(svc))
	authed.GET("/playback/transcode/:job_id/status", transcodeStatusHandler(svc))
}
