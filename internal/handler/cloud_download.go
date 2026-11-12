// 云下载（115 离线下载）HTTP 接口。
//
// 全部挂在 /api/admin 下（管理员专用）：首期不开放普通用户，否则单个用户
// 可以把账号的 115 云下载配额打空（见 docs/cloud-download-plan.md 第 9 节）。
package handler

import (
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

// cloudDownloadUploadHardLimit 是 .torrent 上传的硬上限（字节）。
// 真实上限由设置项 cloud_download.torrent_max_mb 控制，这里只防内存打爆。
const cloudDownloadUploadHardLimit = 256 << 20

func registerAdminCloudDownloadRoutes(admin *gin.RouterGroup, svc *service.Container) {
	admin.GET("/cloud-downloads", listCloudDownloadsHandler(svc))
	admin.POST("/cloud-downloads", createCloudDownloadsHandler(svc))
	admin.POST("/cloud-downloads/torrent", uploadCloudDownloadTorrentHandler(svc))
	admin.GET("/cloud-downloads/quota", cloudDownloadQuotaHandler(svc))
	admin.POST("/cloud-downloads/:id/retry", retryCloudDownloadHandler(svc))
	admin.POST("/cloud-downloads/:id/retry-pipeline", retryCloudDownloadPipelineHandler(svc))
	admin.POST("/cloud-downloads/:id/cancel", cancelCloudDownloadHandler(svc))
	admin.DELETE("/cloud-downloads/:id", deleteCloudDownloadHandler(svc))
	admin.POST("/cloud-downloads/clear-finished", clearFinishedCloudDownloadsHandler(svc))
}

func listCloudDownloadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, _ := strconv.Atoi(c.Query("page"))
		pageSize, _ := strconv.Atoi(c.Query("page_size"))
		res, err := svc.CloudDownload.List(c.Request.Context(), repository.CloudDownloadQuery{
			Status:     c.Query("status"),
			SyncPathID: c.Query("sync_path_id"),
			AccountID:  c.Query("account_id"),
			Page:       page,
			PageSize:   pageSize,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

func createCloudDownloadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req service.CloudDownloadSubmit
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// 逐条结果里已经包含失败原因，因此部分失败仍返回 200，前端按 items 展示。
		res, err := svc.CloudDownload.Submit(c.Request.Context(), req)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

func uploadCloudDownloadTorrentHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cloudDownloadUploadHardLimit)
		fileHeader, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少种子文件（form 字段名 file）：" + err.Error()})
			return
		}
		f, err := fileHeader.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "打开种子文件失败：" + err.Error()})
			return
		}
		defer func() { _ = f.Close() }()
		data, err := io.ReadAll(f)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "读取种子文件失败：" + err.Error()})
			return
		}
		res, err := svc.CloudDownload.SubmitTorrent(c.Request.Context(), service.CloudDownloadTorrentRequest{
			AccountID:  c.PostForm("account_id"),
			SyncPathID: c.PostForm("sync_path_id"),
			FileName:   fileHeader.Filename,
			Data:       data,
		})
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

func cloudDownloadQuotaHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		quota, err := svc.CloudDownload.Quota(c.Request.Context(), c.Query("account_id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, quota)
	}
}

func retryCloudDownloadHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.CloudDownload.Retry(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func retryCloudDownloadPipelineHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.CloudDownload.RetryPipeline(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func cancelCloudDownloadHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.CloudDownload.Cancel(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func deleteCloudDownloadHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.CloudDownload.Delete(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func clearFinishedCloudDownloadsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		n, err := svc.CloudDownload.ClearFinished(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": n})
	}
}
