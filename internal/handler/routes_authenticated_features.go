package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/service"
)

func registerAuthedFileRoutes(authed *gin.RouterGroup, svc *service.Container) {
	authed.GET("/files", middleware.AdminRequired(), browseFilesHandler(svc))
	authed.POST("/files/folders", middleware.AdminRequired(), createFolderHandler(svc))
	authed.PUT("/files/rename", middleware.AdminRequired(), renameFileHandler(svc))
	authed.DELETE("/files", middleware.AdminRequired(), deleteFileHandler(svc))
	authed.POST("/files/transfer", middleware.AdminRequired(), transferFileHandler(svc))
}

func registerAuthedDLNARoutes(authed *gin.RouterGroup, svc *service.Container) {
	authed.GET("/dlna/devices", dlnaListHandler(svc))
	authed.POST("/dlna/cast", dlnaCastHandler(svc))
}

func registerAuthedRealtimeRoutes(authed *gin.RouterGroup, svc *service.Container) {
	authed.GET("/ws", wsHandler(svc))
	authed.GET("/events", sseHandler(svc))
}
