// Package handler — subtitle endpoints.
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

func listSubtitlesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if svc.EmbyRemote != nil && service.IsEmbyRemoteID(id) {
			c.JSON(http.StatusOK, gin.H{"tracks": []service.SubtitleTrack{}})
			return
		}
		var tracks []service.SubtitleTrack
		var err error
		if c.Query("include_embedded") == "true" {
			tracks, err = svc.Subtitle.Discover(c.Request.Context(), id)
		} else {
			tracks, err = svc.Subtitle.DiscoverExternalOnly(c.Request.Context(), id)
		}
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		if tracks == nil {
			tracks = []service.SubtitleTrack{}
		}
		c.JSON(http.StatusOK, gin.H{"tracks": tracks})
	}
}

func serveASSSubtitleHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Query("path")
		if path == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing path"})
			return
		}
		c.Header("Content-Type", "text/plain; charset=utf-8")
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		if err := svc.Subtitle.ServeASS(c.Request.Context(), c.Param("id"), path, c.Writer); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
	}
}

func serveSubtitleHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Query("path")
		if path == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing path"})
			return
		}
		c.Header("Content-Type", "text/vtt; charset=utf-8")
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		if err := svc.Subtitle.Serve(c.Request.Context(), c.Param("id"), path, c.Writer); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
	}
}
