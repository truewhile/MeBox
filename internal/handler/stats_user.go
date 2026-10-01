// Package handler — per-user stats and a play-event recorder.
package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

// statsUserHandler returns a watch-time summary for one user.
// 观看统计是隐私数据：仅允许本人或管理员查询。
func statsUserHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := c.Param("id")
		if !statsCallerAllowed(c, uid) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		var watched int64
		_ = svc.Repo.DB.Model(&model.PlaybackHistory{}).
			Where("user_id = ?", uid).
			Select("COALESCE(SUM(position_ms), 0)").
			Row().Scan(&watched)
		var total int64
		_ = svc.Repo.DB.Model(&model.PlaybackHistory{}).
			Where("user_id = ?", uid).Count(&total).Error
		c.JSON(http.StatusOK, gin.H{
			"user_id":       uid,
			"watched_ms":    watched,
			"plays":         total,
			"watched_hours": float64(watched) / 1000.0 / 3600.0,
		})
	}
}

// statsTopUsersHandler returns the most active users by play count.
// 全员排行含用户名与精确时长，仅管理员可查。
func statsTopUsersHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get(middleware.CtxUserRole)
		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
		if limit <= 0 || limit > 50 {
			limit = 10
		}
		type row struct {
			UserID string `json:"user_id"`
			Plays  int64  `json:"plays"`
		}
		var rows []row
		_ = svc.Repo.DB.Table("playback_histories").
			Select("user_id, COUNT(*) as plays").
			Group("user_id").
			Order("plays desc").
			Limit(limit).Scan(&rows).Error
		// Hydrate usernames in one query.
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.UserID)
		}
		nameIdx := map[string]string{}
		if len(ids) > 0 {
			var users []model.User
			_ = svc.Repo.DB.Where("id IN ?", ids).Find(&users).Error
			for _, u := range users {
				nameIdx[u.ID] = u.Username
			}
		}
		out := make([]gin.H, 0, len(rows))
		for _, r := range rows {
			out = append(out, gin.H{
				"user_id":  r.UserID,
				"username": nameIdx[r.UserID],
				"plays":    r.Plays,
			})
		}
		c.JSON(http.StatusOK, gin.H{"items": out})
	}
}

// statsPlayHandler accepts a play event so the Vue analytics panel can
// emit one even when the actual progress write goes through /history.
type playEventReq struct {
	MediaID    string `json:"media_id" binding:"required"`
	PositionMs int64  `json:"position_ms"`
	DurationMs int64  `json:"duration_ms"`
	Completed  bool   `json:"completed"`
}

func statsPlayHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req playEventReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		uid, _ := c.Get(middleware.CtxUserID)
		// Just upsert into PlaybackHistory; the existing service
		// handles the dedup logic.
		if err := svc.Repo.History.Upsert(c.Request.Context(), &model.PlaybackHistory{
			UserID:     toString(uid),
			MediaID:    req.MediaID,
			PositionMs: req.PositionMs,
			DurationMs: req.DurationMs,
			WatchedAt:  time.Now(),
			Completed:  req.Completed,
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// statsCallerAllowed 判断当前调用者是否允许查看 uid 的观看统计。
func statsCallerAllowed(c *gin.Context, uid string) bool {
	role, _ := c.Get(middleware.CtxUserRole)
	if role == "admin" {
		return true
	}
	caller, _ := c.Get(middleware.CtxUserID)
	return toString(caller) == uid
}
