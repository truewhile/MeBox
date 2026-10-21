// Package handler — library / media HTTP endpoints.
package handler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	IsRemoteEmby bool                     `json:"is_remote_emby,omitempty"`
	RemoteSource string                   `json:"remote_source,omitempty"`
	Total        int64                    `json:"total,omitempty"`
	Cards        []service.SeriesCardView `json:"cards,omitempty"`
	// CoverImageURL 是客户端可直接使用的「有效封面」：管理员自定义的
	// cover_url 优先，否则指向服务端生成的成员海报拼图接口。Web / App 都
	// 读这一个字段，避免各自用卡片在前端拼格子、长得还不一样。
	CoverImageURL string `json:"cover_image_url,omitempty"`
}

// effectiveLibraryCoverURL 计算库封面的有效地址。远程挂载库的封面由上游
// Emby 决定，这里返回空串，调用方继续用卡片拼图兜底。
func effectiveLibraryCoverURL(lib model.Library, isRemote bool) string {
	if isRemote {
		return ""
	}
	if raw := strings.TrimSpace(lib.CoverURL); raw != "" {
		return raw
	}
	if strings.TrimSpace(lib.ID) == "" {
		return ""
	}
	return "/api/libraries/" + url.PathEscape(lib.ID) + "/cover"
}

