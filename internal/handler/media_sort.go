package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

func parseMediaSort(c *gin.Context) service.MediaSortSpec {
	if c == nil {
		return service.NormalizeMediaSort("", "")
	}
	return service.NormalizeMediaSort(c.Query("sort"), c.Query("order"))
}

// mediaHistoryMap loads the current user's latest playback time per media ID.
// It is only used for the explicit last_played sort so normal pagination keeps
// its single-query path.
func mediaHistoryMap(c *gin.Context, svc *service.Container) map[string]time.Time {
	if c == nil || svc == nil || svc.Repo == nil || svc.Repo.DB == nil {
		return nil
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID := strings.TrimSpace(toString(uid))
	if userID == "" {
		return nil
	}
	var rows []struct {
		MediaID   string    `gorm:"column:media_id"`
		WatchedAt time.Time `gorm:"column:watched_at"`
	}
	if err := svc.Repo.DB.WithContext(c.Request.Context()).
		Model(&model.PlaybackHistory{}).
		Select("media_id, MAX(watched_at) AS watched_at").
		Where("user_id = ?", userID).
		Group("media_id").
		Scan(&rows).Error; err != nil {
		return nil
	}
	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		out[row.MediaID] = row.WatchedAt
	}
	return out
}
