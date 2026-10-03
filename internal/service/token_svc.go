// Package service — 双令牌认证服务。
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

const (
	// AccessTokenDuration Access Token 有效期（60分钟）
	AccessTokenDuration = 60 * time.Minute
	// RefreshTokenDuration Refresh Token 有效期（30天）
	RefreshTokenDuration = 30 * 24 * time.Hour
	// RefreshTokenLength Refresh Token 随机字节长度
	RefreshTokenLength = 32
)

// Claims 是 JWT 载荷（复制自 middleware 以避免循环导入）。
type Claims struct {
	UserID  string `json:"uid"`
	Role    string `json:"role"`
	Tier    string `json:"tier,omitempty"`
	Purpose string `json:"purpose,omitempty"`
	MediaID string `json:"media_id,omitempty"`
	jwt.RegisteredClaims
}

// TokenService 处理双令牌认证（Access Token + Refresh Token）。
type TokenService struct {
	cfg            *config.Config
	log            *zap.Logger
	repo           *repository.Container
	delayedStoreMu sync.Mutex
	// delayedStores 记录「已发给客户端但还没写进库」的 refresh token。
	// 键是 token 哈希；值携带签发信息，让 Refresh 在落库完成前也能识别
	// 这些令牌——否则用户登录成功、一小时后 access token 过期，刷新时
	// 因为 refresh token 从未落库而被判定无效，被强制踢回登录页，
	// 表现就是「经常登录报错」。
	delayedStores map[string]pendingRefreshToken

	// now 是可替换的时间源（测试用）；为 nil 时退回 time.Now。
	now func() time.Time

	// rotateMu 保护 refreshFlights / rotations。
	rotateMu sync.Mutex
	// refreshFlights 记录「正在进行的刷新」：同一个 refresh token 被并发
	// 提交时，只有第一个请求去轮换，其余等待并共享同一个结果。
	refreshFlights map[string]*refreshFlight
	// rotations 记录刚轮换过的 refresh token 及其新令牌对，供宽限期内
	// 幂等复用（见 token_refresh_rotation.go）。
	rotations map[string]rotatedRefreshToken
}

// NewTokenService 创建令牌服务实例。
func NewTokenService(cfg *config.Config, log *zap.Logger, repo *repository.Container) *TokenService {
	return &TokenService{
		cfg:            cfg,
		log:            log,
		repo:           repo,
		delayedStores:  make(map[string]pendingRefreshToken),
		now:            time.Now,
		refreshFlights: make(map[string]*refreshFlight),
		rotations:      make(map[string]rotatedRefreshToken),
	}
}

// TokenPair 包含访问令牌和刷新令牌。
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // 秒
	TokenType    string `json:"token_type"`
}

// TokenService 错误定义。
var (
	ErrInvalidRefreshToken = errors.New("invalid refresh token")
	ErrTokenExpired        = errors.New("token expired")
	ErrTokenRevoked        = errors.New("token revoked")
)

// IssuePair 为用户签发新的令牌对。
func (s *TokenService) IssuePair(ctx context.Context, userID, role, tier string) (*TokenPair, error) {
	return s.issuePair(ctx, userID, role, tier, false)
}

// IssuePairBestEffort 为登录签发令牌。SQLite 被后台扫描长期写锁占用时，
// 登录不能因为 refresh token 暂时无法落库而失败：先返回可用 access token，
// 再在后台把 refresh token 补写进库。
func (s *TokenService) IssuePairBestEffort(ctx context.Context, userID, role, tier string) (*TokenPair, error) {
	return s.issuePair(ctx, userID, role, tier, true)
}

func (s *TokenService) issuePair(ctx context.Context, userID, role, tier string, bestEffort bool) (*TokenPair, error) {
	// 生成 Access Token
	accessToken, err := s.issueAccessToken(userID, role, tier)
	if err != nil {
		return nil, err
	}

	// 生成 Refresh Token
	refreshToken, err := s.generateRefreshToken()
	if err != nil {
		return nil, err
	}

	// 存储 Refresh Token 哈希
	tokenHash := repository.HashToken(refreshToken)
	rt := &model.RefreshToken{
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(RefreshTokenDuration),
	}
	if bestEffort {
		s.storeRefreshTokenBestEffort(userID, tokenHash, rt.ExpiresAt)
		return &TokenPair{
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			ExpiresIn:    int64(AccessTokenDuration.Seconds()),
			TokenType:    "Bearer",
		}, nil
	}
	if err := s.storeRefreshToken(ctx, rt); err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(AccessTokenDuration.Seconds()),
		TokenType:    "Bearer",
	}, nil
}

func (s *TokenService) storeRefreshToken(ctx context.Context, rt *model.RefreshToken) error {
	if err := s.repo.RefreshToken.Create(ctx, rt); err != nil {
		return err
	}
	if err := s.repo.RefreshToken.RevokeOldestActiveByUserID(ctx, rt.UserID, s.maxActiveRefreshTokens(ctx)); err != nil && s.log != nil {
		s.log.Warn("failed to enforce refresh token session limit", zap.String("user_id", rt.UserID), zap.Error(err))
	}
	return nil
}

func (s *TokenService) maxActiveRefreshTokens(ctx context.Context) int {
	cfg := loadBotConfig(ctx, s.repo)
	if cfg.MaxLoggedClients < 1 {
		return defaultBotConfig().MaxLoggedClients
	}
	return cfg.MaxLoggedClients
}

