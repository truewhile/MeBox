// Package handler — watch history endpoints.
//
// The base /history GET / POST routes already exist; these add the three
// auxiliary surfaces the React WatchHistoryPage needs:
//
//	GET  /api/watch-history          paginated list (admin sees every user)
//	GET  /api/watch-history/stats    aggregate watch time + completion
//	GET  /api/watch-history/continue resume rail (incomplete only)
//	DELETE /api/watch-history        clear (?media_item_id= optional)
//	DELETE /api/watch-history/:id    remove one row
package handler

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

// historyListHandler returns the caller's history rows joined with the
// matching media in a single response.
func historyListHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, _ := c.Get(middleware.CtxUserID)
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		if limit <= 0 || limit > 500 {
			limit = 50
		}
		items, err := svc.Playback.RecentHistory(c.Request.Context(), toString(uid), limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		visibility := mediaVisibilityForRequest(c, svc)
		filtered := make([]service.HistoryItem, 0, len(items))
		for _, item := range items {
			if item.Media == nil || visibility.Allows(item.Media) {
				filtered = append(filtered, item)
			}
		}
		c.JSON(http.StatusOK, filtered)
	}
}

// historyStatsHandler returns aggregate watch time + completion counts
// for the caller. Used by the WatchHistoryPage hero card and the dedicated
// personal statistics page.
//
// 统计口径全部来自 PlaybackHistory 本身，不新增统计表：position_ms 是「已看
// 时长」的近似值，足以支撑趋势图；精确到秒的播放时长另有会话统计负责。
func historyStatsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, _ := c.Get(middleware.CtxUserID)
		userID := toString(uid)
		ctx := c.Request.Context()

		var total int64
		_ = svc.Repo.DB.Model(&model.PlaybackHistory{}).
			Where("user_id = ?", userID).Count(&total).Error

		var completed int64
		_ = svc.Repo.DB.Model(&model.PlaybackHistory{}).
			Where("user_id = ? AND completed = ?", userID, true).Count(&completed).Error

		var watchedMs int64
		_ = svc.Repo.DB.Model(&model.PlaybackHistory{}).
			Where("user_id = ?", userID).
			Select("COALESCE(SUM(position_ms), 0)").
			Row().Scan(&watchedMs)

		var last *time.Time
		row := svc.Repo.DB.Model(&model.PlaybackHistory{}).
			Where("user_id = ?", userID).
			Select("MAX(watched_at)").Row()
		var lastT time.Time
		if err := row.Scan(&lastT); err == nil && !lastT.IsZero() {
			last = &lastT
		}

		visibility := mediaVisibilityForRequest(c, svc)
		daily, byType, recent := historyStatsBreakdowns(ctx, svc, userID, visibility)

		inProgress := total - completed
		if inProgress < 0 {
			inProgress = 0
		}

		c.JSON(http.StatusOK, gin.H{
			"total":           total,
			"completed":       completed,
			"in_progress":     inProgress,
			"watched_ms":      watchedMs,
			"watched_hours":   float64(watchedMs) / 1000.0 / 3600.0,
			"last_watched":    last,
			"daily":           daily,
			"by_library_type": byType,
			"recent":          recent,
		})
	}
}

// historyStatsDailyDays 是趋势图回看的天数。
const historyStatsDailyDays = 30

// historyStatsRecentLimit 是「最近看过」返回的条数。
const historyStatsRecentLimit = 8

type historyDailyStat struct {
	Day     string `json:"day"`
	WatchMs int64  `json:"watch_ms"`
	Plays   int64  `json:"plays"`
}

type historyTypeStat struct {
	Type    string `json:"type"`
	WatchMs int64  `json:"watch_ms"`
	Count   int64  `json:"count"`
}

