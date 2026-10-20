// Emby 挂载管理 HTTP 层：远程 Emby 服务器（账号）下的媒体库挂载 CRUD，
// 以及账号远程媒体库（View）列表预览。
package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

// embyMountView 挂载的对外 JSON（附带账号信息）。
type embyMountView struct {
	model.EmbyMount
	AccountName string `json:"account_name"`
}

// embyMountInput 创建挂载的请求体（单个或批量）。
type embyMountInput struct {
	AccountID string          `json:"account_id" binding:"required"`
	Views     []embyViewInput `json:"views" binding:"required,min=1"`
}

type embyViewInput struct {
	RemoteViewID   string `json:"remote_view_id" binding:"required"`
	RemoteViewName string `json:"remote_view_name"`
	CollectionType string `json:"collection_type"`
	Name           string `json:"name"`
	ProxyPlay      bool   `json:"proxy_play"`
}

func embyMountViews(mounts []model.EmbyMount, accounts map[string]string) []embyMountView {
	out := make([]embyMountView, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, embyMountView{EmbyMount: m, AccountName: accounts[m.AccountID]})
	}
	return out
}

// embyAccountViewsHandler 列出账号上的远程媒体库（View），供挂载选择。
func embyAccountViewsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		acct := svc.EmbyRemote.AccountByID(c.Request.Context(), c.Param("id"))
		if acct == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在或已禁用"})
			return
		}
		views, err := svc.EmbyRemote.RemoteViews(c.Request.Context(), acct)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		type viewEntry struct {
			RemoteViewID   string `json:"remote_view_id"`
			RemoteViewName string `json:"remote_view_name"`
			CollectionType string `json:"collection_type"`
			ChildCount     int    `json:"child_count"`
			AlreadyMounted bool   `json:"already_mounted"`
		}
		mounted := map[string]bool{}
		if mounts, err := svc.EmbyRemote.ListMountsByAccount(c.Request.Context(), acct.ID); err == nil {
			for _, m := range mounts {
				mounted[m.RemoteViewID] = true
			}
		}
		out := make([]viewEntry, 0, len(views))
		for _, v := range views {
			viewID := service.RemoteItemIDString(v)
			if strings.TrimSpace(viewID) == "" {
				continue
			}
			out = append(out, viewEntry{
				RemoteViewID:   viewID,
				RemoteViewName: service.RemoteItemNameString(v),
				CollectionType: service.RemoteItemCollectionType(v),
				ChildCount:     service.RemoteItemChildCount(v),
				AlreadyMounted: mounted[viewID],
			})
		}
		c.JSON(http.StatusOK, out)
	}
}

// listEmbyMountsHandler 列出全部挂载。
func listEmbyMountsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		mounts, err := svc.EmbyRemote.ListMounts(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		names := map[string]string{}
		if accounts, err := svc.EmbyRemote.ListAccounts(c.Request.Context()); err == nil {
			for _, a := range accounts {
				names[a.ID] = a.Name
			}
		}
		out := embyMountViews(mounts, names)
		if out == nil {
			out = []embyMountView{}
		}
		c.JSON(http.StatusOK, out)
	}
}

// createEmbyMountsHandler 批量创建挂载（同一账号下的多个远程媒体库）。
func createEmbyMountsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req embyMountInput
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		mounts := make([]*model.EmbyMount, 0, len(req.Views))
		for _, v := range req.Views {
			mounts = append(mounts, &model.EmbyMount{
				AccountID:      req.AccountID,
				RemoteViewID:   v.RemoteViewID,
				RemoteViewName: v.RemoteViewName,
				CollectionType: v.CollectionType,
				Name:           v.Name,
				ProxyPlay:      v.ProxyPlay,
				Enabled:        true,
			})
		}
		if _, err := svc.EmbyRemote.CreateMounts(c.Request.Context(), mounts); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "created": len(mounts)})
	}
}

// fullMountEmbyAccountHandler 全量挂载：把账号所有远程媒体库一次挂载进来。
func fullMountEmbyAccountHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		acct := svc.EmbyRemote.AccountByID(c.Request.Context(), c.Param("id"))
		if acct == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在或已禁用"})
			return
		}
		proxy := c.Query("proxy") == "1" || c.Query("proxy") == "true"
		n, err := svc.EmbyRemote.FullMountAccount(c.Request.Context(), acct, proxy)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "created": n})
	}
}

// updateEmbyMountHandler 更新挂载（显示名 / 代理开关 / 启用）。
func updateEmbyMountHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Name      *string `json:"name"`
			ProxyPlay *bool   `json:"proxy_play"`
			Enabled   *bool   `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		mount, err := svc.EmbyRemote.MountByID(c.Request.Context(), c.Param("id"))
		if err != nil || mount == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "挂载不存在"})
			return
		}
		if req.Name != nil {
			mount.Name = *req.Name
		}
		if req.ProxyPlay != nil {
			mount.ProxyPlay = *req.ProxyPlay
		}
		if req.Enabled != nil {
			mount.Enabled = *req.Enabled
		}
		if _, err := svc.EmbyRemote.UpdateMount(c.Request.Context(), mount.ID, mount); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, mount)
	}
}

// deleteEmbyMountHandler 删除挂载。
func deleteEmbyMountHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.EmbyRemote.DeleteMount(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// setEmbyMountsEnabledHandler 一键启用/停用某账号下的全部挂载。
//
// 远端整站不可用时（如 Cloudflare 522），管理员可以用它把该账号的挂载
// 全部从媒体库摘掉，首页立刻只返回本地库；远端恢复后再一键启用。
func setEmbyMountsEnabledHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Enabled *bool `json:"enabled" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if svc.EmbyRemote == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "emby remote service not available"})
			return
		}
		affected, err := svc.EmbyRemote.SetMountsEnabled(c.Request.Context(), c.Param("id"), *req.Enabled)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "affected": affected})
	}
}

type reorderEmbyMountsReq struct {
	IDs []string `json:"ids" binding:"required"`
}

// reorderEmbyMountsHandler 批量重排挂载媒体库顺序。
func reorderEmbyMountsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req reorderEmbyMountsReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if svc.EmbyRemote == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "emby remote service not available"})
			return
		}
		if err := svc.EmbyRemote.ReorderMounts(c.Request.Context(), req.IDs); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}
