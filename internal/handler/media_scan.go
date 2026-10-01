package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/service"
)

func scanLibraryHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		lib, err := svc.Repo.Library.FindByID(c.Request.Context(), id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if lib == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "library not found"})
			return
		}
		if _, ok := service.ParseCloudLibraryMount(lib.Path); ok {
			// 云盘扫描已随网盘后端移除；此处保留空分支以兼容既有客户端。
			c.JSON(http.StatusBadRequest, gin.H{"error": "cloud 网盘后端已移除，无法扫描云盘媒体库"})
			return
		}
		finishScan, ok := svc.Scan.TryBeginLocalScan(id)
		if !ok {
			c.JSON(http.StatusAccepted, gin.H{
				"library_id":       id,
				"queued":           true,
				"already_running":  true,
				"message":          "该媒体库正在后台扫描，请在任务面板查看进度",
				"estimate_message": "页面关闭不会中断扫描",
			})
			return
		}
		task := startScanHTTPTask(svc, "手动扫描入库", lib.Name, lib.Path)
		go func(libraryID string, task *service.TaskHandle, finish func()) {
			defer finish()
			var res *service.ScanResult
			var err error
			helper.Run(svc.Log, "scan.library", func() {
				res, err = svc.Scan.ScanLibrary(context.Background(), libraryID)
			})
			if err != nil {
				finishHTTPTask(task, err, "scan", "手动扫描入库失败", scanTaskMetrics(res), scanTaskDetails(res, 20))
				return
			}
			finishHTTPTask(task, nil, "completed", "手动扫描入库结束", scanTaskMetrics(res), scanTaskDetails(res, 20))
		}(id, task, finishScan)
		c.JSON(http.StatusAccepted, gin.H{
			"library_id":       id,
			"queued":           true,
			"message":          "本地媒体库扫描已在后台运行，页面关闭不会中断",
			"estimate_message": "可在右上角任务面板查看扫描进度",
		})
	}
}

func scanLibraryRootHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		rootID := c.Param("root_id")
		finishScan, ok := svc.Scan.TryBeginLocalScan(id + ":" + rootID)
		if !ok {
			c.JSON(http.StatusAccepted, gin.H{
				"library_id":       id,
				"queued":           true,
				"already_running":  true,
				"message":          "该路径正在后台扫描，请在任务面板查看进度",
				"estimate_message": "页面关闭不会中断扫描",
			})
			return
		}
		task := startScanHTTPTask(svc, "手动扫描媒体库路径", id, rootID)
		go func(libraryID, libraryRootID string, task *service.TaskHandle, finish func()) {
			defer finish()
			var res *service.ScanResult
			var err error
			helper.Run(svc.Log, "scan.libraryRoot", func() {
				res, err = svc.Scan.ScanLibraryRoot(context.Background(), libraryID, libraryRootID)
			})
			if err != nil {
				finishHTTPTask(task, err, "scan", "手动扫描路径失败", scanTaskMetrics(res), scanTaskDetails(res, 20))
				return
			}
			finishHTTPTask(task, nil, "completed", "手动扫描路径结束", scanTaskMetrics(res), scanTaskDetails(res, 20))
		}(id, rootID, task, finishScan)
		c.JSON(http.StatusAccepted, gin.H{
			"library_id":       id,
			"queued":           true,
			"message":          "媒体库路径扫描已在后台运行，页面关闭不会中断",
			"estimate_message": "可在右上角任务面板查看扫描进度",
		})
	}
}

// queueLibraryRootScan starts the same background scan used by the manual
// scan endpoints. Library creation/root addition used to refresh only the
// watcher, which left a newly-created library empty until the user clicked
// "扫描" manually. Keeping this helper in the scan handler makes all mutation
// paths use one scan lifecycle and preserves the local-scan de-duplication.
func queueLibraryRootScan(svc *service.Container, libraryID, rootID string) {
	if svc == nil || svc.Scan == nil || strings.TrimSpace(libraryID) == "" {
		return
	}
	key := libraryID
	if strings.TrimSpace(rootID) != "" {
		key += ":" + rootID
	}
	finish, ok := svc.Scan.TryBeginLocalScan(key)
	if !ok {
		return
	}
	go func() {
		defer finish()
		helper.Run(svc.Log, "scan.queuedRoot", func() {
			if strings.TrimSpace(rootID) == "" {
				_, _ = svc.Scan.ScanLibrary(context.Background(), libraryID)
				return
			}
			_, _ = svc.Scan.ScanLibraryRoot(context.Background(), libraryID, rootID)
		})
	}()
}

func startScanHTTPTask(svc *service.Container, name, libraryName, path string) *service.TaskHandle {
	if svc == nil || svc.Tasks == nil {
		return nil
	}
	if libraryName != "" {
		name += "：" + libraryName
	}
	return svc.Tasks.Start(service.TaskKindScan, name, service.TaskUpdate{
		Stage:      "scan",
		SourcePath: path,
		Message:    "正在扫描并入库",
	})
}

func scanTaskMetrics(res *service.ScanResult) map[string]int64 {
	if res == nil {
		return nil
	}
	return map[string]int64{
		"visited":        int64(res.Visited),
		"added":          int64(res.Added),
		"updated":        int64(res.Updated),
		"skipped":        int64(res.Skipped),
		"probed":         int64(res.Probed),
		"local_metadata": int64(res.LocalMetadata),
		"removed":        res.Removed,
		"errors":         int64(res.ErrorCount),
	}
}

func scanTaskDetails(res *service.ScanResult, limit int) []string {
	if res == nil || limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	for _, line := range res.Errors {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, "错误: "+line)
		if len(out) >= limit {
			return out
		}
	}
	return out
}