// historyStatsBreakdowns 产出每日趋势、按媒体库类型分布与最近记录。
//
// 分桶在 Go 里做而不是用 SQL 的日期函数：SQLite 的 strftime 与 PostgreSQL 的
// to_char 语法不同，写两份 SQL 会在方言差异上长期出错，而历史行数受用户规模
// 约束（每人一行一部媒体），一次全量读取是可以接受的。
//
// visibility 控制哪些媒体对调用者可见（播放档案、成人锁等）。
func historyStatsBreakdowns(ctx context.Context, svc *service.Container, userID string, visibility service.MediaVisibility) ([]historyDailyStat, []historyTypeStat, []map[string]any) {
	daily := make([]historyDailyStat, 0, historyStatsDailyDays)
	byType := make([]historyTypeStat, 0)
	recent := make([]map[string]any, 0, historyStatsRecentLimit)

	var rows []model.PlaybackHistory
	if err := svc.Repo.DB.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("watched_at desc").
		Find(&rows).Error; err != nil || len(rows) == 0 {
		return daily, byType, recent
	}

	// 每日趋势：只回看最近 N 天，且按「本地日」分桶，避免跨时区偏移。
	now := time.Now()
	cutoff := now.AddDate(0, 0, -(historyStatsDailyDays - 1))
	startOfDay := func(t time.Time) time.Time {
		local := t.In(time.Local)
		return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	}
	buckets := make(map[string]*historyDailyStat, historyStatsDailyDays)
	for i := 0; i < historyStatsDailyDays; i++ {
		day := startOfDay(cutoff).AddDate(0, 0, i).Format("2006-01-02")
		buckets[day] = &historyDailyStat{Day: day}
	}
	for _, r := range rows {
		watched := r.WatchedAt.In(time.Local)
		if watched.Before(startOfDay(cutoff)) {
			continue
		}
		if bucket, ok := buckets[watched.Format("2006-01-02")]; ok {
			bucket.WatchMs += r.PositionMs
			bucket.Plays++
		}
	}
	for i := 0; i < historyStatsDailyDays; i++ {
		day := startOfDay(cutoff).AddDate(0, 0, i).Format("2006-01-02")
		if bucket, ok := buckets[day]; ok && bucket.Plays > 0 {
			daily = append(daily, *bucket)
		}
	}

	mediaIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		mediaIDs = append(mediaIDs, r.MediaID)
	}
	var medias []model.Media
	_ = svc.Repo.DB.WithContext(ctx).Where("id IN ?", mediaIDs).Find(&medias).Error
	mediaByID := make(map[string]*model.Media, len(medias))
	for i := range medias {
		mediaByID[medias[i].ID] = &medias[i]
	}

	libraryTypes := make(map[string]string)
	var libraries []model.Library
	if svc.Repo.Library != nil {
		if libs, err := svc.Repo.Library.List(ctx); err == nil {
			libraries = libs
		}
	}
	for _, lib := range libraries {
		libraryTypes[lib.ID] = lib.Type
	}

	typeAcc := make(map[string]*historyTypeStat)
	order := make([]string, 0, 4)
	for _, r := range rows {
		media := mediaByID[r.MediaID]
		var key string
		if media == nil {
			// 媒体记录已删除（含 Emby 远程缓存失效）：计入 "other" 桶而非丢弃，
			// 这样类型分布总数才能与播放历史总数吻合。
			key = "other"
		} else {
			// 如果调用者的可见性策略排除了该媒体，则跳过统计（visibility leak fix）。
			if !visibility.Allows(media) {
				continue
			}
			key = strings.TrimSpace(libraryTypes[media.LibraryID])
			if key == "" {
				key = "other"
			}
		}
		acc, ok := typeAcc[key]
		if !ok {
			acc = &historyTypeStat{Type: key}
			typeAcc[key] = acc
			order = append(order, key)
		}
		acc.WatchMs += r.PositionMs
		acc.Count++
	}
	// 顺序按观看时长降序，让「我主要在看什么」一眼可见。
	for _, key := range order {
		byType = append(byType, *typeAcc[key])
	}
	sort.SliceStable(byType, func(i, j int) bool {
		if byType[i].WatchMs != byType[j].WatchMs {
			return byType[i].WatchMs > byType[j].WatchMs
		}
		return byType[i].Type < byType[j].Type
	})

	for _, r := range rows {
		if len(recent) >= historyStatsRecentLimit {
			break
		}
		entry := map[string]any{"history": r}
		if media := mediaByID[r.MediaID]; media != nil {
			// 可见性检查：隐藏库或受档案限制的媒体不进入最近记录（visibility leak fix）。
			if !visibility.Allows(media) {
				continue
			}
			entry["media"] = media
		} else if svc.EmbyRemote != nil && service.IsEmbyRemoteID(r.MediaID) {
			// 尝试从 Emby 远端补全媒体详情，与 historyContinueHandler 保持相同策略。
			mountID, remoteID, _ := service.DecodeEmbyRemoteID(r.MediaID)
			mount, acct, resolveErr := svc.EmbyRemote.ResolveMount(ctx, mountID)
			if resolveErr == nil && mount != nil && acct != nil {
				remoteMedia, detailErr := svc.EmbyRemote.RemoteMediaDetail(ctx, mount, acct, remoteID)
				if detailErr == nil && remoteMedia != nil {
					if !visibility.Allows(remoteMedia) {
						continue
					}
					entry["media"] = *remoteMedia
				} else {
					// 无法获取 Emby 媒体详情，跳过此条记录。
					continue
				}
			} else {
				// 挂载不可用，跳过。
				continue
			}
		} else {
			// 媒体记录不存在且无法 Emby 补全，跳过。
			continue
		}
		recent = append(recent, entry)
	}

	return daily, byType, recent
}

