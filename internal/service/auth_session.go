// Package service — 设备认证会话。
package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// 认证会话错误。middleware 与 handler 依据这些错误区分「重新登录」和
// 「临时基础设施故障」。
var (
	ErrAuthSessionRequired = errors.New("auth session required")
	ErrAuthSessionRevoked  = errors.New("auth session revoked")
	ErrAuthSessionExpired  = errors.New("auth session expired")
)

// AuthSessionService 管理设备会话的创建、校验和撤销。
type AuthSessionService struct {
	log  *zap.Logger
	repo *repository.Container
	now  func() time.Time
}

// NewAuthSessionService 创建会话服务。
func NewAuthSessionService(log *zap.Logger, repo *repository.Container) *AuthSessionService {
	return &AuthSessionService{log: log, repo: repo, now: time.Now}
}

// CreateAuthSessionInput 是创建会话所需的最小设备信息。
type CreateAuthSessionInput struct {
	UserID        string
	DeviceID      string
	DeviceName    string
	ClientName    string
	ClientVersion string
}

// Create 创建一次登录对应的认证会话。
//
// 设备 ID 由 App 安装随机生成，不接收 IMEI/MAC 等硬件标识；这里只做长度
// 与字符白名单校验，避免任意字符串进入索引和日志。
func (s *AuthSessionService) Create(ctx context.Context, in CreateAuthSessionInput) (*model.AuthSession, error) {
	userID := strings.TrimSpace(in.UserID)
	deviceID := strings.TrimSpace(in.DeviceID)
	if userID == "" || deviceID == "" {
		return nil, errors.New("user id and device id required")
	}
	if len(deviceID) > 128 || !validDeviceID(deviceID) {
		return nil, errors.New("invalid device id")
	}
	now := s.currentTime()
	session := &model.AuthSession{
		UserID:        userID,
		DeviceID:      deviceID,
		DeviceName:    truncateRunes(strings.TrimSpace(in.DeviceName), 128),
		ClientName:    truncateRunes(strings.TrimSpace(in.ClientName), 64),
		ClientVersion: truncateRunes(strings.TrimSpace(in.ClientVersion), 64),
		LastUsedAt:    now,
		ExpiresAt:     now.Add(RefreshTokenDuration),
	}
	if err := s.repo.AuthSession.Create(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

// Validate 校验会话仍可用于给定用户。
//
// 未找到的会话按「已撤销」处理：App 收到 SESSION_REVOKED 后应重新登录，
// 而不是无限刷新。数据库错误保持原样返回，由上层映射成可重试的 503。
func (s *AuthSessionService) Validate(ctx context.Context, sessionID, userID string) (*model.AuthSession, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, ErrAuthSessionRequired
	}
	session, err := s.repo.AuthSession.FindByID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil || session.UserID != userID {
		return nil, ErrAuthSessionRevoked
	}
	if session.Revoked {
		return nil, ErrAuthSessionRevoked
	}
	if session.IsExpired() {
		return nil, ErrAuthSessionExpired
	}
	return session, nil
}

// Touch 更新会话最近使用时间，失败只记日志，不影响请求。
func (s *AuthSessionService) Touch(ctx context.Context, sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	if err := s.repo.AuthSession.TouchLastUsed(ctx, sessionID, s.currentTime()); err != nil && s.log != nil {
		s.log.Debug("touch auth session failed", zap.String("session_id", sessionID), zap.Error(err))
	}
}

// Revoke 撤销单个会话及其名下刷新令牌（幂等）。
func (s *AuthSessionService) Revoke(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if err := s.repo.AuthSession.Revoke(ctx, sessionID, s.currentTime()); err != nil {
		return err
	}
	return s.repo.RefreshToken.RevokeByAuthSessionID(ctx, sessionID)
}

// RevokeByUser 撤销某用户的全部会话（幂等）。
func (s *AuthSessionService) RevokeByUser(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	sessions, err := s.repo.AuthSession.ListActiveByUser(ctx, userID)
	if err != nil {
		return err
	}
	for i := range sessions {
		if err := s.Revoke(ctx, sessions[i].ID); err != nil {
			return err
		}
	}
	return nil
}

// RevokeDevice 撤销某用户某设备的全部会话（幂等）。
func (s *AuthSessionService) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	userID = strings.TrimSpace(userID)
	deviceID = strings.TrimSpace(deviceID)
	if userID == "" || deviceID == "" {
		return nil
	}
	sessions, err := s.repo.AuthSession.ListByDevice(ctx, userID, deviceID)
	if err != nil {
		return err
	}
	for i := range sessions {
		if sessions[i].Revoked {
			continue
		}
		if err := s.Revoke(ctx, sessions[i].ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *AuthSessionService) currentTime() time.Time {
	if s == nil || s.now == nil {
		return time.Now()
	}
	return s.now()
}

func validDeviceID(id string) bool {
	for _, r := range id {
		if r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' || r == ':') {
			return false
		}
	}
	return true
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
