// Package handler — library / media HTTP endpoints.
package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

type createLibraryReq struct {
	Name               string                     `json:"name"`
	Path               string                     `json:"path"`
	Paths              []string                   `json:"paths"`
	Roots              []service.LibraryRootInput `json:"roots"`
	Type               string                     `json:"type"`
	CoverURL           string                     `json:"cover_url"`
	CreatePerSubfolder bool                       `json:"create_per_subfolder"`
}

// webLibraryPayload 是 /api/libraries 返回的库条目：本地库与远程 Emby 挂载库
// 统一结构（远程库附加 is_remote_emby / remote_source 只读标记）。
type webLibraryPayload struct {
	model.Library
	IsRemoteEmby bool                 `json:"is_remote_emby,omitempty"`
	RemoteSource string               `json:"remote_source,omitempty"`
	Total        int64                `json:"total,omitempty"`
	Cards        []service.SeriesCard `json:"cards,omitempty"`
}

// remoteLibraryItemTypes 远程库内容拉取时按 CollectionType 过滤直属条目，
// 避免电影库里的合集文件夹(Folder) 漏出为电影卡片。
func remoteLibraryItemTypes(collectionType string) string {
	switch collectionType {
	case "movies":
		return "Movie"
	case "tvshows":
		return "Series"
	}
	return ""
}

func listLibrariesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		libs, err := svc.Media.ListLibraries(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		role, _ := c.Get(middleware.CtxUserRole)
		includeHidden := role == "admin" && (c.Query("include_hidden") == "1" || c.Query("include_hidden") == "true" || c.Query("all") == "1")
			if !includeHidden {
				libs = service.FilterDisplayCloudLibraries(ctx, svc.Repo, libs)
				visibility := mediaVisibilityForRequest(c, svc)
				filtered := libs[:0]
				for _, lib := range libs {
					if service.LibraryVisibleForUser(ctx, svc.Repo, lib, visibility) {
						filtered = append(filtered, lib)
					}
				}
				libs = filtered
			}
			rawIDs := strings.TrimSpace(c.Query("ids"))
			var targetSet map[string]struct{}
			if rawIDs != "" {
				targetSet = make(map[string]struct{})
				for _, id := range strings.Split(rawIDs, ",") {
					id = strings.TrimSpace(id)
					if id != "" {
						targetSet[id] = struct{}{}
					}
				}
			}
			if len(targetSet) > 0 {
				filtered := libs[:0]
				for _, lib := range libs {
					if _, ok := targetSet[lib.ID]; ok {
						filtered = append(filtered, lib)
					}
				}
				libs = filtered
			}
		withPreview := c.Query("with_preview") == "1" || c.Query("with_preview") == "true"
		limit := 10
		if withPreview {
			limit, _ = strconv.Atoi(c.DefaultQuery("preview_limit", c.DefaultQuery("limit", "10")))
			if limit <= 0 {
				limit = 10
			} else if limit > 100 {
				limit = 100
			}
		}
		out := make([]webLibraryPayload, 0, len(libs)+8)
		if withPreview {
			previews, err := svc.Media.ListLibrariesWithPreview(ctx, libs, mediaVisibilityForRequest(c, svc), limit)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			for _, p := range previews {
				out = append(out, webLibraryPayload{Library: p.Library, Total: p.Total, Cards: p.Cards})
			}
			} else {
				visibility := mediaVisibilityForRequest(c, svc)
				libIDs := make([]string, len(libs))
				for i, l := range libs {
					libIDs[i] = l.ID
				}
				counts, _ := svc.Repo.Media.CountByLibraries(ctx, libIDs, repository.MediaQueryFilter{
					IncludeNSFW:       visibility.IncludeNSFW,
					AllowedLibraryIDs: visibility.AllowedLibraryIDs,
					HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
				})
				for _, l := range libs {
					var total int64
					if counts != nil {
						total = counts[l.ID]
					}
					out = append(out, webLibraryPayload{Library: l, Total: total})
				}
			}
		// 远程 Emby 挂载库追加在本地库之后（非管理员视图仍受 allowed_library_ids 约束）。
		if svc.EmbyRemote != nil {
			if views, err := svc.EmbyRemote.RemoteLibraries(ctx); err == nil {
					visibility := mediaVisibilityForRequest(c, svc)
					allowedViews := make([]service.RemoteLibraryView, 0, len(views))
					for _, v := range views {
						if !includeHidden && !service.LibraryVisibleForUser(ctx, svc.Repo, v.Library, visibility) {
							continue
						}
						if len(targetSet) > 0 {
							if _, ok := targetSet[v.Library.ID]; !ok {
								continue
							}
						}
						allowedViews = append(allowedViews, v)
					}
				remotePayloads := make([]webLibraryPayload, len(allowedViews))
				for i, v := range allowedViews {
					remotePayloads[i] = webLibraryPayload{Library: v.Library, IsRemoteEmby: true, RemoteSource: v.AccountName}
				}
				if withPreview && len(allowedViews) > 0 {
					const maxRemotePreviewWorkers = 6
					sem := make(chan struct{}, maxRemotePreviewWorkers)
					var wg sync.WaitGroup
					for i, v := range allowedViews {
						i, v := i, v
						wg.Add(1)
						go func() {
							defer wg.Done()
							select {
							case sem <- struct{}{}:
								defer func() { <-sem }()
							case <-ctx.Done():
								return
							}
							helper.Run(svc.Log, "media.remotePreview", func() {
								acct := svc.EmbyRemote.AccountByID(ctx, v.AccountID)
								if acct == nil {
									return
								}
									tmpMount := &model.EmbyMount{
										Base:           model.Base{ID: v.MountID},
										AccountID:      v.AccountID,
										RemoteViewID:   v.RemoteID,
										CollectionType: v.CollectionType,
										Name:           v.Library.Name,
									}
								itemTypes := remoteLibraryItemTypes(v.CollectionType)
								if _, total, err := svc.EmbyRemote.RemoteLibraryMedia(ctx, tmpMount, acct, v.RemoteID, itemTypes, 0, 1); err == nil {
									remotePayloads[i].Total = total
								}
								if cards, err := svc.EmbyRemote.RemoteLatestCards(ctx, tmpMount, acct, v.RemoteID, limit); err == nil {
									remotePayloads[i].Cards = cards
								}
							})
						}()
					}
					wg.Wait()
				}
				out = append(out, remotePayloads...)
			}
		}
		c.JSON(http.StatusOK, out)
	}
}

func getLibraryHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		id := c.Param("id")
		// 远程 Emby 挂载库详情。
		if svc.EmbyRemote != nil && service.IsEmbyRemoteID(id) {
			mountID, remoteID, _ := service.DecodeEmbyRemoteID(id)
			view, err := svc.EmbyRemote.RemoteLibraryByID(ctx, mountID, remoteID)
			if err != nil || view == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			role, _ := c.Get(middleware.CtxUserRole)
			includeHidden := role == "admin" && (c.Query("include_hidden") == "1" || c.Query("include_hidden") == "true" || c.Query("all") == "1")
			if !includeHidden && !service.LibraryVisibleForUser(ctx, svc.Repo, view.Library, mediaVisibilityForRequest(c, svc)) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			c.JSON(http.StatusOK, webLibraryPayload{Library: view.Library, IsRemoteEmby: true, RemoteSource: view.AccountName})
			return
		}
		lib, err := svc.Repo.Library.FindByID(ctx, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if lib == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		role, _ := c.Get(middleware.CtxUserRole)
		includeHidden := role == "admin" && (c.Query("include_hidden") == "1" || c.Query("include_hidden") == "true" || c.Query("all") == "1")
		if !includeHidden {
			libs := service.FilterDisplayCloudLibraries(ctx, svc.Repo, []model.Library{*lib})
			if len(libs) == 0 || !service.LibraryVisibleForUser(ctx, svc.Repo, libs[0], mediaVisibilityForRequest(c, svc)) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			c.JSON(http.StatusOK, webLibraryPayload{Library: libs[0]})
		} else {
			c.JSON(http.StatusOK, webLibraryPayload{Library: *lib})
		}
	}
}

func createLibraryHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createLibraryReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		roots := req.Roots
		if len(roots) == 0 {
			for _, path := range req.Paths {
				roots = append(roots, service.LibraryRootInput{Path: path})
			}
		}
		if len(roots) == 0 && strings.TrimSpace(req.Path) != "" {
			roots = append(roots, service.LibraryRootInput{Path: req.Path})
		}
		var l *model.Library
		if req.CreatePerSubfolder {
			parent := ""
			if len(roots) > 0 {
				parent = roots[0].Path
			} else if strings.TrimSpace(req.Path) != "" {
				parent = req.Path
			}
			created, err := svc.Media.CreateLibrariesPerSubfolder(c.Request.Context(), parent, req.Type, req.CoverURL)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			uid, _ := c.Get("ctx_user_id")
			for i := range created {
				lib := &created[i]
				svc.Audit.Record(c.Request.Context(), toString(uid), "library.create", lib.ID, c.ClientIP(), lib.Path)
				if svc.Watcher != nil {
					go func() { _ = svc.Watcher.Refresh(context.Background()) }()
				}
				for _, root := range lib.Roots {
					if root.Enabled {
						queueLibraryRootScan(svc, lib.ID, root.ID)
					}
				}
			}
			c.JSON(http.StatusCreated, gin.H{"libraries": created})
			return
		}
		l, err := svc.Media.CreateLibraryWithRootsAndCover(c.Request.Context(), req.Name, req.Type, req.CoverURL, roots)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		uid, _ := c.Get("ctx_user_id")
		svc.Audit.Record(c.Request.Context(), toString(uid), "library.create", l.ID, c.ClientIP(), l.Path)
		// Refresh fsnotify watcher to pick up the new library root, then perform
		// an initial scan. Without the scan a newly-created library remained
		// empty until the operator pressed the separate "扫描" action.
		if svc.Watcher != nil {
			go func() { _ = svc.Watcher.Refresh(context.Background()) }()
		}
		if len(l.Roots) == 0 {
			queueLibraryRootScan(svc, l.ID, "")
		} else {
			for _, root := range l.Roots {
				if root.Enabled {
					queueLibraryRootScan(svc, l.ID, root.ID)
				}
			}
		}
		c.JSON(http.StatusCreated, l)
	}
}

type updateLibraryReq struct {
	CoverURL        *string `json:"cover_url"`
	SortOrder       *int    `json:"sort_order"`
	CarouselEnabled *bool   `json:"carousel_enabled"`
}

func updateLibraryHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req updateLibraryReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.CoverURL != nil {
			if err := svc.Media.UpdateLibraryCover(c.Request.Context(), c.Param("id"), *req.CoverURL); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
		if req.SortOrder != nil || req.CarouselEnabled != nil {
			if err := svc.Media.UpdateLibraryFields(c.Request.Context(), c.Param("id"), req.SortOrder, req.CarouselEnabled); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
		lib, err := svc.Repo.Library.FindByID(c.Request.Context(), c.Param("id"))
		if err != nil || lib == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "library not found"})
			return
		}
		c.JSON(http.StatusOK, lib)
	}
}

type reorderLibrariesReq struct {
	IDs []string `json:"ids" binding:"required"`
}

func reorderLibrariesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req reorderLibrariesReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := svc.Media.ReorderLibraries(c.Request.Context(), req.IDs); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"updated": len(req.IDs)})
	}
}

func deleteLibraryHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if err := svc.Media.DeleteLibrary(c.Request.Context(), id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		uid, _ := c.Get("ctx_user_id")
		svc.Audit.Record(c.Request.Context(), toString(uid), "library.delete", id, c.ClientIP(), "")
		// goroutine 内的 panic 无法被 gin.Recovery 捕获，会直接崩掉进程：
		// 与其他调用点一致先判空。
		if svc.Watcher != nil {
			go func() { _ = svc.Watcher.Refresh(context.Background()) }()
		}
		c.Status(http.StatusNoContent)
	}
}

func listMediaHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		ctx := c.Request.Context()
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		size, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
		// 远程 Emby 库：转发远程直属条目并映射为本地 Media 结构（分页由远程承接）。
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
			itemTypes := ""
			if view, err := svc.EmbyRemote.RemoteLibraryByID(ctx, mountID, remoteID); err == nil && view != nil {
				itemTypes = remoteLibraryItemTypes(view.CollectionType)
			}
			items, total, err := svc.EmbyRemote.RemoteLibraryMedia(ctx, mount, acct, remoteID, itemTypes, (page-1)*size, size)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if items == nil {
				items = []model.Media{}
			}
			c.JSON(http.StatusOK, gin.H{
				"items":     items,
				"total":     total,
				"page":      page,
				"page_size": size,
			})
			return
		}
		groupVersions := c.DefaultQuery("group_versions", "1") != "0"
		if !groupVersions {
			items, total, err := svc.Media.ListMediaVisible(c.Request.Context(), id, page, size, mediaVisibilityForRequest(c, svc))
			if err != nil {
				writeInternalOrCanceled(c, err)
				return
			}
			if items == nil {
				items = []model.Media{}
			}
			c.JSON(http.StatusOK, gin.H{
				"items":     items,
				"total":     total,
				"page":      page,
				"page_size": size,
			})
			return
		}
		items, total, err := svc.Media.ListMediaVisibleGrouped(c.Request.Context(), id, page, size, mediaVisibilityForRequest(c, svc))
		if err != nil {
			writeInternalOrCanceled(c, err)
			return
		}
		if items == nil {
			items = []service.MediaItem{}
		}
		c.JSON(http.StatusOK, gin.H{
			"items":     items,
			"total":     total,
			"page":      page,
			"page_size": size,
		})
	}
}

func getMediaHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		id := c.Param("id")
		// 远程 Emby 条目：拉远程详情并映射为本地 Media 结构。
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
			m, err := svc.EmbyRemote.RemoteMediaDetail(ctx, mount, acct, remoteID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if m == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			if !mediaVisibleForRequest(c, svc, m) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			c.JSON(http.StatusOK, m)
			return
		}
		m, err := svc.Media.GetMediaItem(ctx, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if m == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if !mediaVisibleForRequest(c, svc, &m.Media) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusOK, m)
	}
}

func updateMediaMetadataHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req service.MediaMetadataUpdate
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		m, err := svc.Media.UpdateMetadata(c.Request.Context(), c.Param("id"), req)
		if err != nil {
			status := http.StatusInternalServerError
			if strings.Contains(strings.ToLower(err.Error()), "not found") {
				status = http.StatusNotFound
			} else if strings.Contains(strings.ToLower(err.Error()), "required") {
				status = http.StatusBadRequest
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, m)
	}
}