// historyContinueHandler returns "Continue Watching" rows: incomplete
// items, most recent first.
func historyContinueHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, _ := c.Get(middleware.CtxUserID)
		userID := toString(uid)
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
		if limit <= 0 || limit > 50 {
			limit = 10
		}
		var rows []model.PlaybackHistory
		if err := svc.Repo.DB.
			Where("user_id = ? AND completed = ?", userID, false).
			Order("watched_at desc").
			Limit(limit).
			Find(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Hydrate media in one query.
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.MediaID)
		}
		var media []model.Media
		if len(ids) > 0 {
			_ = svc.Repo.DB.Where("id IN ?", ids).Find(&media).Error
		}
		mIdx := make(map[string]model.Media, len(media))
		for _, m := range media {
			mIdx[m.ID] = m
		}
		out := make([]gin.H, 0, len(rows))
		staleIDs := make([]string, 0)
		for _, r := range rows {
			m, ok := mIdx[r.MediaID]
			if ok {
				if mediaVisibleForRequest(c, svc, &m) {
					out = append(out, gin.H{
						"history": r,
						"media":   m,
					})
				}
				continue
			}

			if svc.EmbyRemote != nil && service.IsEmbyRemoteID(r.MediaID) {
				mountID, remoteID, _ := service.DecodeEmbyRemoteID(r.MediaID)
				mount, acct, resolveErr := svc.EmbyRemote.ResolveMount(c.Request.Context(), mountID)
				if resolveErr == nil && mount != nil && acct != nil {
					remoteMedia, detailErr := svc.EmbyRemote.RemoteMediaDetail(c.Request.Context(), mount, acct, remoteID)
					if detailErr == nil && remoteMedia != nil {
						if mediaVisibleForRequest(c, svc, remoteMedia) {
							out = append(out, gin.H{
								"history": r,
								"media":   *remoteMedia,
							})
						}
						continue
					}
				}
			}

			// 媒体记录已不存在。继续返回占位卡只会让用户点击后遇到 404，
			// 因此清理这条失效播放记录，不再占用继续观看列表。
			staleIDs = append(staleIDs, r.MediaID)
		}
		if len(staleIDs) > 0 {
			_ = svc.Repo.DB.WithContext(c.Request.Context()).Unscoped().
				Where("user_id = ? AND media_id IN ?", userID, staleIDs).
				Delete(&model.PlaybackHistory{}).Error
		}
		c.JSON(http.StatusOK, out)
	}
}

// historyDeleteHandler removes one or all history rows for the caller.
//
//	DELETE /api/watch-history?media_id=xxx  → delete just that media's row
//	DELETE /api/watch-history?status=completed   → delete completed rows
//	DELETE /api/watch-history?status=incomplete  → delete unfinished rows
//	DELETE /api/watch-history               → clear all rows for the user
func historyDeleteHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, _ := c.Get(middleware.CtxUserID)
		userID := toString(uid)
		mediaID := c.Query("media_id")
		status := c.Query("status")

		q := svc.Repo.DB.Where("user_id = ?", userID)
		if mediaID != "" {
			q = q.Where("media_id = ?", mediaID)
		}
		switch status {
		case "completed", "watched":
			q = q.Where("completed = ?", true)
		case "incomplete", "unfinished", "unwatched":
			q = q.Where("completed = ?", false)
		case "":
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "status must be completed or incomplete"})
			return
		}
		res := q.Unscoped().Delete(&model.PlaybackHistory{})
		if err := res.Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"removed": res.RowsAffected})
	}
}

func historyDeleteOneHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, _ := c.Get(middleware.CtxUserID)
		res := svc.Repo.DB.
			Where("user_id = ? AND id = ?", toString(uid), c.Param("id")).
			Delete(&model.PlaybackHistory{})
		if err := res.Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"removed": res.RowsAffected})
	}
}
