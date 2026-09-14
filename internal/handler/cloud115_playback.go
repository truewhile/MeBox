package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

func mediaPlaybackInfoHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || svc.Cloud115 == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "cloud115 playback unavailable"})
			return
		}
		m, err := svc.Media.GetMedia(c.Request.Context(), c.Param("id"))
		if err != nil || m == nil || !mediaVisibleForRequest(c, svc, m) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if !enforceScopedPlaybackToken(c, m.ID) {
			return
		}
		quality, _ := strconv.Atoi(c.Query("quality"))
		info, err := svc.Cloud115.PlaybackInfo(c.Request.Context(), m.ID, quality)
		if err != nil {
			if errors.Is(err, service.ErrMediaNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, info)
	}
}

func mediaTranscodeHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || svc.Cloud115 == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "cloud115 playback unavailable"})
			return
		}
		m, err := svc.Media.GetMedia(c.Request.Context(), c.Param("id"))
		if err != nil || m == nil || !mediaVisibleForRequest(c, svc, m) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if !enforceScopedPlaybackToken(c, m.ID) {
			return
		}
		var req struct {
			Definition int `json:"definition"`
		}
		_ = c.ShouldBindJSON(&req)
		info, err := svc.Cloud115.StartTranscode(c.Request.Context(), m.ID, req.Definition)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, info)
	}
}

func cloud115HLSMasterHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || svc.Cloud115 == nil || svc.Cloud115.HLSProxy() == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "cloud115 playback unavailable"})
			return
		}
		m, err := svc.Media.GetMedia(c.Request.Context(), c.Param("id"))
		if err != nil || m == nil || !mediaVisibleForRequest(c, svc, m) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if !enforceScopedPlaybackToken(c, m.ID) {
			return
		}
		definition, _ := strconv.Atoi(c.Query("definition"))
		err = svc.Cloud115.HLSProxy().ServeMaster(c.Request.Context(), c.Writer, c.Request, m.ID, definition)
		if err == nil {
			return
		}
		if c.Writer.Written() {
			return
		}
		if errors.Is(err, service.ErrCloud115TranscodePending) {
			retryAfter := 5
			message := "115 正在转码，请稍候"
			var info *service.PlaybackInfo
			if latest, infoErr := svc.Cloud115.PlaybackInfo(c.Request.Context(), m.ID, definition); infoErr == nil && latest != nil {
				info = latest
				if latest.Transcode.Message != "" {
					message = latest.Transcode.Message
				}
				if latest.Transcode.RetryAfterSec > 0 {
					retryAfter = latest.Transcode.RetryAfterSec
				}
			}
			c.JSON(http.StatusConflict, gin.H{
				"code":            "transcode_pending",
				"message":         message,
				"retry_after_sec": retryAfter,
				"playback":        info,
			})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
	}
}

func cloud115HLSSessionHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || svc.Cloud115 == nil || svc.Cloud115.HLSProxy() == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "cloud115 playback unavailable"})
			return
		}
		mediaID := c.Query("media_id")
		if mediaID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing media_id"})
			return
		}
		if !enforceScopedPlaybackToken(c, mediaID) {
			return
		}
		err := svc.Cloud115.HLSProxy().ServeChild(
			c.Request.Context(),
			c.Writer,
			c.Request,
			c.Param("session"),
			c.Param("key"),
		)
		if err == nil || c.Writer.Written() {
			return
		}
		switch {
		case errors.Is(err, service.ErrCloud115HLSSessionNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "hls session not found"})
		case errors.Is(err, service.ErrCloud115HLSUpstreamExpired):
			c.JSON(http.StatusGone, gin.H{"error": "hls upstream expired"})
		default:
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		}
	}
}
