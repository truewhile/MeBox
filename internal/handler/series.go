// Package handler — TV series endpoints.
//
// These return episode lists grouped by season number for a library that
// holds TV episodes. Series rows are distinct from Movies — the front
// end uses /api/libraries/:id/seasons to render a season selector.
package handler

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

// seasonGroup is the JSON returned to the React UI per season.
type seasonGroup struct {
	Season   int           `json:"season"`
	Episodes []model.Media `json:"episodes"`
}

func listSeasonsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		libID := c.Param("id")
		if lib, err := svc.Repo.Library.FindByID(c.Request.Context(), libID); err == nil && lib != nil {
			if !service.LibraryVisibleForUser(c.Request.Context(), svc.Repo, *lib, mediaVisibilityForRequest(c, svc)) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
		}
		visibility := mediaVisibilityForRequest(c, svc)
		var rows []model.Media
		const pageSize = 2000
		for page := 1; ; page++ {
			pageRows, total, err := svc.Media.ListMediaVisible(c.Request.Context(), libID, page, pageSize, visibility)
			if err != nil && err != gorm.ErrRecordNotFound {
				writeInternalOrCanceled(c, err)
				return
			}
			rows = append(rows, pageRows...)
			if int64(len(rows)) >= total || len(pageRows) < pageSize {
				break
			}
		}
		buckets := make(map[int][]model.Media)
		for _, r := range rows {
			if !visibility.Allows(&r) {
				continue
			}
			buckets[r.SeasonNum] = append(buckets[r.SeasonNum], r)
		}
		out := make([]seasonGroup, 0, len(buckets))
		for s, items := range buckets {
			out = append(out, seasonGroup{Season: s, Episodes: items})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Season < out[j].Season })
		c.JSON(http.StatusOK, gin.H{"seasons": out})
	}
}

func listLibrarySeriesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		libID := c.Param("id")
		ctx := c.Request.Context()
		// 远程剧集库：远程 Series 映射为系列卡片。
		if svc.EmbyRemote != nil && service.IsEmbyRemoteID(libID) {
			mountID, remoteID, _ := service.DecodeEmbyRemoteID(libID)
			mount, acct, _ := svc.EmbyRemote.ResolveMount(ctx, mountID)
			if mount == nil || acct == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			if !service.EmbyMountLibraryAllowed(mediaVisibilityForRequest(c, svc), mount) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			cards, err := svc.EmbyRemote.RemoteSeriesCards(ctx, mount, acct, remoteID)
			if err != nil {
				writeInternalOrCanceled(c, err)
				return
			}
			page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
			size, _ := strconv.Atoi(c.DefaultQuery("page_size", "500"))
			if page < 1 {
				page = 1
			}
			if size <= 0 || size > 1000 {
				size = 500
			}
			start := (page - 1) * size
			if start > len(cards) {
				start = len(cards)
			}
			end := start + size
			if end > len(cards) {
				end = len(cards)
			}
			pageItems := cards[start:end]
			if pageItems == nil {
				pageItems = []service.SeriesCard{}
			}
			c.JSON(http.StatusOK, gin.H{
				"items":     pageItems,
				"total":     len(cards),
				"page":      page,
				"page_size": size,
			})
			return
		}
		if lib, err := svc.Repo.Library.FindByID(ctx, libID); err == nil && lib != nil {
			if !service.LibraryVisibleForUser(c.Request.Context(), svc.Repo, *lib, mediaVisibilityForRequest(c, svc)) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
		}
		items, total, err := svc.Media.ListLibrarySeriesCards(c.Request.Context(), libID, mediaVisibilityForRequest(c, svc))
		if err != nil {
			writeInternalOrCanceled(c, err)
			return
		}
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		size, _ := strconv.Atoi(c.DefaultQuery("page_size", "500"))
		if page < 1 {
			page = 1
		}
		if size <= 0 || size > 1000 {
			size = 500
		}
		start := (page - 1) * size
		if start > len(items) {
			start = len(items)
		}
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		pageItems := items[start:end]
		if pageItems == nil {
			// 非 nil 空切片，避免空库返回 "items": null 触发前端崩溃。
			pageItems = []service.SeriesCard{}
		}
		c.JSON(http.StatusOK, gin.H{
			"items":     pageItems,
			"total":     total,
			"page":      page,
			"page_size": size,
		})
	}
}

func listLibrarySeriesEpisodesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		libID := c.Param("id")
		key := c.Query("key")
		if key == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "key is required"})
			return
		}
		ctx := c.Request.Context()
		// 远程系列 key（伪装系列 ID）：转发远程该系列全部剧集。
		if svc.EmbyRemote != nil && service.IsEmbyRemoteID(key) {
			mountID, remoteSeriesID, _ := service.DecodeEmbyRemoteID(key)
			mount, acct, _ := svc.EmbyRemote.ResolveMount(ctx, mountID)
			if mount == nil || acct == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			if !service.EmbyMountLibraryAllowed(mediaVisibilityForRequest(c, svc), mount) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			items, err := svc.EmbyRemote.RemoteEpisodes(ctx, mount, acct, remoteSeriesID)
			if err != nil {
				writeInternalOrCanceled(c, err)
				return
			}
			if items == nil {
				items = []model.Media{}
			}
			c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
			return
		}
		if lib, err := svc.Repo.Library.FindByID(ctx, libID); err == nil && lib != nil {
			if !service.LibraryVisibleForUser(c.Request.Context(), svc.Repo, *lib, mediaVisibilityForRequest(c, svc)) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
		}
		items, err := svc.Media.ListLibrarySeriesEpisodes(c.Request.Context(), libID, key, mediaVisibilityForRequest(c, svc))
		if err != nil {
			writeInternalOrCanceled(c, err)
			return
		}
		grouped := service.GroupEpisodeVersionsForDisplay(items)
		c.JSON(http.StatusOK, gin.H{"items": grouped, "total": len(grouped)})
	}
}

func listMediaEpisodesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
			return
		}
		ctx := c.Request.Context()
		// 远程条目：单集→同系列集列表；系列/季/文件夹→子集；电影→自身单条。
		if svc.EmbyRemote != nil && service.IsEmbyRemoteID(id) {
			mountID, remoteID, _ := service.DecodeEmbyRemoteID(id)
			mount, acct, _ := svc.EmbyRemote.ResolveMount(ctx, mountID)
			if mount == nil || acct == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			if !service.EmbyMountLibraryAllowed(mediaVisibilityForRequest(c, svc), mount) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			items, err := svc.EmbyRemote.RemoteEpisodes(ctx, mount, acct, remoteID)
			if err != nil {
				writeInternalOrCanceled(c, err)
				return
			}
			if items == nil {
				items = []model.Media{}
			}
			c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
			return
		}
		items, err := svc.Media.ListMediaEpisodes(ctx, id, mediaVisibilityForRequest(c, svc))
		if err != nil {
			writeInternalOrCanceled(c, err)
			return
		}
		grouped := service.GroupEpisodeVersionsForDisplay(items)
		c.JSON(http.StatusOK, gin.H{"items": grouped, "total": len(grouped)})
	}
}
