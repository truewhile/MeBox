// Package handler — danmaku endpoints.
package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

// getDanmakuHandler returns danmaku for a media. ?kw= optionally overrides the
// search keyword (used by the player's manual search box); ?episodeId= forces a
// specific danmaku library chosen by the user after a disambiguation.
func getDanmakuHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := currentUserID(c)
		// 按用户读取弹幕源、凭据、合并偏好和渲染参数。
		opts := service.DanmakuFetchOptions{UserID: uid}
		res, err := svc.Danmaku.FetchWithOptions(
			c.Request.Context(), c.Param("id"), c.Query("kw"), c.Query("episodeId"), opts)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

// getDanmakuConfigHandler exposes the current user's player volume and danmaku
// preferences so the player can initialize without admin privileges.
func getDanmakuConfigHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, svc.Danmaku.ConfigForUser(c.Request.Context(), currentUserID(c)))
	}
}

// updateDanmakuSettingsHandler 持久化当前用户的播放器音量与弹幕偏好。
func updateDanmakuSettingsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := currentUserID(c)
		if strings.TrimSpace(uid) == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
			return
		}
		var req service.DanmakuSettingsPatch
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		cfg, err := svc.Danmaku.UpdateUserSettings(c.Request.Context(), uid, req)
		if errors.Is(err, service.ErrNoDanmakuSettings) || errors.Is(err, service.ErrInvalidDanmakuSettings) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, cfg)
	}
}
