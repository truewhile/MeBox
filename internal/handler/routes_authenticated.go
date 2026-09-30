// Package handler — authenticated application routes.
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/service"
)

func registerAuthenticatedRoutes(api *gin.RouterGroup, cfg *config.Config, svc *service.Container) {
	authed := api.Group("/")
	authed.Use(middleware.AuthRequired(cfg.Secrets.JWTSecret))
	authed.Use(activeUserRequired(svc))

	registerAuthedUserAndLicenseRoutes(authed, svc)
	registerAuthedLibraryRoutes(authed, svc)
	registerAuthedMediaRoutes(authed, svc)
	registerAuthedPlaybackAndProxyRoutes(authed, svc)
	registerAuthedCollectionRoutes(authed, svc)
	registerAuthedFileRoutes(authed, svc)
	registerAuthedDLNARoutes(authed, svc)
	registerAuthedRealtimeRoutes(authed, svc)
	registerAuthedUISurfaceRoutes(authed, svc)
	registerAuthedSearchRoutes(authed, svc)
	registerAuthedSystemExtraRoutes(authed, svc)
	registerAuthedStatsExtraRoutes(authed, svc)
	registerAuthedPlaylistExtraRoutes(authed, svc)
	registerAuthedDLNAControlRoutes(authed, svc)
	registerAuthedFavoriteAndMediaActionRoutes(authed, svc)
	registerAuthedPlaybackExtraRoutes(authed, svc)
	registerReaderRoutes(authed, svc)
}