// remoteLibraryPreviewBudget 限制 /api/libraries?with_preview=1 里远程 Emby
// 库预览（总条数 + 最新卡片）的总耗时。这些预览只用于首页卡片，值得为它
// 排队等待的量很小；此前它跟随请求上下文，上游超时会让整页等 15 秒以上。
var remoteLibraryPreviewBudget = 3 * time.Second

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
			var previews []service.LibraryPreviewItem
			if c.Query("include_total") == "0" {
				previews, err = svc.Media.ListLibraryPreviews(ctx, libs, mediaVisibilityForRequest(c, svc), limit)
			} else {
				previews, err = svc.Media.ListLibrariesWithPreview(ctx, libs, mediaVisibilityForRequest(c, svc), limit)
			}
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			for _, p := range previews {
				out = append(out, webLibraryPayload{
					Library:       p.Library,
					Total:         p.Total,
					Cards:         service.NewSeriesCardViews(p.Cards),
					CoverImageURL: effectiveLibraryCoverURL(p.Library, false),
				})
			}
		} else {
			visibility := mediaVisibilityForRequest(c, svc)
			libIDs := make([]string, len(libs))
			for i, l := range libs {
				libIDs[i] = l.ID
			}
			counts, _ := svc.Media.CountLibrariesCached(ctx, libIDs, repository.MediaQueryFilter{
				IncludeNSFW:       visibility.IncludeNSFW,
				AllowedLibraryIDs: visibility.AllowedLibraryIDs,
				HiddenLibraryIDs:  visibility.HiddenLibraryIDs,
			})
			for _, l := range libs {
				var total int64
				if counts != nil {
					total = counts[l.ID]
				}
				out = append(out, webLibraryPayload{Library: l, Total: total, CoverImageURL: effectiveLibraryCoverURL(l, false)})
			}
		}
		// 精确指定目标库时，如果目标全是本地库，就不必枚举远程挂载。
		// 首页预览会拆成多个小批次请求，跳过无关远程调用可以明显缩短
		// 每批次的尾延迟；未指定 ids 的完整库列表仍保持原行为。
		includeRemote := true
		if len(targetSet) > 0 {
			includeRemote = false
			for id := range targetSet {
				if service.IsEmbyRemoteID(id) {
					includeRemote = true
					break
				}
			}
		}

		// 远程 Emby 挂载库追加在本地库之后（非管理员视图仍受 allowed_library_ids 约束）。
		if includeRemote && svc.EmbyRemote != nil {
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
					// 远程库预览只是首页卡片上的「最新几条」，绝不能拖住整个
					// 库列表：上游 Emby 一旦超时，每个远程库都要等满
					// embyRemoteHTTPTimeout（15s），串起来就是 17–30 秒。
					// 给整段预览一个总预算，超时就只返回库名，卡片留给下一次刷新。
					previewCtx, cancelPreview := context.WithTimeout(ctx, remoteLibraryPreviewBudget)
					defer cancelPreview()
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
							case <-previewCtx.Done():
								return
							}
							helper.Run(svc.Log, "media.remotePreview", func() {
								acct := svc.EmbyRemote.AccountByID(previewCtx, v.AccountID)
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
								if _, total, err := svc.EmbyRemote.RemoteLibraryMedia(previewCtx, tmpMount, acct, v.RemoteID, itemTypes, 0, 1); err == nil {
									remotePayloads[i].Total = total
								}
								if cards, err := svc.EmbyRemote.RemoteLatestCards(previewCtx, tmpMount, acct, v.RemoteID, limit); err == nil {
									remotePayloads[i].Cards = service.NewSeriesCardViews(cards)
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
			c.JSON(http.StatusOK, webLibraryPayload{Library: libs[0], CoverImageURL: effectiveLibraryCoverURL(libs[0], false)})
		} else {
			c.JSON(http.StatusOK, webLibraryPayload{Library: *lib, CoverImageURL: effectiveLibraryCoverURL(*lib, false)})
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

// parseLibraryFilters 解析媒体库列表的筛选查询参数。
//
// 全部参数都是可选的：缺省时返回零值，`MediaListFilters.empty()` 为真，列表
// 行为与此前完全一致（不引入任何默认筛选）。
//
// 参数约定：
//   - genre=Action&genre=Comedy  类型多选（或关系，整词匹配）
//   - year_min / year_max        年份区间，0 或非法值表示不限
//   - rating_min                 评分下限（浮点）
//   - unwatched=1                仅显示未看完；用户 ID 取自会话
func parseLibraryFilters(c *gin.Context) service.MediaListFilters {
	filters := service.MediaListFilters{
		Genres:    parseRepeatedQueryValues(c, "genre"),
		YearMin:   parseNonNegativeInt(firstQueryValue(c, "year_min", "yearMin")),
		YearMax:   parseNonNegativeInt(firstQueryValue(c, "year_max", "yearMax")),
		RatingMin: parseNonNegativeFloat(firstQueryValue(c, "rating_min", "ratingMin")),
	}
	if isTruthyQuery(firstQueryValue(c, "unwatched", "unwatched_only", "unwatchedOnly")) {
		filters.Unwatched = true
		filters.UserID = toString(mustSessionUserID(c))
	}
	return filters
}

// parseRepeatedQueryValues 读取可重复出现的查询参数，去重并丢弃空值。
// 同时接受 key[] 括号格式（axios 1.x 默认序列化方式）作为向后兼容回退，
// 在前端 paramsSerializer 未正确配置时不会静默返回空结果。
func parseRepeatedQueryValues(c *gin.Context, key string) []string {
	raw := c.QueryArray(key)
	if len(raw) == 0 {
		// fallback: axios bracket format (e.g. genre[]=Action&genre[]=Comedy)
		raw = c.QueryArray(key + "[]")
	}
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		// 客户端可能把多值拼成一次逗号分隔，两种形式都要接受。
		for _, part := range strings.Split(value, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; ok {
				continue
			}
			seen[trimmed] = struct{}{}
			out = append(out, trimmed)
		}
	}
	return out
}

func parseNonNegativeInt(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func parseNonNegativeFloat(raw string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value < 0 || math.IsNaN(value) {
		return 0
	}
	return value
}

func isTruthyQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// mustSessionUserID 取会话用户 ID，缺失时返回空串（筛选逻辑会忽略它）。
func mustSessionUserID(c *gin.Context) any {
	uid, _ := c.Get(middleware.CtxUserID)
	return uid
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
		sortSpec := parseMediaSort(c)
		var history map[string]time.Time
		if sortSpec.Field == "last_played" {
			history = mediaHistoryMap(c, svc)
		}
		filters := parseLibraryFilters(c)
		groupVersions := c.DefaultQuery("group_versions", "1") != "0"
		if !groupVersions {
			items, total, err := svc.Media.ListMediaVisibleFiltered(ctx, id, page, size, mediaVisibilityForRequest(c, svc), filters)
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
		items, total, err := svc.Media.ListMediaVisibleSortedPage(ctx, id, page, size, mediaVisibilityForRequest(c, svc), filters, sortSpec, history)
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
	if page-1 > len(items)/size {
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
		query := strings.TrimSpace(c.Query("q"))
		visibility := mediaVisibilityForRequest(c, svc)
		filter := service.MediaSearchFilter{
			LibraryID: strings.TrimSpace(c.Query("library_id")),
			YearMin:   boundedSearchYear(c.Query("year_min")),
			YearMax:   boundedSearchYear(c.Query("year_max")),
		}
		for _, genre := range c.QueryArray("genre") {
			if genre = strings.TrimSpace(genre); genre != "" && len(filter.Genres) < 8 {
				filter.Genres = append(filter.Genres, genre)
			}
		}
		if filter.YearMin > 0 && filter.YearMax > 0 && filter.YearMin > filter.YearMax {
			filter.YearMin, filter.YearMax = filter.YearMax, filter.YearMin
		}
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		if page < 1 {
			page = 1
		}
		size, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
		if size < 1 {
			size = 50
		} else if size > 2000 {
			size = 2000
		}
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		if limit < 1 {
			limit = 50
		} else if limit > 50000 {
			limit = 50000
		}
		paginated := c.Query("page") != "" || c.Query("page_size") != ""
		sortBy := normalizeMediaSearchSort(c.Query("sort"))
		// 远程搜索与本地查询并行发起：本地结果不再排在远程超时之后才返回。
		remoteCh := startRemoteMediaSearch(ctx, svc, query, visibility, filter)
		// Sorting and grouping must operate on the same candidate set before
		// either a page size or the caller's output limit is applied.
		items, err := svc.Media.SearchMediaVisibleFiltered(ctx, query, 50000, visibility, filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if remoteCh != nil {
			for _, item := range <-remoteCh {
				items = append(items, item)
			}
		}
		if c.DefaultQuery("group_series", "0") != "0" {
			items = service.GroupMediaSeriesItems(items)
			sortSearchMedia(items, sortBy)
			writeMediaSearchResults(c, items, page, size, limit, paginated)
			return
		}
		if c.DefaultQuery("group_versions", "1") != "0" {
			grouped := service.GroupMediaVersions(items)
			if sortBy != "" {
				sort.SliceStable(grouped, func(i, j int) bool {
					return mediaSearchSortLess(grouped[i].Media, grouped[j].Media, sortBy)
				})
			}
			writeMediaSearchResults(c, grouped, page, size, limit, paginated)
			return
		}
		sortSearchMedia(items, sortBy)
		writeMediaSearchResults(c, items, page, size, limit, paginated)
	}
}

const (
	// remoteSearchItemLimit 是每个远程 Emby 挂载点返回的条目上限。此前这里传
	// 50000（等于让上游做一次全库查询），上游稍慢就会一路跑到 5 秒超时，把每次
	// 搜索都拖成秒级；搜索场景只需要少量候选，多出来的结果也会被分页丢掉。
	remoteSearchItemLimit = 100
	// remoteSearchBudget 限制远程结果最多给一次请求增加多少延迟。
	remoteSearchBudget = 2 * time.Second
	// remoteSearchCacheTTL 让同一关键词的翻页与重复搜索复用远程结果，避免每翻
	// 一页都重新打一遍远程挂载点。
	remoteSearchCacheTTL = 30 * time.Second
	// remoteSearchFailureCacheTTL 用于上游超时/失败时的短缓存：部分结果只保留
	// 几秒，避免反复吃满 remoteSearchBudget，又不至于长期压住后续重试。
	remoteSearchFailureCacheTTL = 5 * time.Second
)

// startRemoteMediaSearch 并发发起远程搜索，返回一个只投递一次结果的通道。
// 不满足远程搜索条件时返回 nil。通道带缓冲，调用方即使提前返回也不会泄漏
// goroutine；请求结束时请求上下文被取消，远程调用会随之退出。
func startRemoteMediaSearch(
	ctx context.Context,
	svc *service.Container,
	query string,
	visibility service.MediaVisibility,
	filter service.MediaSearchFilter,
) <-chan []model.Media {
	if svc == nil || svc.EmbyRemote == nil || query == "" {
		return nil
	}
	if filter.LibraryID != "" && !service.IsEmbyRemoteID(filter.LibraryID) {
		return nil
	}
	results := make(chan []model.Media, 1)
	go func() {
		results <- remoteMediaSearchItems(ctx, svc, query, visibility, filter)
	}()
	return results
}

func remoteMediaSearchItems(
	ctx context.Context,
	svc *service.Container,
	query string,
	visibility service.MediaVisibility,
	filter service.MediaSearchFilter,
) []model.Media {
	cacheKey := remoteSearchCacheKey(query, visibility)
	if svc.Cache != nil {
		if cached, ok := svc.Cache.GetObject(cacheKey); ok {
			if rows, ok := cached.([]model.Media); ok {
				return filterRemoteSearchItems(rows, filter)
			}
		}
	}
	searchCtx, cancel := context.WithTimeout(ctx, remoteSearchBudget)
	defer cancel()
	remote, _ := svc.EmbyRemote.RemoteSearchMedia(searchCtx, query, remoteSearchItemLimit, visibility)
	if svc.Cache != nil {
		// 空结果也要缓存：否则查不到片名时每次都会重新等到 remoteSearchBudget。
		if remote == nil {
			remote = []model.Media{}
		}
		ttl := remoteSearchCacheTTL
		if searchCtx.Err() != nil {
			ttl = remoteSearchFailureCacheTTL
		}
		svc.Cache.SetObject(cacheKey, remote, ttl)
	}
	return filterRemoteSearchItems(remote, filter)
}

func filterRemoteSearchItems(remote []model.Media, filter service.MediaSearchFilter) []model.Media {
	if len(remote) == 0 {
		return nil
	}
	out := make([]model.Media, 0, len(remote))
	for _, item := range remote {
		if remoteSearchMatchesFilter(item, filter) {
			out = append(out, item)
		}
	}
	return out
}

// remoteSearchCacheKey 按可见范围隔离远程搜索结果，避免把某个用户可见挂载
// 点的结果复用给可见范围不同的用户。
func remoteSearchCacheKey(query string, visibility service.MediaVisibility) string {
	allowed := append([]string(nil), visibility.AllowedLibraryIDs...)
	hidden := append([]string(nil), visibility.HiddenLibraryIDs...)
	sort.Strings(allowed)
	sort.Strings(hidden)
	return fmt.Sprintf("media:obj:remote-search:%t|%s|%s|%s",
		visibility.IncludeNSFW, strings.Join(allowed, ","), strings.Join(hidden, ","), query)
}

func writeMediaSearchResults[T any](c *gin.Context, items []T, page, size, limit int, paginated bool) {
	if items == nil {
		items = []T{}
	}
	if paginated {
		c.JSON(http.StatusOK, gin.H{
			"items": paginateSlice(items, page, size),
			"total": len(items), "page": page, "page_size": size,
		})
		return
	}
	if len(items) > limit {
		items = items[:limit]
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func remoteSearchMatchesFilter(item model.Media, filter service.MediaSearchFilter) bool {
	if filter.LibraryID != "" && item.LibraryID != filter.LibraryID && item.DisplayLibraryID != filter.LibraryID {
		return false
	}
	if filter.YearMin > 0 && item.Year < filter.YearMin || filter.YearMax > 0 && item.Year > filter.YearMax {
		return false
	}
	if len(filter.Genres) == 0 {
		return true
	}
	normalize := func(value string) string {
		return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	}
	for _, genre := range strings.Split(strings.ReplaceAll(item.Genres, "，", ","), ",") {
		for _, wanted := range filter.Genres {
			if normalize(genre) == normalize(wanted) {
				return true
			}
		}
	}
	return false
}

func normalizeMediaSearchSort(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "title_asc", "year_desc", "rating_desc":
		return value
	default:
		return ""
	}
}

func sortSearchMedia(items []model.Media, sortBy string) {
	if sortBy == "" {
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		return mediaSearchSortLess(items[i], items[j], sortBy)
	})
}

func mediaSearchSortLess(a, b model.Media, sortBy string) bool {
	switch sortBy {
	case "title_asc":
		left, right := strings.ToLower(strings.TrimSpace(a.Title)), strings.ToLower(strings.TrimSpace(b.Title))
		if left != right {
			return left < right
		}
	case "year_desc":
		if a.Year != b.Year {
			return a.Year > b.Year
		}
	case "rating_desc":
		if a.Rating != b.Rating {
			return a.Rating > b.Rating
		}
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID < b.ID
}

func boundedSearchYear(value string) int {
	year, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || year < 1870 || year > 2200 {
		return 0
	}
	return year
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
			// 远程条目可能是「整部剧/季/目录」：这类条目自身没有媒体源，远程 Emby 对它
			// 们的 /Videos/{id}/stream 会直接回 500（Unable to cast object of type
			// 'Series' to type 'IHasMediaSources'）。网页端、外部播放器、第三方客户端都
			// 可能把这种 ID 送进来，这里统一改投第一集：302 让客户端自己跟过去，播放器
			// 原有的 token / proxy 查询参数一并保留。
			if playable := svc.EmbyRemote.PlayableRemoteID(ctx, acct, remoteID); playable != "" && playable != remoteID {
				target := "/api/stream/" + service.EncodeEmbyRemoteID(mountID, playable)
				if rawQuery := c.Request.URL.RawQuery; rawQuery != "" {
					target += "?" + rawQuery
				}
				setRedirectNoStoreHeaders(c)
				c.Redirect(http.StatusFound, target)
				return
			}
			// 远程 Emby 条目的同源转发：VR 全景需要浏览器读帧（见 stream_proxy.go），
			// 与挂载账号的 proxy_play 开关等价。
			if mount.ProxyPlay || wantSameOriginProxy(c) {
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
		// ?proxy=1：把网盘/STRM 直链改为服务端同源转发（网页端读帧、VR 全景用），
		// 画质与原文件一致，不触发转码。
		if wantSameOriginProxy(c) {
			handled, proxyErr := proxySTRMStream(c, svc, m)
			if handled {
				writeProxyError(c, proxyErr)
				return
			}
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
