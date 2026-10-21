package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

// libraryCoverHandler 输出媒体库封面图（Emby 风格的成员海报拼图）。
//
// 之前 Web 端是拿两张成员海报在前端用 CSS 拼格子，Emby 客户端则直接拿一张
// 评分最高的海报，结果同一个库在不同客户端长得完全不一样。现在服务端生成
// 一张 16:9 的拼图（Emby 的 folder image 做法），Emby 客户端、Web 和 App
// 都指向这一份，外观统一。
func libraryCoverHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.Param("id"))
		if id == "" || svc == nil || svc.ImageProxy == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cover unavailable"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
		defer cancel()

		sources, lib := service.LibraryCoverSourcesForLibrary(ctx, svc.Repo, id)
		if lib == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "library not found"})
			return
		}
		if strings.TrimSpace(lib.CoverURL) != "" {
			// 管理员自定义封面优先，保持既有行为（代理该地址）。
			if err := svc.ImageProxy.Serve(ctx, c.Writer, c.Request, lib.CoverURL); err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "custom cover unavailable"})
			}
			return
		}
		if len(sources) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "no library items with artwork"})
			return
		}
		width, height := libraryCoverRequestSize(c)
		data, err := svc.ImageProxy.LibraryCoverImage(ctx, id, sources, width, height)
		if err != nil || len(data) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "cover generation failed"})
			return
		}
		writeLibraryCoverImage(c, data, http.StatusOK)
	}
}

// libraryCoverRequestSize 解析请求的封面尺寸：Web 用 w/h，Emby 用
// maxWidth/maxHeight，两种都兼容。服务端统一输出 16:9 画布（与该库在 Emby
// 里上报的 PrimaryImageAspectRatio 一致），客户端传的尺寸只当作「不超过」的
// 边界使用。
func libraryCoverRequestSize(c *gin.Context) (int, int) {
	return firstPositiveQueryIntParam(c, "maxWidth", "maxwidth", "width", "w"),
		firstPositiveQueryIntParam(c, "maxHeight", "maxheight", "height", "h")
}

func firstPositiveQueryIntParam(c *gin.Context, names ...string) int {
	for _, name := range names {
		raw := strings.TrimSpace(c.Query(name))
		if raw == "" {
			continue
		}
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
		if f, err := strconv.ParseFloat(raw, 64); err == nil && f > 0 {
			return int(f)
		}
	}
	return 0
}

func writeLibraryCoverImage(c *gin.Context, data []byte, status int) {
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	c.Header("ETag", etag)
	c.Header("Cache-Control", "public, max-age=300, stale-while-revalidate=86400")
	if match := strings.TrimSpace(c.GetHeader("If-None-Match")); match != "" && match == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Header("Content-Type", "image/jpeg")
	c.Header("Content-Length", strconv.Itoa(len(data)))
	if c.Request.Method == http.MethodHead {
		c.Status(status)
		return
	}
	c.Data(status, "image/jpeg", data)
}
