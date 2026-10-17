package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/service"
)

// registerMobileRoutes 挂载 /api/v1 移动端稳定契约。
//
// 既有 /api 与 Emby 入口保持不变；v1 只做适配和会话语义补强，不复制媒体
// 或书源业务逻辑。
func registerMobileRoutes(api *gin.RouterGroup, svc *service.Container, log *zap.Logger) {
	if svc == nil || svc.ServerIdentity == nil || svc.AuthSessions == nil {
		// 最小容器（例如旧测试桩）没有身份/会话服务时，不暴露不可用的 v1。
		return
	}
	wireAuthSessionValidator(svc)

	v1 := api.Group("/v1")
	v1.Use(mobileRequestIDMiddleware())

	v1.GET("/server-info", mobileServerInfoHandler(svc, log))

	authLimiter := middleware.NewRateLimiter(30, time.Minute)
	v1.POST("/auth/login", mobileRateLimit(authLimiter), mobileLoginHandler(svc, log))
	v1.POST("/auth/refresh", mobileRefreshHandler(svc, log))

	authed := v1.Group("/auth")
	{
		authed.GET("/session", mobileAuthRequired(svc), mobileSessionHandler(svc))
		authed.POST("/logout", mobileLogoutAuthRequired(svc), mobileLogoutHandler(svc))
	}
}

// wireAuthSessionValidator 让旧 /api 中间件也能校验新令牌的 sid。
// service 层错误在这里映射为 middleware 的两个哨兵，避免包依赖反转。
func wireAuthSessionValidator(svc *service.Container) {
	middleware.SetSessionValidator(func(ctx context.Context, sessionID, userID string) error {
		if svc == nil || svc.AuthSessions == nil {
			return nil
		}
		_, err := svc.AuthSessions.Validate(ctx, sessionID, userID)
		if err == nil {
			return nil
		}
		if errors.Is(err, service.ErrAuthSessionRevoked) ||
			errors.Is(err, service.ErrAuthSessionExpired) ||
			errors.Is(err, service.ErrAuthSessionRequired) {
			return middleware.ErrSessionRevoked
		}
		return fmt.Errorf("%w: %v", middleware.ErrSessionUnavailable, err)
	})
}

func mobileServerInfoHandler(svc *service.Container, log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		info, err := svc.ServerIdentity.Info(ctx, service.CurrentServerCapabilities(svc))
		if err != nil {
			if log != nil {
				log.Warn("mobile server info failed", zap.Error(err))
			}
			writeMobileError(c, http.StatusServiceUnavailable, mobileCodeServiceUnavailable,
				"server identity temporarily unavailable", true, 2)
			return
		}
		// 身份信息随配置变化且不应被中间缓存：同一地址可能切换部署实例。
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, info)
	}
}
