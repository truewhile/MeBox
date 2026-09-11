// Package handler — danmaku endpoints.
package handler

import (
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
		// 弹幕合并偏好按用户存储：这里读取后作为本次抓取的选项传入。
		opts := service.DanmakuFetchOptions{
			MergeSources: svc.Danmaku.MergeSourcesEnabled(c.Request.Context(), uid),
		}
		res, err := svc.Danmaku.FetchWithOptions(
			c.Request.Context(), c.Param("id"), c.Query("kw"), c.Query("episodeId"), opts)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

// getDanmakuConfigHandler exposes the danmaku renderer knobs (opacity, font
// size, area, enabled) so the player can initialize its control panel without
// admin privileges.
func getDanmakuConfigHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, svc.Danmaku.ConfigForUser(c.Request.Context(), currentUserID(c)))
	}
}

// updateDanmakuSettingsHandler 持久化当前用户的弹幕偏好。目前只有合并开关，
// 落在 user 表上（与字幕简繁偏好同样按用户存储）。
func updateDanmakuSettingsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := currentUserID(c)
		if strings.TrimSpace(uid) == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
			return
		}
		var req struct {
			MergeSources *bool `json:"merge_sources"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		if req.MergeSources == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "merge_sources is required"})
			return
		}
		if err := svc.Danmaku.SetMergeSources(c.Request.Context(), uid, *req.MergeSources); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"merge_sources": *req.MergeSources})
	}
}
