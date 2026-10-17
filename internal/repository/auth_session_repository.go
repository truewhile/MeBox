package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// AuthSessionRepository persists model.AuthSession records.
type AuthSessionRepository struct{ db *gorm.DB }

// Create inserts a new authentication session.
func (r *AuthSessionRepository) Create(ctx context.Context, s *model.AuthSession) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Create(s).Error
	})
}

// FindByID returns the session or (nil, nil) when absent.
func (r *AuthSessionRepository) FindByID(ctx context.Context, id string) (*model.AuthSession, error) {
	if id == "" {
		return nil, nil
	}
	var s model.AuthSession
	err := withSQLiteBusyRetry(ctx, func() error {
		s = model.AuthSession{}
		return r.db.WithContext(ctx).Where("id = ?", id).First(&s).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// TouchLastUsed 更新会话最近使用时间。
func (r *AuthSessionRepository) TouchLastUsed(ctx context.Context, id string, at time.Time) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.AuthSession{}).
			Where("id = ? AND revoked = ?", id, false).
			Update("last_used_at", at).Error
	})
}

// Revoke 撤销单个会话（幂等）。
func (r *AuthSessionRepository) Revoke(ctx context.Context, id string, at time.Time) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.AuthSession{}).
			Where("id = ? AND revoked = ?", id, false).
			Updates(map[string]any{"revoked": true, "revoked_at": at}).Error
	})
}

// RevokeByUser 撤销某用户的全部会话（幂等）。
func (r *AuthSessionRepository) RevokeByUser(ctx context.Context, userID string, at time.Time) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.AuthSession{}).
			Where("user_id = ? AND revoked = ?", userID, false).
			Updates(map[string]any{"revoked": true, "revoked_at": at}).Error
	})
}

// RevokeByDevice 撤销某用户某设备关联的全部会话（幂等）。
func (r *AuthSessionRepository) RevokeByDevice(ctx context.Context, userID, deviceID string, at time.Time) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.AuthSession{}).
			Where("user_id = ? AND device_id = ? AND revoked = ?", userID, deviceID, false).
			Updates(map[string]any{"revoked": true, "revoked_at": at}).Error
	})
}

// ListActiveByUser 返回某用户当前有效（未撤销、未过期）的会话。
func (r *AuthSessionRepository) ListActiveByUser(ctx context.Context, userID string) ([]model.AuthSession, error) {
	var rows []model.AuthSession
	err := withSQLiteBusyRetry(ctx, func() error {
		rows = nil
		return r.db.WithContext(ctx).
			Where("user_id = ? AND revoked = ? AND expires_at > ?", userID, false, time.Now()).
			Order("last_used_at desc, created_at desc").
			Find(&rows).Error
	})
	return rows, err
}

// ListByDevice 返回某用户某设备的全部会话，用于踢出设备时逐个撤销。
func (r *AuthSessionRepository) ListByDevice(ctx context.Context, userID, deviceID string) ([]model.AuthSession, error) {
	var rows []model.AuthSession
	err := withSQLiteBusyRetry(ctx, func() error {
		rows = nil
		return r.db.WithContext(ctx).
			Where("user_id = ? AND device_id = ?", userID, deviceID).
			Find(&rows).Error
	})
	return rows, err
}
