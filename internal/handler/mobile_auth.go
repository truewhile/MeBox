// Package handler — 移动端 /api/v1 认证端点。
package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

// mobileUserPayload 是 v1 返回的用户摘要。这里显式挑选字段，避免把仅服务端
// 使用的偏好和内部状态带进新契约。
type mobileUserPayload struct {
	ID                 string     `json:"id"`
	Username           string     `json:"username"`
	Nickname           string     `json:"nickname,omitempty"`
	Role               string     `json:"role"`
	Tier               string     `json:"tier"`
	IsActive           bool       `json:"is_active"`
	ForcePasswordReset bool       `json:"force_password_reset"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
}

func toMobileUserPayload(u *model.User) *mobileUserPayload {
	if u == nil {
		return nil
	}
	return &mobileUserPayload{
		ID:                 u.ID,
		Username:           u.Username,
		Nickname:           u.Nickname,
		Role:               u.Role,
		Tier:               u.Tier,
		IsActive:           u.IsActive,
		ForcePasswordReset: u.ForcePasswordReset,
		ExpiresAt:          u.ExpiredAt,
	}
}

type mobileSessionPayload struct {
	AuthSessionID string    `json:"auth_session_id"`
	DeviceID      string    `json:"device_id"`
	DeviceName    string    `json:"device_name,omitempty"`
	ClientName    string    `json:"client_name,omitempty"`
	ClientVersion string    `json:"client_version,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	LastUsedAt    time.Time `json:"last_used_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func toMobileSessionPayload(s *model.AuthSession) *mobileSessionPayload {
	if s == nil {
		return nil
	}
	return &mobileSessionPayload{
		AuthSessionID: s.ID,
		DeviceID:      s.DeviceID,
		DeviceName:    s.DeviceName,
		ClientName:    s.ClientName,
		ClientVersion: s.ClientVersion,
		CreatedAt:     s.CreatedAt,
		LastUsedAt:    s.LastUsedAt,
		ExpiresAt:     s.ExpiresAt,
	}
}

type mobileLoginRequest struct {
	Username      string `json:"username" binding:"required"`
	Password      string `json:"password" binding:"required"`
	DeviceID      string `json:"device_id" binding:"required"`
	DeviceName    string `json:"device_name"`
	ClientName    string `json:"client_name"`
	ClientVersion string `json:"client_version"`
}

type mobileRefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// mobileAuthRequired 是 v1 专用认证中间件：要求有效 JWT + 有效设备会话，
// 并统一输出 v1 错误契约。
func mobileAuthRequired(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := middleware.ExtractToken(c)
		if raw == "" {
			writeMobileError(c, http.StatusUnauthorized, mobileCodeInvalidCredentials,
				"access token required", false, 0)
			return
		}
		claims, err := svc.Token.ValidateAccessToken(raw)
		if err != nil {
			if errors.Is(err, jwt.ErrTokenExpired) {
				writeMobileError(c, http.StatusUnauthorized, mobileCodeTokenExpired,
					"access token expired", false, 0)
				return
			}
			writeMobileError(c, http.StatusUnauthorized, mobileCodeInvalidCredentials,
				"invalid access token", false, 0)
			return
		}
		if strings.TrimSpace(claims.Purpose) != "" {
			writeMobileError(c, http.StatusForbidden, mobileCodePermissionDenied,
				"token scope denied for this endpoint", false, 0)
			return
		}
		sessionID := strings.TrimSpace(claims.Sid)
		if sessionID == "" {
			// v1 客户端必须使用登录时创建的会话；旧网页令牌不进入移动端契约。
			writeMobileError(c, http.StatusUnauthorized, mobileCodeInvalidCredentials,
				"auth session required; sign in again", false, 0)
			return
		}
		session, err := svc.AuthSessions.Validate(c.Request.Context(), sessionID, claims.UserID)
		if err != nil {
			if service.IsTransientDatabaseLock(err) {
				writeMobileError(c, http.StatusServiceUnavailable, mobileCodeServiceUnavailable,
					"auth session check temporarily unavailable", true, 2)
				return
			}
			writeMobileError(c, http.StatusUnauthorized, mobileCodeSessionRevoked,
				"auth session revoked or expired", false, 0)
			return
		}
		c.Set(middleware.CtxUserID, claims.UserID)
		c.Set(middleware.CtxUserRole, claims.Role)
		c.Set(middleware.CtxUserTier, claims.Tier)
		c.Set(middleware.CtxAuthSessionID, sessionID)
		c.Set(mobileCtxAuthSession, session)
		// 每个请求写一次 last_used_at 会在 SQLite 上造成不必要的写锁竞争；
		// 一分钟内的重复请求不更新，足以支撑设备列表的“最近活动”。
		if time.Since(session.LastUsedAt) > time.Minute {
			svc.AuthSessions.Touch(c.Request.Context(), sessionID)
		}
		c.Next()
	}
}

