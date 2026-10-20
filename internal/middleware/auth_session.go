package middleware

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// 会话校验错误。service 层把自己的错误映射到这两个哨兵，避免 middleware
// 依赖 service 包（auth 服务已经依赖 middleware 的上下文键）。
var (
	// ErrSessionRevoked 表示会话已撤销、已过期或不存在：客户端应重新登录。
	ErrSessionRevoked = errors.New("auth session revoked")
	// ErrSessionUnavailable 表示暂时无法完成会话校验：客户端应稍后重试。
	ErrSessionUnavailable = errors.New("auth session unavailable")
)

// SessionValidator 校验认证会话是否仍属于该用户。
type SessionValidator func(ctx context.Context, sessionID, userID string) error

var (
	sessionValidatorMu sync.RWMutex
	sessionValidator   SessionValidator
)

// SetSessionValidator 注册全局会话校验器；传 nil 表示关闭校验（旧装配路径）。
func SetSessionValidator(fn SessionValidator) {
	sessionValidatorMu.Lock()
	defer sessionValidatorMu.Unlock()
	sessionValidator = fn
}

// ValidateAuthSession 使用已注册的校验器检查 sid。未注册校验器时放行，
// 以保持旧测试和嵌入式调用的兼容行为。
func ValidateAuthSession(ctx context.Context, sessionID, userID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	sessionValidatorMu.RLock()
	fn := sessionValidator
	sessionValidatorMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, sessionID, userID)
}
