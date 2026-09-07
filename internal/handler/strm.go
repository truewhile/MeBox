// STRM 管理 HTTP 层：网盘账号 / 同步目录 / 全局设置 / 同步控制 / 下载上传队列，
// 以及公开的 strm 播放重定向端点 /api/strm/play/:provider/*filepath。
package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

// ─── 网盘账号 ──────────────────────────────────────────────────────────────────

type strmAccountReq struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider" binding:"required"`
	Config   map[string]string `json:"config"`
	Enabled  *bool             `json:"enabled"`
}

type strmAccountView struct {
	model.StrmAccount
	HasCredential bool   `json:"has_credential"`
	ProviderLabel string `json:"provider_label"`
	// ConfigPreview 非敏感配置字段，供编辑表单回显。
	ConfigPreview service.StrmAccountConfigPreview `json:"config_preview,omitempty"`
	// ProxyPlay 仅远程 Emby 挂载账号返回：播放流量是否经过 MeBox 代理（编辑回显用）。
	ProxyPlay *bool `json:"proxy_play,omitempty"`
	// EmbyLines 仅远程 Emby 挂载账号返回：多线路配置（不含凭据）。
	EmbyLines      []service.EmbyRemoteLine `json:"emby_lines,omitempty"`
	EmbyActiveLine int                      `json:"emby_active_line,omitempty"`
}

func strmAccountViews(svc *service.Container, accounts []model.StrmAccount) []strmAccountView {
	out := make([]strmAccountView, 0, len(accounts))
	for i := range accounts {
		a := accounts[i]
		view := strmAccountView{
			StrmAccount:   a,
			HasCredential: service.HasStrmAccountCredential(&a),
			ProviderLabel: providerLabelOf(a.Provider),
		}
		if svc != nil && svc.Strm != nil {
			view.ConfigPreview = svc.Strm.StrmAccountConfigPreviewOf(&a)
		}
		if a.Provider == model.StrmProviderEmbyRemote && svc != nil && svc.EmbyRemote != nil {
			if proxyPlay, err := svc.EmbyRemote.ProxyPlayOf(&a); err == nil {
				view.ProxyPlay = &proxyPlay
			}
			if lines, activeLine, err := svc.EmbyRemote.LinesOf(&a); err == nil {
				view.EmbyLines = lines
				view.EmbyActiveLine = activeLine
			}
		}
		out = append(out, view)
	}
	return out
}

func providerLabelOf(provider string) string {
	label, ok := service.StrmProviderLabels[provider]
	if !ok {
		return provider
	}
	return label
}

func listStrmAccountsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		accounts, err := svc.Strm.ListAccounts(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, strmAccountViews(svc, accounts))
	}
}

func createStrmAccountHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req strmAccountReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		acct, err := svc.Strm.CreateStrmAccount(c.Request.Context(), req.Name, req.Provider, req.Config)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		views := strmAccountViews(svc, []model.StrmAccount{*acct})
		c.JSON(http.StatusOK, views[0])
	}
}

func updateStrmAccountHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var req strmAccountReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		acct, err := svc.Strm.UpdateStrmAccount(c.Request.Context(), id, req.Name, req.Enabled, req.Config)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		views := strmAccountViews(svc, []model.StrmAccount{*acct})
		c.JSON(http.StatusOK, views[0])
	}
}

func deleteStrmAccountHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Strm.DeleteStrmAccount(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func testStrmAccountHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		acct, err := svc.Repo.StrmAccount.FindByID(c.Request.Context(), id)
		if err != nil || acct == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "网盘账号不存在"})
			return
		}
		now := time.Now()
		acct.LastTestAt = &now
		if acct.Provider == model.StrmProviderEmbyRemote && svc.EmbyRemote != nil {
			if err := svc.EmbyRemote.TestConnection(c.Request.Context(), acct); err != nil {
				acct.LastTestResult = err.Error()
				acct.LastTestOK = false
			} else {
				acct.LastTestResult = "ok"
				acct.LastTestOK = true
			}
		} else {
			acct = svc.Strm.TestStrmAccount(c.Request.Context(), id)
			if acct == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "网盘账号不存在"})
				return
			}
			c.JSON(http.StatusOK, strmAccountViews(svc, []model.StrmAccount{*acct})[0])
			return
		}
		_ = svc.Repo.StrmAccount.Update(c.Request.Context(), acct)
		c.JSON(http.StatusOK, strmAccountViews(svc, []model.StrmAccount{*acct})[0])
	}
}

func listStrmRemoteDirHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		dir := strings.TrimSpace(c.Query("dir"))
		entries, err := svc.Strm.ListRemoteDir(c.Request.Context(), c.Param("id"), dir)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, entries)
	}
}

// resolveStrmRemoteDirHandler 按远端目录引用（115 为目录 ID）反查完整展示路径。
func resolveStrmRemoteDirHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		dir := strings.TrimSpace(c.Query("dir"))
		path, err := svc.Strm.ResolveRemoteDirPath(c.Request.Context(), c.Param("id"), dir)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"path": path})
	}
}

// ─── 全局设置 ──────────────────────────────────────────────────────────────────

func getStrmSettingsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		settings, err := svc.Strm.GetStrmSettings(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, settings)
	}
}

func updateStrmSettingsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req map[string]string
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := svc.Strm.UpdateStrmSettings(c.Request.Context(), req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// ─── 同步目录 ──────────────────────────────────────────────────────────────────

type strmSyncPathReq struct {
	Name              string `json:"name"`
	AccountID         string `json:"account_id"`
	Provider          string `json:"provider"`
	RemotePath        string `json:"remote_path"`
	RemoteDisplayPath string `json:"remote_display_path"`
	LocalPath         string `json:"local_path"`
	StrmBaseURL       string `json:"strm_base_url"`
	VideoExt          string `json:"video_ext"`
	MetaExt           string `json:"meta_ext"`
	ExcludeName       string `json:"exclude_name"`
	MinVideoSizeMB    int64  `json:"min_video_size_mb"`
	AddPath           int    `json:"add_path"`
	DownloadMeta      *bool  `json:"download_meta"`
	UploadMeta        *bool  `json:"upload_meta"`
	DeleteDir         *bool  `json:"delete_dir"`
	KeepExt           *bool  `json:"keep_ext"`
	Cron              string `json:"cron"`
	EnableCron        *bool  `json:"enable_cron"`
	SyncMode          string `json:"sync_mode"`
	Enabled           *bool  `json:"enabled"`
}

type strmSyncPathView struct {
	model.StrmSyncPath
	AccountName    string `json:"account_name"`
	AccountEnabled bool   `json:"account_enabled"`
}

func strmSyncPathViews(svc *service.Container, c *gin.Context, paths []model.StrmSyncPath) []strmSyncPathView {
	out := make([]strmSyncPathView, 0, len(paths))
	for i := range paths {
		p := paths[i]
		view := strmSyncPathView{StrmSyncPath: p, AccountEnabled: true}
		if p.AccountID != "" {
			if acct, err := svc.Repo.StrmAccount.FindByID(c.Request.Context(), p.AccountID); err == nil && acct != nil {
				view.AccountName = acct.Name
				view.AccountEnabled = acct.Enabled
			}
		}
		// 历史 115 数据若尚未记录展示路径，尝试反查一次并回写数据库自愈
		if p.Provider == model.StrmProvider115 && strings.TrimSpace(p.RemoteDisplayPath) == "" && strings.TrimSpace(p.RemotePath) != "" && p.AccountID != "" {
			resolveCtx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
			if fullPath, err := svc.Strm.ResolveRemoteDirPath(resolveCtx, p.AccountID, p.RemotePath); err == nil && fullPath != "" {
				view.RemoteDisplayPath = fullPath
				p.RemoteDisplayPath = fullPath
				_ = svc.Repo.StrmSyncPath.Update(context.Background(), &p)
			}
			cancel()
		}
		out = append(out, view)
	}
	return out
}

func listStrmSyncPathsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		paths, err := svc.Strm.ListSyncPaths(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, strmSyncPathViews(svc, c, paths))
	}
}

func createStrmSyncPathHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req strmSyncPathReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		path, err := svc.Strm.CreateSyncPath(c.Request.Context(), strmSyncPathFromReq(req))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		views := strmSyncPathViews(svc, c, []model.StrmSyncPath{*path})
		c.JSON(http.StatusOK, views[0])
	}
}

func updateStrmSyncPathHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var req strmSyncPathReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		path, err := svc.Strm.UpdateSyncPath(c.Request.Context(), id, strmSyncPathFromReq(req))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		views := strmSyncPathViews(svc, c, []model.StrmSyncPath{*path})
		c.JSON(http.StatusOK, views[0])
	}
}

func deleteStrmSyncPathHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Strm.DeleteSyncPath(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func startStrmSyncHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		mode := c.Query("mode")
		if mode == "" {
			var body struct {
				Mode string `json:"mode"`
			}
			if err := c.ShouldBindJSON(&body); err == nil && body.Mode != "" {
				mode = body.Mode
			}
		}
		if err := svc.Strm.StartSync(c.Request.Context(), c.Param("id"), mode); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func cancelStrmSyncHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Strm.CancelSync(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func listStrmSyncRecordsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		records, err := svc.Strm.ListSyncRecords(c.Request.Context(), c.Query("path_id"), 50)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, records)
	}
}

func deleteStrmSyncRecordHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Param("id") == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少记录 ID"})
			return
		}
		if err := svc.Strm.DeleteSyncRecord(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func clearStrmSyncRecordsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		deleted, err := svc.Strm.ClearSyncRecords(c.Request.Context(), c.Query("path_id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "deleted": deleted})
	}
}

// ─── 下载/上传队列 ─────────────────────────────────────────────────────────────

func downloadQueueHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
		snap, err := svc.Strm.DownloadQueueSnapshot(c.Request.Context(), c.Query("status"), page, pageSize)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, snap)
	}
}

func uploadQueueHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
		snap, err := svc.Strm.UploadQueueSnapshot(c.Request.Context(), c.Query("status"), page, pageSize)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, snap)
	}
}

func cancelStrmDownloadHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Strm.CancelDownloadTask(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func retryStrmDownloadHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Strm.RetryDownloadTask(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func cancelStrmUploadHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Strm.CancelUploadTask(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func retryStrmUploadHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Strm.RetryUploadTask(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func deleteStrmDownloadHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Strm.DeleteDownloadTask(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func deleteStrmUploadHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Strm.DeleteUploadTask(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

type queueBatchReq struct {
	Action string   `json:"action" binding:"required"`
	IDs    []string `json:"ids" binding:"required"`
}

func batchActionDownloadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req queueBatchReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		n, err := svc.Strm.BatchActionDownloadTasks(c.Request.Context(), req.Action, req.IDs)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"affected": n, "action": req.Action})
	}
}

func batchActionUploadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req queueBatchReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		n, err := svc.Strm.BatchActionUploadTasks(c.Request.Context(), req.Action, req.IDs)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"affected": n, "action": req.Action})
	}
}

// ─── 下载队列批量操作 ─────────────────────────────────────────────────────────

func clearDoneDownloadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.Strm.ClearDoneDownloadTasks(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": n})
	}
}

func clearFinishedDownloadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.Strm.ClearFinishedDownloadTasks(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": n})
	}
}

func clearCanceledDownloadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.Strm.ClearCanceledDownloadTasks(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": n})
	}
}

func clearFailedDownloadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.Strm.ClearFailedDownloadTasks(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": n})
	}
}

func clearCanceledUploadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.Strm.ClearCanceledUploadTasks(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": n})
	}
}

func clearFailedUploadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.Strm.ClearFailedUploadTasks(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": n})
	}
}

func clearDoneUploadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.Strm.ClearDoneUploadTasks(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": n})
	}
}

func clearFinishedUploadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.Strm.ClearFinishedUploadTasks(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": n})
	}
}

func retryAllFailedDownloadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.Strm.RetryAllFailedDownloadTasks(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"retried": n})
	}
}

func retryAllFailedUploadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.Strm.RetryAllFailedUploadTasks(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"retried": n})
	}
}

func cancelPendingDownloadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.Strm.CancelPendingDownloadTasks(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"canceled": n})
	}
}

func cancelPendingUploadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.Strm.CancelPendingUploadTasks(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"canceled": n})
	}
}

// ─── 公开播放端点 ──────────────────────────────────────────────────────────────