// mobileLogoutAuthRequired 只校验 JWT 与 sid，不要求会话仍然有效。
// 退出必须幂等：同一个令牌重复调用 logout 也要返回成功，而不是在第二次
// 被会话校验拦成 401。
func mobileLogoutAuthRequired(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := middleware.ExtractToken(c)
		if raw == "" {
			writeMobileError(c, http.StatusUnauthorized, mobileCodeInvalidCredentials,
				"access token required", false, 0)
			return
		}
		claims, err := svc.Token.ValidateAccessToken(raw)
		if err != nil {
			if errors.Is(err, jwt.ErrTokenExpired) {
				writeMobileError(c, http.StatusUnauthorized, mobileCodeTokenExpired,
					"access token expired", false, 0)
				return
			}
			writeMobileError(c, http.StatusUnauthorized, mobileCodeInvalidCredentials,
				"invalid access token", false, 0)
			return
		}
		sessionID := strings.TrimSpace(claims.Sid)
		if sessionID == "" {
			writeMobileError(c, http.StatusUnauthorized, mobileCodeInvalidCredentials,
				"auth session required; sign in again", false, 0)
			return
		}
		c.Set(middleware.CtxUserID, claims.UserID)
		c.Set(middleware.CtxAuthSessionID, sessionID)
		c.Next()
	}
}

func mobileLoginHandler(svc *service.Container, log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req mobileLoginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeMobileError(c, http.StatusBadRequest, mobileCodeInvalidArgument,
				"username, password and device_id are required", false, 0)
			return
		}
		req.Username = strings.TrimSpace(req.Username)
		req.DeviceID = strings.TrimSpace(req.DeviceID)
		if req.Username == "" || req.DeviceID == "" || len(req.DeviceID) > 128 {
			writeMobileError(c, http.StatusBadRequest, mobileCodeInvalidArgument,
				"invalid username or device_id", false, 0)
			return
		}
		ctx := c.Request.Context()
		user, err := svc.Auth.Authenticate(ctx, req.Username, req.Password)
		if err != nil {
			writeMobileAuthError(c, log, err)
			return
		}

		deviceName := strings.TrimSpace(req.DeviceName)
		clientName := strings.TrimSpace(req.ClientName)
		if clientName == "" {
			clientName = "MeBox Android"
		}
		session, err := svc.AuthSessions.Create(ctx, service.CreateAuthSessionInput{
			UserID:        user.ID,
			DeviceID:      req.DeviceID,
			DeviceName:    deviceName,
			ClientName:    clientName,
			ClientVersion: req.ClientVersion,
		})
		if err != nil {
			if isTransientServiceError(err) {
				writeMobileError(c, http.StatusServiceUnavailable, mobileCodeServiceUnavailable,
					"cannot create auth session right now", true, 2)
				return
			}
			writeMobileError(c, http.StatusBadRequest, mobileCodeInvalidArgument,
				"invalid device information", false, 0)
			return
		}

		tokens, err := svc.Token.IssuePairForSession(ctx, user.ID, user.Role, user.Tier, session.ID)
		if err != nil {
			// 令牌签发失败时回收会话，避免留下永远无法使用的会话记录。
			_ = svc.AuthSessions.Revoke(ctx, session.ID)
			writeMobileAuthError(c, log, err)
			return
		}
		if svc.Device != nil {
			svc.Device.RecordLogin(ctx, user.ID, session.DeviceID, deviceName, clientName, c.ClientIP())
		}
		if svc.Audit != nil {
			svc.Audit.RecordBestEffort(user.ID, "auth.mobile.login", user.Username, c.ClientIP(), "")
		}
		c.JSON(http.StatusOK, gin.H{
			"auth_session_id": session.ID,
			"user":            toMobileUserPayload(user),
			"tokens":          tokens,
		})
	}
}

