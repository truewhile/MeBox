// Package handler — database backup/restore endpoints.
package handler

import (
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

func createBackupHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		info, err := svc.Backup.Create(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, info)
	}
}

func listBackupsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		items, err := svc.Backup.List()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	}
}

func deleteBackupHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Backup.Delete(c.Query("filename")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func downloadBackupHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		filename := c.Query("filename")
		path, err := svc.Backup.ResolvePath(filename)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.FileAttachment(path, filepath.Base(path))
	}
}

func restoreBackupHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Backup.Restore(c.Request.Context(), c.Query("filename")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		message := "数据库已恢复，请重启 MeBox 以刷新运行时配置"
		if svc.Repo.DB.Dialector.Name() == "sqlite" {
			message = "备份已校验并暂存，请重启 MeBox 后应用恢复"
		}
		c.JSON(http.StatusOK, gin.H{"message": message, "restart_required": true})
	}
}
