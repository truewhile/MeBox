// Package handler — HLS / image-proxy / scrape endpoints.
package handler

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

func hlsPlaylistHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		// 远程 Emby 挂载没有本地文件，不能转码。STRM 允许直连失败后走 HLS。
		if svc.EmbyRemote != nil && service.IsEmbyRemoteID(id) {
			c.JSON(http.StatusConflict, gin.H{"error": "transcode disabled"})
			return
		}
		m, err := svc.Media.GetMedia(c.Request.Context(), id)
		if err != nil || m == nil || !mediaVisibleForRequest(c, svc, m) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if !enforceScopedPlaybackToken(c, m.ID) {
			return
		}
		err = svc.Stream.ServeHLSPlaylist(c.Writer, c.Request, c.Param("id"))
		if errors.Is(err, service.ErrMediaNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if errors.Is(err, service.ErrTranscodeDisabled) {
			c.JSON(http.StatusConflict, gin.H{"error": "transcode disabled"})
			return
		}
		if errors.Is(err, service.ErrTranscodeBusy) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "transcode busy"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
}

func hlsSegmentHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if svc.EmbyRemote != nil && service.IsEmbyRemoteID(id) {
			c.JSON(http.StatusConflict, gin.H{"error": "transcode disabled"})
			return
		}
		m, err := svc.Media.GetMedia(c.Request.Context(), id)
		if err != nil || m == nil || !mediaVisibleForRequest(c, svc, m) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if !enforceScopedPlaybackToken(c, m.ID) {
			return
		}
		err = svc.Stream.ServeHLSSegment(c.Writer, c.Request, c.Param("id"), c.Param("seg"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
	}
}

func stopTranscodeHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc.Transcoder.StopJob(c.Param("id"))
		c.Status(http.StatusNoContent)
	}
}

func imageProxyHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.Query("url")
		if c.Query("refresh") != "" {
			_ = svc.ImageProxy.RemoveFailed(raw)
		} else if c.Query("retry") != "" {
			_ = svc.ImageProxy.RemoveFailed(raw)
		}
		// Serve handles upstream errors internally by returning a 1×1 PNG
		// placeholder, so the only error we can get back here is a malformed
		// URL. In that case we still return 400 to make the misuse visible.
		if err := svc.ImageProxy.Serve(c.Request.Context(), c.Writer, c.Request, raw); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
}

type scrapeRequest struct {
	EpisodeArtwork *bool `json:"episode_artwork"`
	EpisodeImages  *bool `json:"episode_images"`
	RefreshMatched *bool `json:"refresh_matched"`
	IncludeMatched *bool `json:"include_matched"`
}

func (r scrapeRequest) episodeArtworkOption() *bool {
	if r.EpisodeImages != nil {
		return r.EpisodeImages
	}
	return r.EpisodeArtwork
}

func (r scrapeRequest) includeMatchedOption() bool {
	if r.IncludeMatched != nil {
		return *r.IncludeMatched
	}
	if r.RefreshMatched != nil {
		return *r.RefreshMatched
	}
	return false
}

func scrapeOptionsFromRequest(c *gin.Context, retryNoMatch bool) (service.ScrapeOptions, error) {
	options := service.ScrapeOptions{RetryNoMatch: retryNoMatch}
	if c.Request.Body == nil || c.Request.ContentLength == 0 {
		return options, nil
	}
	var req scrapeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return options, nil
		}
		return options, err
	}
	options.EpisodeArtwork = req.episodeArtworkOption()
	options.IncludeMatched = req.includeMatchedOption()
	return options, nil
}

// scrapeOneHandler enriches a single media via the configured scraper chain.
func scrapeOneHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		options, err := scrapeOptionsFromRequest(c, true)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid scrape options"})
			return
		}
		options.IncludeMatched = true
		task, err := svc.Scraper.EnqueueMedia(c.Request.Context(), c.Param("id"), options)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, task)
	}
}

// scrapeLibraryHandler manually refreshes every scrapeable row in a library.
func scrapeLibraryHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		libID := c.Param("id")
		options, err := scrapeOptionsFromRequest(c, true)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid scrape options"})
			return
		}
		options.IncludeMatched = true
		n, err := svc.Scraper.EnqueueLibrary(c.Request.Context(), libID, options)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "queued", "enqueued": n})
	}
}

func startScrapeHTTPTask(svc *service.Container, name, title, path string) *service.TaskHandle {
	if svc == nil || svc.Tasks == nil {
		return nil
	}
	if title != "" {
		name += "：" + title
	}
	return svc.Tasks.Start(service.TaskKindScrape, name, service.TaskUpdate{
		Stage:      "scrape",
		SourcePath: path,
		Message:    "正在刮削元数据",
	})
}

// reprobeHandler re-runs ffprobe against a single media. Admin-only.
func reprobeHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Stream.Probe(c.Request.Context(), c.Param("id"), svc.FFprobe); err != nil {
			if errors.Is(err, service.ErrMediaNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			// ffprobe unavailable or file inaccessible — still 200 with error info
			c.JSON(http.StatusOK, gin.H{"code": 1, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok"})
	}
}
