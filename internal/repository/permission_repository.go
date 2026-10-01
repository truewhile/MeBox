package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// PermissionRepository persists model.UserPermission records.
type PermissionRepository struct{ db *gorm.DB }

// Create inserts a new permission record.
func (r *PermissionRepository) Create(ctx context.Context, p *model.UserPermission) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Create(p).Error
	})
}

// FindByUserID returns the permission record for a user, or (nil, nil) when absent.
func (r *PermissionRepository) FindByUserID(ctx context.Context, userID string) (*model.UserPermission, error) {
	var p model.UserPermission
	err := withSQLiteBusyRetry(ctx, func() error {
		p = model.UserPermission{}
		return r.db.WithContext(ctx).Where("user_id = ?", userID).First(&p).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Update updates permission fields for a user.
func (r *PermissionRepository) Update(ctx context.Context, userID string, updates map[string]bool) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.UserPermission{}).
			Where("user_id = ?", userID).Updates(updates).Error
	})
}

// Upsert creates or updates a permission record.
// 显式 map 更新：Assign(struct) 会被 GORM 跳过零值字段，导致权限
// "撤销"（false）保存后静默失效且无法重置。
func (r *PermissionRepository) Upsert(ctx context.Context, p *model.UserPermission) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var existing model.UserPermission
			err := tx.Where("user_id = ?", p.UserID).First(&existing).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return tx.Create(p).Error
			}
			if err != nil {
				return err
			}
			p.ID = existing.ID
			p.CreatedAt = existing.CreatedAt
			return tx.Model(&existing).Updates(map[string]any{
				"can_view_dashboard":       p.CanViewDashboard,
				"can_play_media":           p.CanPlayMedia,
				"can_cast":                 p.CanCast,
				"can_external_player":      p.CanExternalPlayer,
				"can_favorite":             p.CanFavorite,
				"can_view_history":         p.CanViewHistory,
				"can_edit_media":           p.CanEditMedia,
				"can_rescrape":             p.CanRescrape,
				"can_use_ai":               p.CanUseAI,
				"can_capture_frames":       p.CanCaptureFrames,
				"can_manage_downloads":     p.CanManageDownloads,
				"can_manage_subscriptions": p.CanManageSubscriptions,
				"can_manage_sites":         p.CanManageSites,
				"can_use_ai_assistant":     p.CanUseAIAssistant,
				"can_manage_users":         p.CanManageUsers,
				"can_manage_files":         p.CanManageFiles,
				"can_manage_strm":          p.CanManageStrm,
				"can_access_settings":      p.CanAccessSettings,
				"updated_at":               time.Now(),
			}).Error
		})
	})
}

// Delete 物理删除权限记录。
func (r *PermissionRepository) Delete(ctx context.Context, userID string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Unscoped().Where("user_id = ?", userID).Delete(&model.UserPermission{}).Error
	})
}
