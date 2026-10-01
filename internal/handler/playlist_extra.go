// Package handler — playlist reordering + per-item-id removal that the
// Vue UI uses on top of the basic /playlists/:id/items surface.
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

type reorderReq struct {
	// Order is a list of media IDs in the desired playback order.
	Order []string `json:"order" binding:"required"`
}

// reorderPlaylistHandler updates the Position column on each
// PlaylistItem to match the supplied order. Items missing from the
// order keep their existing position.
func reorderPlaylistHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req reorderReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		pid := c.Param("id")
		if _, _, ok := playlistWriteGuard(c, svc, pid); !ok {
			return
		}
		for i, mid := range req.Order {
			if err := svc.Repo.DB.WithContext(c.Request.Context()).
				Model(&model.PlaylistItem{}).
				Where("playlist_id = ? AND media_id = ?", pid, mid).
				Update("position", i).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
		c.Status(http.StatusNoContent)
	}
}

// deletePlaylistItemByIDHandler is the alternate route at
// /playlists/:id/items/:item_id (vs. the existing /:media_id variant).
func deletePlaylistItemByIDHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		pid := c.Param("id")
		if _, _, ok := playlistWriteGuard(c, svc, pid); !ok {
			return
		}
		if err := svc.Repo.DB.WithContext(c.Request.Context()).
			Where("playlist_id = ? AND id = ?", pid, c.Param("item_id")).
			Delete(&model.PlaylistItem{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	}
}
