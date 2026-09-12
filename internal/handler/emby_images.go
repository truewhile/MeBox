package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

var embyPlaceholderPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x50, 0xd1, 0x30, 0xf8,
	0x0f, 0x00, 0x02, 0x6c, 0x01, 0x7c, 0x30, 0xed,
	0x6e, 0x0a, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
	0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

// embyItemImageHandler 把 /Items/{id}/Images/Primary 等请求直接输出为图片。
// Emby 客户端缓存图片 URL 时经常不会继续携带 token；如果重定向到受保护的
// /api/img 会变成 401，所以这里复用 ImageProxy 但不再走 /api 路由。
func embyItemImageHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		embyServeImage(c, svc, c.Param("id"), c.Param("type"), c.Query("tag"), false)
	}
}

// embyPersonImageHandler 兼容 Emby 官方的 /Persons/{Name}/Images/{Type}。
// Name 可能是伪装后的远程人物 ID，也可能是电影详情 People 中的显示名称。
func embyPersonImageHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		embyServeImage(c, svc, c.Param("name"), c.Param("type"), c.Query("tag"), true)
	}
}

func embyServeImage(c *gin.Context, svc *service.Container, id, imageType, tag string, person bool) {
	clearEmbyImageNoStoreHeaders(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	req := c.Request.WithContext(ctx)
	if svc == nil || svc.Emby == nil {
		embyServePlaceholderImage(c)
		return
	}
	var raw string
	var err error
	if person {
		raw, err = svc.Emby.PersonImageURL(ctx, id, imageType, tag)
	} else {
		raw, err = svc.Emby.ImageURL(ctx, id, imageType)
	}
	if err != nil || raw == "" {
		embyServePlaceholderImage(c)
		return
	}
	if svc.ImageProxy == nil {
		embyServePlaceholderImage(c)
		return
	}
	if err := svc.ImageProxy.Serve(ctx, c.Writer, req, raw); err != nil {
		embyServePlaceholderImage(c)
	}
}

// embyItemImagesHandler 处理不带 Type 的 GET /Items/{Id}/Images，返回图片
// 清单（Emby 的 ImageInfo 数组）。客户端据此决定详情页加载哪些图。
func embyItemImagesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.Param("id"))
		if svc == nil || svc.Emby == nil || id == "" {
			c.JSON(http.StatusOK, []any{})
			return
		}
		infos := svc.Emby.ImageInfos(c.Request.Context(), id)
		if infos == nil {
			infos = []map[string]any{}
		}
		c.JSON(http.StatusOK, infos)
	}
}

// embyUserImageHandler 处理 /Users/{UserId}/Images/{Type}。Emby 对未设置
// 头像的用户同样返回 404，但响应必须带缓存头，否则客户端每次进入设置页
// 都会重复请求同一个空头像（日志中曾观察到每分钟重试）。
func embyUserImageHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := strings.TrimSpace(c.Param("userId"))
		raw := ""
		if svc != nil && svc.Emby != nil && uid != "" {
			raw = svc.Emby.UserAvatarURL(c.Request.Context(), uid)
		}
		if raw == "" || svc == nil || svc.ImageProxy == nil {
			embyMissingAvatar(c)
			return
		}
		if err := svc.ImageProxy.Serve(c.Request.Context(), c.Writer, c.Request, raw); err != nil {
			embyMissingAvatar(c)
		}
	}
}

// embyMissingAvatar 以 Emby 语义返回"该用户没有头像"，并允许客户端长期缓存。
func embyMissingAvatar(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=86400")
	c.Status(http.StatusNotFound)
}

func clearEmbyImageNoStoreHeaders(c *gin.Context) {
	c.Writer.Header().Del("Cache-Control")
	c.Writer.Header().Del("Pragma")
	c.Writer.Header().Del("Expires")
}

func embyServePlaceholderImage(c *gin.Context) {
	c.Header("Content-Type", "image/png")
	c.Header("Cache-Control", "public, max-age=86400")
	c.Header("Content-Length", strconv.Itoa(len(embyPlaceholderPNG)))
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}
	c.Data(http.StatusOK, "image/png", embyPlaceholderPNG)
}