// issueAccessToken 签发 JWT Access Token（HS256，60分钟有效期）。
func (s *TokenService) issueAccessToken(userID, role, tier string) (string, error) {
	claims := Claims{
		UserID: userID,
		Role:   role,
		Tier:   tier,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(AccessTokenDuration)),
			Issuer:    "mebox",
			Subject:   userID,
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(s.cfg.Secrets.JWTSecret))
}

// generateRefreshToken 生成安全的随机 Refresh Token。
func (s *TokenService) generateRefreshToken() (string, error) {
	buf := make([]byte, RefreshTokenLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// Refresh 使用 Refresh Token 轮换获取新的令牌对。
//
// refresh token 是一次性凭证，但真实客户端会并发使用同一个令牌（同一标签
// 页的 WebSocket 重连与 401 拦截器、多个标签页、容器重启后同时刷新的多个
// 页面）。因此这里做两件事：
//  1. 同一个 token 的并发刷新共享同一次轮换（single-flight）；
//  2. 轮换后在宽限期内重复提交同一个 token，返回同一次轮换的令牌对。
//
// 二者共同保证「重复刷新不会把已经成功的会话打成 401 revoked」。
func (s *TokenService) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	tokenHash := repository.HashToken(refreshToken)

	flight, leader := s.startRefreshFlight(tokenHash)
	if !leader {
		// 已有一次刷新在途：等它的结果，不再拿同一个一次性凭证轮换第二次。
		return s.waitForRefreshFlight(flight)
	}

	pair, err := s.refreshOnce(ctx, tokenHash)
	s.finishRefreshFlight(tokenHash, pair, err)
	if err != nil {
		return nil, err
	}
	return pair, nil
}

func (s *TokenService) refreshOnce(ctx context.Context, tokenHash string) (*TokenPair, error) {
	// 查找 Refresh Token 记录
	rt, err := s.repo.RefreshToken.FindByHash(ctx, tokenHash)
	if err != nil {
		return nil, err
	}
	if rt == nil {
		// 登录高峰/扫描写压力下，refresh token 可能还在后台补写队列里
		// 没来得及落库。此时令牌对客户端而言是合法的，不能判无效。
		pending, ok := s.pendingDelayedStore(tokenHash)
		if !ok || !s.currentTime().Before(pending.ExpiresAt) {
			// 也可能是「刚轮换完但从未落库」的令牌（同上，行本身不存在），
			// 宽限期内同样幂等复用。
			if pair, reused := s.reusedRotation(tokenHash, ""); reused {
				return pair, nil
			}
			return nil, ErrInvalidRefreshToken
		}
		rt = &model.RefreshToken{
			UserID:    pending.UserID,
			TokenHash: tokenHash,
			ExpiresAt: pending.ExpiresAt,
		}
	}

	// 检查是否已撤销
	if rt.Revoked {
		// 刚被轮换过的 token 在宽限期内允许幂等复用，避免并发/重试的
		// 客户端拿到 revoked 401 后清空整个会话。
		if pair, ok := s.reusedRotation(tokenHash, rt.UserID); ok {
			return pair, nil
		}
		return nil, ErrTokenRevoked
	}

	// 检查是否过期
	if rt.IsExpired() {
		return nil, ErrTokenExpired
	}

	// 获取用户信息
	user, err := s.repo.User.FindByID(ctx, rt.UserID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrInvalidRefreshToken
	}
	if !user.IsActive {
		return nil, ErrUserInactive
	}
	if user.ExpiredAt != nil && s.currentTime().After(*user.ExpiredAt) {
		return nil, ErrUserExpired
	}

	// 撤销旧的 Refresh Token（包括可能仍在后台补写队列里的副本）。
	if err := s.repo.RefreshToken.Revoke(ctx, tokenHash); err != nil {
		s.log.Warn("failed to revoke old refresh token", zap.Error(err))
	}
	s.untrackDelayedStore(rt.UserID, tokenHash)

	// 签发新的令牌对
	pair, err := s.IssuePairBestEffort(ctx, user.ID, user.Role, user.Tier)
	if err != nil {
		return nil, err
	}
	// 必须在结束 flight 之前记住本次轮换：否则等待中的请求会在 flight 与
	// 复用表之间的空档里查不到记录，把并发刷新误判为 revoked。
	s.rememberRotation(tokenHash, user.ID, pair)
	return pair, nil
}

// RevokeAll 撤销用户的所有 Refresh Token（用于登出）。
func (s *TokenService) RevokeAll(ctx context.Context, userID string) error {
	// 复用条目必须一起丢弃：否则登出/被踢下线后的宽限期内，
	// 旧令牌仍能换回一对有效令牌。
	s.forgetRotationsForUser(userID)
	return s.repo.RefreshToken.RevokeByUserID(ctx, userID)
}

// ValidateAccessToken 验证 Access Token 并返回 Claims。
func (s *TokenService) ValidateAccessToken(tokenString string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(s.cfg.Secrets.JWTSecret), nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// CleanupExpired 清理过期的 Refresh Token。
func (s *TokenService) CleanupExpired(ctx context.Context) error {
	return s.repo.RefreshToken.DeleteExpired(ctx)
}
