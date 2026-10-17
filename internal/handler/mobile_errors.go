package handler

import (
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/truewhile/MeBox/internal/middleware"
)

// 移动端 v1 错误码。新增错误码必须同步 OpenAPI 与 App 的 ApiErrorMapper。
const (
	mobileCodeInvalidArgument       = "INVALID_ARGUMENT"
	mobileCodeInvalidCredentials    = "INVALID_CREDENTIALS"
	mobileCodeTokenExpired          = "TOKEN_EXPIRED"
	mobileCodeSessionRevoked        = "SESSION_REVOKED"
	mobileCodeAccountDisabled       = "ACCOUNT_DISABLED"
	mobileCodeAccountExpired        = "ACCOUNT_EXPIRED"
	mobileCodePermissionDenied      = "PERMISSION_DENIED"
	mobileCodeProfilePINRequired    = "PROFILE_PIN_REQUIRED"
	mobileCodeRateLimited           = "RATE_LIMITED"
	mobileCodeUpstreamUnavailable   = "UPSTREAM_UNAVAILABLE"
	mobileCodeTranscodeBusy         = "TRANSCODE_BUSY"
	mobileCodePlaybackSessionAbsent = "PLAYBACK_SESSION_NOT_FOUND"
	mobileCodeResourceExpired       = "RESOURCE_EXPIRED"
	mobileCodeProgressConflict      = "PROGRESS_CONFLICT"
	mobileCodeServiceUnavailable    = "SERVICE_UNAVAILABLE"
)

const (
	mobileCtxRequestID    = "mobile_request_id"
	mobileCtxAuthSession  = "mobile_auth_session"
	mobileRequestIDHeader = "X-Request-ID"
)

// mobileErrorBody 是 v1 统一错误响应。字段直接位于顶层，便于客户端按
// code 分流，不需要再判断后端历史包装层。
type mobileErrorBody struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	RequestID     string `json:"request_id"`
	Retryable     bool   `json:"retryable"`
	RetryAfterSec int    `json:"retry_after_sec,omitempty"`
	Details       any    `json:"details,omitempty"`
}

// mobileRequestIDMiddleware 为每个 v1 请求绑定可追踪的请求 ID。
// 合法客户端值会沿用（便于 App 与服务端日志对齐），非法或过长则由服务器
// 重新生成；该值仅用于诊断，绝不参与鉴权。
func mobileRequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader(mobileRequestIDHeader))
		if !validRequestID(id) {
			id = uuid.NewString()
		}
		c.Set(mobileCtxRequestID, id)
		c.Header(mobileRequestIDHeader, id)
		c.Next()
	}
}

func validRequestID(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if r > unicode.MaxASCII {
			return false
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		switch r {
		case '-', '_', '.', ':':
			continue
		default:
			return false
		}
	}
	return true
}

func currentMobileRequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(mobileCtxRequestID); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// writeMobileError 输出统一错误体。request_id 缺失时现场生成，保证所有
// v1 错误都可用于对账，而不是只在注册了中间件的路由上才有。
func writeMobileError(c *gin.Context, status int, code, message string, retryable bool, retryAfterSec int) {
	requestID := currentMobileRequestID(c)
	if requestID == "" {
		requestID = uuid.NewString()
		c.Header(mobileRequestIDHeader, requestID)
	}
	if retryAfterSec > 0 {
		c.Header("Retry-After", strconv.Itoa(retryAfterSec))
	}
	c.AbortWithStatusJSON(status, mobileErrorBody{
		Code:          code,
		Message:       message,
		RequestID:     requestID,
		Retryable:     retryable,
		RetryAfterSec: retryAfterSec,
	})
}

// mobileRateLimit 与旧端点共用 RateLimiter，但返回 v1 错误契约和 Retry-After。
func mobileRateLimit(limiter *middleware.RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !limiter.Allow(c.ClientIP()) {
			writeMobileError(c, http.StatusTooManyRequests, mobileCodeRateLimited,
				"too many requests, please try again later", true, 60)
			return
		}
		c.Next()
	}
}
