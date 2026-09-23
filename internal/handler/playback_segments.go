// Package handler — 片头/片尾片段接口。
//
//	GET /playback/:id/segments
//
// 单独开一个接口而不是塞进 /media/:id/playback，有两个原因：一是抓取外部数据
// 可能要几秒，不能拖慢决定能否起播的那个请求；二是片段与播放来源（本地 / 云盘 /
// 远程 Emby）无关，独立出来对所有媒体一致。
package handler

import (
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
