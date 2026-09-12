// Package handler wires the HTTP routes to the service container.
//
// All routes are mounted under /api/* so the frontend dev-server can proxy a
// single prefix.
package handler

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/service"
)

// Register attaches every API route to the engine.
func Register(r *gin.Engine, cfg *config.Config, log *zap.Logger, svc *service.Container) {
	api := r.Group("/api")
	api.Use(middleware.GzipAPI())
	{
		api.GET("/health", healthCheck)
		api.GET("/version", versionInfo)
		api.GET("/public/ui-config", publicUIConfigHandler(svc))

		// STRM 播放端点：strm 文件内容指向这里，Emby/Infuse 直接请求（无 JWT）。
		api.GET("/strm/play/:provider/:file", strmPlayHandler(svc))
		api.HEAD("/strm/play/:provider/:file", strmPlayHandler(svc))
		// 115 中继/CloudDrive 授权回跳（authorization_id 会话 + 共享密钥校验）
		api.POST("/strm/oauth/callback", strm115OAuthCallbackHandler(svc))
		api.GET("/strm/oauth/callback", strm115OAuthCallbackHandler(svc))

		registerPublicAuthRoutes(api, svc, log)

		registerAuthenticatedRoutes(api, cfg, svc)

		registerAdminRoutes(api, cfg, svc)

		registerAPIConfigRoutes(api, cfg, svc)

		// Emby/Jellyfin compatibility shim — routes mounted at /emby/* AND
		// the root path so Infuse / Yamby / Hills / Senplayer 都能自动连接。
	}

	registerEmbyRoutes(r, cfg.Secrets.JWTSecret, svc)
}

func healthCheck(c *gin.Context) {
	c.JSON(200, gin.H{"status": "ok"})
}

func versionInfo(c *gin.Context) {
	c.JSON(200, gin.H{"name": "MeBox", "version": "0.1.0"})
}

// ─── 权限 Handler 包装 ────────────────────────────────────────────────────────

func getMyPermissionsHandler(svc *service.Container) gin.HandlerFunc {
	h := NewPermissionHandler(svc, svc.Log)
	return h.GetMyPermissions
}

// ─── API Config Handler 包装 ───────────────────────────────────────────────────

func listApiConfigsHandler(svc *service.Container) gin.HandlerFunc {
	h := NewApiConfigHandler(svc, svc.Log)
	return h.ListApiConfigs
}

func listProvidersHandler(svc *service.Container) gin.HandlerFunc {
	h := NewApiConfigHandler(svc, svc.Log)
	return h.ListProviders
}

func getApiConfigHandler(svc *service.Container) gin.HandlerFunc {
	h := NewApiConfigHandler(svc, svc.Log)
	return h.GetApiConfig
}

func getEffectiveConfigHandler(svc *service.Container) gin.HandlerFunc {
	h := NewApiConfigHandler(svc, svc.Log)
	return h.GetEffectiveConfig
}

func upsertApiConfigHandler(svc *service.Container) gin.HandlerFunc {
	h := NewApiConfigHandler(svc, svc.Log)
	return h.UpsertApiConfig
}

func deleteApiConfigHandler(svc *service.Container) gin.HandlerFunc {
	h := NewApiConfigHandler(svc, svc.Log)
	return h.DeleteApiConfig
}

func testApiConfigHandler(svc *service.Container) gin.HandlerFunc {
	h := NewApiConfigHandler(svc, svc.Log)
	return h.TestApiConfig
}

// ─── SSE Handler ──────────────────────────────────────────────────────────────

func sseHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 获取 SSE Hub
		hub := svc.SSEHub

		// 设置 SSE 响应头
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")

		// 订阅事件流
		client := hub.Subscribe()
		defer hub.Unsubscribe(client)

		// 发送初始连接成功事件
		c.SSEvent("connected", gin.H{"status": "ok"})
		c.Writer.Flush()

		// 持续发送事件直到客户端断开连接
		clientGone := c.Request.Context().Done()
		for {
			select {
			case <-clientGone:
				return
			case event, ok := <-client.Ch:
				if !ok {
					return
				}
				c.SSEvent(event.Type, event.Payload)
				c.Writer.Flush()
			}
		}
	}
}