func mobileRefreshHandler(svc *service.Container, log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req mobileRefreshRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeMobileError(c, http.StatusBadRequest, mobileCodeInvalidArgument,
				"refresh_token required", false, 0)
			return
		}
		ctx := c.Request.Context()
		rt, err := svc.Repo.RefreshToken.FindByHash(ctx, repository.HashToken(req.RefreshToken))
		if err != nil {
			if isTransientServiceError(err) {
				writeMobileError(c, http.StatusServiceUnavailable, mobileCodeServiceUnavailable,
					"token store temporarily unavailable", true, 2)
				return
			}
			writeMobileAuthError(c, log, err)
			return
		}
		// 只有 v1 登录签发的刷新令牌才带会话；旧网页令牌必须重新登录。
		// 注意这里不能在 rt.Revoked 时提前返回：轮换宽限期内的幂等复用
		// 由 TokenService 判定。
		if rt == nil || strings.TrimSpace(rt.AuthSessionID) == "" {
			writeMobileError(c, http.StatusUnauthorized, mobileCodeInvalidCredentials,
				"refresh token is not bound to a device session; sign in again", false, 0)
			return
		}
		if _, err := svc.AuthSessions.Validate(ctx, rt.AuthSessionID, rt.UserID); err != nil {
			if isTransientServiceError(err) {
				writeMobileError(c, http.StatusServiceUnavailable, mobileCodeServiceUnavailable,
					"auth session check temporarily unavailable", true, 2)
				return
			}
			writeMobileError(c, http.StatusUnauthorized, mobileCodeSessionRevoked,
				"auth session revoked or expired", false, 0)
			return
		}

		tokens, err := svc.Auth.RefreshTokens(ctx, req.RefreshToken)
		if err != nil {
			writeMobileAuthError(c, log, err)
			return
		}
		user, err := svc.Repo.User.FindByID(ctx, rt.UserID)
		if err != nil {
			writeMobileAuthError(c, log, err)
			return
		}
		if user == nil {
			writeMobileError(c, http.StatusUnauthorized, mobileCodeInvalidCredentials,
				"user not found", false, 0)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"auth_session_id": rt.AuthSessionID,
			"user":            toMobileUserPayload(user),
			"tokens":          tokens,
		})
	}
}

func mobileSessionHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		userID := c.GetString(middleware.CtxUserID)
		user, err := svc.Repo.User.FindByID(ctx, userID)
		if err != nil {
			writeMobileAuthError(c, svc.Log, err)
			return
		}
		if user == nil {
			writeMobileError(c, http.StatusUnauthorized, mobileCodeInvalidCredentials,
				"user not found", false, 0)
			return
		}
		session, _ := c.Get(mobileCtxAuthSession)
		sessionModel, _ := session.(*model.AuthSession)
		c.JSON(http.StatusOK, gin.H{
			"auth_session_id": c.GetString(middleware.CtxAuthSessionID),
			"user":            toMobileUserPayload(user),
			"session":         toMobileSessionPayload(sessionModel),
		})
	}
}

func mobileLogoutHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		sessionID := c.GetString(middleware.CtxAuthSessionID)
		if err := svc.AuthSessions.Revoke(c.Request.Context(), sessionID); err != nil {
			if isTransientServiceError(err) {
				writeMobileError(c, http.StatusServiceUnavailable, mobileCodeServiceUnavailable,
					"cannot revoke session right now", true, 2)
				return
			}
			writeMobileError(c, http.StatusInternalServerError, mobileCodeServiceUnavailable,
				"logout failed", false, 0)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

// writeMobileAuthError 把 service 层错误映射为稳定错误码。
// 临时性存储/基础设施错误必须返回可重试的 503，不能伪装成凭证失效。
func writeMobileAuthError(c *gin.Context, log *zap.Logger, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidCredentials), errors.Is(err, service.ErrInvalidRefreshToken):
		writeMobileError(c, http.StatusUnauthorized, mobileCodeInvalidCredentials,
			"invalid username, password or refresh token", false, 0)
	case errors.Is(err, service.ErrTokenExpired):
		writeMobileError(c, http.StatusUnauthorized, mobileCodeTokenExpired,
			"refresh token expired", false, 0)
	case errors.Is(err, service.ErrTokenRevoked),
		errors.Is(err, service.ErrAuthSessionRevoked),
		errors.Is(err, service.ErrAuthSessionExpired),
		errors.Is(err, service.ErrAuthSessionRequired):
		writeMobileError(c, http.StatusUnauthorized, mobileCodeSessionRevoked,
			"auth session revoked or expired", false, 0)
	case errors.Is(err, service.ErrUserInactive):
		writeMobileError(c, http.StatusForbidden, mobileCodeAccountDisabled,
			"account is disabled", false, 0)
	case errors.Is(err, service.ErrUserExpired):
		writeMobileError(c, http.StatusForbidden, mobileCodeAccountExpired,
			"account has expired", false, 0)
	case isTransientServiceError(err):
		writeMobileError(c, http.StatusServiceUnavailable, mobileCodeServiceUnavailable,
			"service temporarily unavailable, please retry", true, 2)
	default:
		if log != nil {
			log.Warn("mobile auth request failed", zap.Error(err))
		}
		writeMobileError(c, http.StatusServiceUnavailable, mobileCodeServiceUnavailable,
			"service temporarily unavailable, please retry", true, 2)
	}
}

func isTransientServiceError(err error) bool {
	if err == nil {
		return false
	}
	if service.IsTransientDatabaseLock(err) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	return false
}