// strmPlayHandler 处理 strm 文件指向的播放请求（Emby/Infuse 直接请求，无 JWT）。
func strmPlayHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		provider := strings.TrimSpace(c.Param("provider"))
		q := url.Values(c.Request.URL.Query())
		if userAgent := c.GetHeader("User-Agent"); userAgent != "" {
			q.Set("__ua", userAgent)
		}
		result, err := svc.Strm.ResolvePlay(c.Request.Context(), provider, q)
		if err != nil {
			if errors.Is(err, service.ErrStrmPlayNotFound) {
				c.Status(http.StatusNotFound)
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		switch {
		case result.LocalPath != "":
			c.Header("Accept-Ranges", "bytes")
			c.File(result.LocalPath)
		case result.Proxy && result.Link != nil:
			if err := svc.Strm.ProxyDirect(c.Request.Context(), c.Writer, c.Request, result.Link); err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			}
		case result.RedirectURL != "":
			c.Redirect(http.StatusFound, result.RedirectURL)
		default:
			c.Status(http.StatusNotFound)
		}
	}
}

// strmSyncPathFromReq 组装同步目录模型（缺省值交给服务层处理）。
func strmSyncPathFromReq(req strmSyncPathReq) *model.StrmSyncPath {
	return &model.StrmSyncPath{
		Name:              strings.TrimSpace(req.Name),
		AccountID:         strings.TrimSpace(req.AccountID),
		Provider:          strings.TrimSpace(req.Provider),
		RemotePath:        strings.TrimSpace(req.RemotePath),
		RemoteDisplayPath: strings.TrimSpace(req.RemoteDisplayPath),
		LocalPath:         strings.TrimSpace(req.LocalPath),
		StrmBaseURL:       strings.TrimSpace(req.StrmBaseURL),
		VideoExt:          req.VideoExt,
		MetaExt:           req.MetaExt,
		ExcludeName:       req.ExcludeName,
		MinVideoSizeMB:    req.MinVideoSizeMB,
		AddPath:           req.AddPath,
		DownloadMeta:      boolValue(req.DownloadMeta, true),
		UploadMeta:        boolValue(req.UploadMeta, false),
		DeleteDir:         boolValue(req.DeleteDir, false),
		KeepExt:           boolValue(req.KeepExt, false),
		Cron:              strings.TrimSpace(req.Cron),
		EnableCron:        boolValue(req.EnableCron, false),
		SyncMode:          strings.TrimSpace(req.SyncMode),
		Enabled:           boolValue(req.Enabled, true),
	}
}

func boolValue(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

// ─── 115 开放平台授权 ──────────────────────────────────────────────────────────

func listStrm115SourcesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		sources, err := svc.Strm.List115Sources(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, sources)
	}
}

type strm115OAuthStartReq struct {
	AuthSource  string `json:"auth_source" binding:"required"` // built_in_appid / custom_appid / built_in_relay / third_party_service
	AppID       string `json:"app_id"`
	Provider    string `json:"provider"`
	RedirectURL string `json:"redirect_url"`
}

type strm115OAuthPollReq struct {
	SessionID string `json:"session_id" binding:"required"`
}

func startStrm115OAuthHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req strm115OAuthStartReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		redirectURL := strings.TrimSpace(req.RedirectURL)
		if redirectURL == "" {
			// 默认回跳本服务回调端点（中继/CloudDrive 模式需要）
			redirectURL = "http://" + c.Request.Host + "/api/strm/oauth/callback"
		}
		result, err := svc.Strm.Start115OAuth(c.Request.Context(), c.Param("id"), req.AuthSource, req.AppID, req.Provider, redirectURL)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func pollStrm115OAuthHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req strm115OAuthPollReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		status, err := svc.Strm.Poll115OAuth(c.Request.Context(), req.SessionID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, status)
	}
}

// strm115OAuthCallbackHandler 处理中继 / CloudDrive 授权回跳（公开端点，
// 无鉴权；凭 authorization_id 会话 + 共享密钥（中继）校验合法性）。
func strm115OAuthCallbackHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		payload := map[string]string{}
		for key, values := range c.Request.URL.Query() {
			if len(values) > 0 {
				payload[key] = values[0]
			}
		}
		if err := c.Request.ParseForm(); err == nil {
			for key, values := range c.Request.PostForm {
				if len(values) > 0 && payload[key] == "" {
					payload[key] = values[0]
				}
			}
		}
		if err := svc.Strm.Handle115OAuthCallback(c.Request.Context(), payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// listStrmLocalDirsHandler 本地目录选择器：path 为空时返回根/盘符列表。
func listStrmLocalDirsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		listing, err := svc.Strm.ListStrmLocalDirs(c.Request.Context(), c.Query("path"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, listing)
	}
}
