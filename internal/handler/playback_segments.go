// Package handler — 片头/片尾片段接口。
//
//	GET    /playback/:id/segments
//	PUT    /playback/:id/segments   {kind, position_ms, clear}
//
// 单独开一个接口而不是塞进 /media/:id/playback，有两个原因：一是抓取外部数据
// 可能要几秒，不能拖慢决定能否起播的那个请求；二是片段与播放来源（本地 / 云盘 /
// 远程 Emby）无关，独立出来对所有媒体一致。
//
// 写入接口只处理「手动标记」：用户播放时把当前位置标成片头结束 / 片尾开始。
// 外部数据永远由提供方刷新，不接受客户端写入。
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/service"
)

// playbackSegmentsHandler returns the skippable ranges for one media item.
// The client calls it after playback has already started, so the provider
// lookup never delays a play.
func playbackSegmentsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		autoSkip := resolveAutoSkipFlag(c, svc)
		segments := []service.SegmentView{}

		m, err := findMediaForPlaybackEndpoint(c, svc, c.Param("id"))
		if err != nil || m == nil || !mediaVisibleForRequest(c, svc, m) {
			c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
			return
		}
		// 远程 Emby 挂载的条目是上游库的投影，本地没有可查询的外部 ID 关联。
		if svc.Segments != nil && !service.IsEmbyRemoteID(m.ID) {
			rows, listErr := svc.Segments.ListForPlayback(c.Request.Context(), m)
			if listErr != nil && svc.Log != nil {
				svc.Log.Debug("list media segments failed",
					zap.String("media_id", m.ID), zap.Error(listErr))
			}
			segments = service.ToSegmentViews(rows)
		}
		c.JSON(http.StatusOK, gin.H{"segments": segments, "auto_skip": autoSkip})
	}
}

// playbackSegmentsMarkRequest 是手动标记的入参。
type playbackSegmentsMarkRequest struct {
	// Kind 只接受 intro / credits。
	Kind string `json:"kind" binding:"required"`
	// PositionMs 是当前播放位置：标片头时表示片头结束点，标片尾时表示片尾开始点。
	PositionMs int64 `json:"position_ms"`
	// Clear 为 true 时删除该类型的标记。
	Clear bool `json:"clear"`
}

// playbackSegmentsMarkHandler 写入或清除一条手动标记，返回更新后的完整片段列表。
//
// 返回整份列表而不是只回 204：播放器需要立刻反映合并后的结果（手动标记优先级
// 最高），避免自己再发一次 GET 造成竞态。
func playbackSegmentsMarkHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc.Segments == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "segments unavailable"})
			return
		}
		m, err := findMediaForPlaybackEndpoint(c, svc, c.Param("id"))
		if err != nil || m == nil || !mediaVisibleForRequest(c, svc, m) {
			c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
			return
		}
		// 远程 Emby 挂载是上游库的投影，本地标记没有可落地的地方。
		if service.IsEmbyRemoteID(m.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "remote media cannot be marked"})
			return
		}
		var req playbackSegmentsMarkRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		err = svc.Segments.SetManualMark(c.Request.Context(), m, service.ManualMarkRequest{
			Kind:       req.Kind,
			PositionMs: req.PositionMs,
			Clear:      req.Clear,
		})
		if errors.Is(err, service.ErrInvalidManualMark) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid skip mark"})
			return
		}
		if err != nil {
			if svc.Log != nil {
				svc.Log.Warn("save manual skip mark failed",
					zap.String("media_id", m.ID), zap.Error(err))
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "save failed"})
			return
		}
		rows, listErr := svc.Segments.ListForPlayback(c.Request.Context(), m)
		if listErr != nil {
			if svc.Log != nil {
				svc.Log.Debug("list media segments after mark failed",
					zap.String("media_id", m.ID), zap.Error(listErr))
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"segments":  service.ToSegmentViews(rows),
			"auto_skip": resolveAutoSkipFlag(c, svc),
		})
	}
}

// resolveAutoSkipFlag reads the「自动跳过片头」switch off whichever profile is
// currently in effect. It reuses selectedPlayProfile so the server agrees with
// the UI about which profile is active (explicit header first, then the user's
// default profile), and a PIN-locked profile never silently skips for the user.
func resolveAutoSkipFlag(c *gin.Context, svc *service.Container) bool {
	profile, locked := selectedPlayProfile(c, svc)
	if locked || profile == nil {
		return false
	}
	return profile.SkipIntro
}
