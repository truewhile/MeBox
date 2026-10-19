package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// RefreshTokenRepository persists model.RefreshToken records.
type RefreshTokenRepository struct{ db *gorm.DB }

// Create inserts a new refresh token record.
func (r *RefreshTokenRepository) Create(ctx context.Context, t *model.RefreshToken) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Create(t).Error
	})
}

// FindByHash returns the refresh token matching the hash, or (nil, nil).
func (r *RefreshTokenRepository) FindByHash(ctx context.Context, hash string) (*model.RefreshToken, error) {
	var t model.RefreshToken
	err := withSQLiteBusyRetry(ctx, func() error {
		t = model.RefreshToken{}
		return r.db.WithContext(ctx).Where("token_hash = ?", hash).First(&t).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// RevokeByUserID revokes all refresh tokens for a user.
func (r *RefreshTokenRepository) RevokeByUserID(ctx context.Context, userID string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.RefreshToken{}).
			Where("user_id = ?", userID).Update("revoked", true).Error
	})
}

// RevokeOldestActiveLegacyByUserID keeps at most limit active refresh tokens
// for a user among tokens that are not bound to a device auth session
// (legacy web clients). Device sessions are independent: trimming one
// device's token because another device logged in would silently sign the
// first device out even though its session is still valid.
func (r *RefreshTokenRepository) RevokeOldestActiveLegacyByUserID(ctx context.Context, userID string, limit int) error {
	if limit < 1 {
		limit = 1
	}
	return withSQLiteBusyRetry(ctx, func() error {
		var tokens []model.RefreshToken
		if err := r.db.WithContext(ctx).
			Where("user_id = ? AND revoked = ? AND expires_at > ? AND (auth_session_id IS NULL OR auth_session_id = '')",
				userID, false, time.Now()).
			Order("created_at desc, id desc").
			Find(&tokens).Error; err != nil {
			return err
		}
		if len(tokens) <= limit {
			return nil
		}
		ids := make([]string, 0, len(tokens)-limit)
		for _, token := range tokens[limit:] {
			ids = append(ids, token.ID)
		}
		return r.db.WithContext(ctx).Model(&model.RefreshToken{}).
			Where("id IN ?", ids).Update("revoked", true).Error
	})
}

// DeleteExpired 物理清理所有过期的 refresh tokens。
func (r *RefreshTokenRepository) DeleteExpired(ctx context.Context) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Unscoped().Where("expires_at < ?", time.Now()).Delete(&model.RefreshToken{}).Error
	})
}

// Revoke 撤销某个刷新令牌。
func (r *RefreshTokenRepository) Revoke(ctx context.Context, hash string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.RefreshToken{}).
			Where("token_hash = ?", hash).Update("revoked", true).Error
	})
}

// RevokeByAuthSessionID 撤销某个认证会话名下的全部刷新令牌。
// 设备被踢或主动退出后，旧刷新令牌不能再换出新令牌。
func (r *RefreshTokenRepository) RevokeByAuthSessionID(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.RefreshToken{}).
			Where("auth_session_id = ?", sessionID).Update("revoked", true).Error
	})
}

// HashToken returns the SHA256 hash of a token.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