func paginateSlice[T any](items []T, page, size int) []T {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = 50
	}
	if len(items) == 0 {
		return []T{}
	}
	start := (page - 1) * size
	if start >= len(items) {
		return []T{}
	}
	end := start + size
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func searchMediaHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		q := c.Query("q")
		visibility := mediaVisibilityForRequest(c, svc)
		groupVersions := c.DefaultQuery("group_versions", "1") != "0"

		fetchRemote := func(limit int) []model.Media {
			if svc.EmbyRemote == nil || strings.TrimSpace(q) == "" {
				return nil
			}
			remoteItems, _ := svc.EmbyRemote.RemoteSearchMedia(ctx, q, limit, visibility)
			return remoteItems
		}

		if c.Query("page") != "" || c.Query("page_size") != "" {
			page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
			size, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
			if !groupVersions {
				localItems, _, err := svc.Media.SearchMediaVisiblePage(ctx, q, 1, 50000, visibility)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				remoteItems := fetchRemote(size * 2)
				all := append(localItems, remoteItems...)
				paged := paginateSlice(all, page, size)
				c.JSON(http.StatusOK, gin.H{
					"items":     paged,
					"total":     len(all),
					"page":      page,
					"page_size": size,
				})
				return
			}
			localItems, err := svc.Media.SearchMediaVisible(ctx, q, 50000, visibility)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			remoteItems := fetchRemote(size * 2)
			all := append(localItems, remoteItems...)
			grouped := service.GroupMediaVersions(all)
			paged := service.PaginateMediaItems(grouped, page, size)
			c.JSON(http.StatusOK, gin.H{
				"items":     paged,
				"total":     len(grouped),
				"page":      page,
				"page_size": size,
			})
			return
		}

		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		if limit <= 0 {
			limit = 50
		}
		if !groupVersions {
			localItems, err := svc.Media.SearchMediaVisible(ctx, q, limit, visibility)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			remoteItems := fetchRemote(limit)
			all := append(localItems, remoteItems...)
			if len(all) > limit {
				all = all[:limit]
			}
			if all == nil {
				all = []model.Media{}
			}
			c.JSON(http.StatusOK, gin.H{"items": all})
			return
		}

		localItems, err := svc.Media.SearchMediaVisible(ctx, q, 50000, visibility)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		remoteItems := fetchRemote(limit)
		all := append(localItems, remoteItems...)
		grouped := service.GroupMediaVersions(all)
		items := service.FirstMediaItems(grouped, limit)
		if items == nil {
			items = []service.MediaItem{}
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	}
}

func streamHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		id := c.Param("id")
		// 远程 Emby 条目：按挂载代理配置分流——代理走 MeBox 反代，否则 302 直连。
		if svc.EmbyRemote != nil && service.IsEmbyRemoteID(id) {
			if !enforceScopedPlaybackToken(c, id) {
				return
			}
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
			if mount.ProxyPlay {
				if err := svc.Emby.ProxyRemoteVideoStream(ctx, c.Writer, c.Request, mountID, remoteID); err != nil {
					if !c.Writer.Written() {
						c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
					}
				}
				return
			}
				target, err := svc.EmbyRemote.WebStreamURL(ctx, acct, remoteID)
				if err != nil {
					c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
					return
				}
				// 现代浏览器在 HTTPS 页面中请求不安全源（HTTP 视频流）会直接报 Mixed Content 拦截导致播放失败。
				// 仅当当前前端请求为 HTTPS 且远程直连目标为 HTTP 时，自动降级通过本机反向代理传输流，避免播放被浏览器阻断；
				// 其它场景（HTTP 页面访问 HTTP/HTTPS，或 HTTPS 访问 HTTPS）继续 302 直连，最大化节省服务器带宽与流量。
				if requestIsHTTPS(c) && strings.HasPrefix(strings.ToLower(target), "http://") {
					if err := svc.Emby.ProxyRemoteVideoStream(ctx, c.Writer, c.Request, mountID, remoteID); err != nil {
						if !c.Writer.Written() {
							c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
						}
					}
					return
				}
				setRedirectNoStoreHeaders(c)
				c.Redirect(http.StatusFound, target)
				return
		}
		m, err := svc.Media.GetMedia(ctx, id)
		if err != nil || m == nil || !mediaVisibleForRequest(c, svc, m) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if !enforceScopedPlaybackToken(c, m.ID) {
			return
		}
		err = svc.Stream.ServeFile(c.Writer, c.Request, c.Param("id"))
		if errors.Is(err, service.ErrMediaNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if errors.Is(err, service.ErrCloudPlaybackDisabled) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, service.ErrCloudPlaybackUnavailable) {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
}
