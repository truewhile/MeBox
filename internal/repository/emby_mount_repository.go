package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// EmbyMountRepository 持久化远程 Emby 媒体库挂载。
type EmbyMountRepository struct{ db *gorm.DB }

func (r *EmbyMountRepository) Create(ctx context.Context, m *model.EmbyMount) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if m != nil && m.SortOrder == 0 {
				var maxSort int
				_ = tx.Model(&model.EmbyMount{}).Select("COALESCE(MAX(sort_order), -1)").Scan(&maxSort)
				m.SortOrder = maxSort + 1
			}
			return tx.Create(m).Error
		})
	})
}

func (r *EmbyMountRepository) CreateInBatches(ctx context.Context, mounts []*model.EmbyMount, batchSize int) error {
	if len(mounts) == 0 {
		return nil
	}
	if batchSize <= 0 {
		batchSize = 50
	}
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var maxSort int
			_ = tx.Model(&model.EmbyMount{}).Select("COALESCE(MAX(sort_order), -1)").Scan(&maxSort)
			for _, m := range mounts {
				if m != nil && m.SortOrder == 0 {
					maxSort++
					m.SortOrder = maxSort
				}
			}
			return tx.CreateInBatches(mounts, batchSize).Error
		})
	})
}

func (r *EmbyMountRepository) FindByID(ctx context.Context, id string) (*model.EmbyMount, error) {
	var m model.EmbyMount
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *EmbyMountRepository) List(ctx context.Context) ([]model.EmbyMount, error) {
	var rows []model.EmbyMount
	err := r.db.WithContext(ctx).Order("sort_order asc, created_at asc").Find(&rows).Error
	return rows, err
}

func (r *EmbyMountRepository) ListByAccountID(ctx context.Context, accountID string) ([]model.EmbyMount, error) {
	var rows []model.EmbyMount
	err := r.db.WithContext(ctx).Where("account_id = ?", accountID).Order("sort_order asc, created_at asc").Find(&rows).Error
	return rows, err
}

func (r *EmbyMountRepository) SetSortOrder(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i, id := range ids {
				if err := tx.Model(&model.EmbyMount{}).Where("id = ?", id).
					Update("sort_order", i).Error; err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func (r *EmbyMountRepository) CountByAccountID(ctx context.Context, accountID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.EmbyMount{}).Where("account_id = ?", accountID).Count(&count).Error
	return count, err
}

func (r *EmbyMountRepository) Update(ctx context.Context, m *model.EmbyMount) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&model.EmbyMount{}).Where("id = ?", m.ID).Updates(map[string]any{
			"name":             m.Name,
			"proxy_play":       m.ProxyPlay,
			"enabled":          m.Enabled,
			"remote_view_id":   m.RemoteViewID,
			"remote_view_name": m.RemoteViewName,
			"collection_type":  m.CollectionType,
			"updated_at":       time.Now(),
		}).Error
	})
}

func (r *EmbyMountRepository) Delete(ctx context.Context, id string) error {
	return withSQLiteBusyRetry(ctx, func() error {
		return r.db.WithContext(ctx).Where("id = ?", id).Delete(&model.EmbyMount{}).Error
	})
}

// DeleteByAccountID 删除账号下全部挂载（删除账号时级联清理）。
func (r *EmbyMountRepository) DeleteByAccountID(ctx context.Context, accountID string) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).Where("account_id = ?", accountID).Delete(&model.EmbyMount{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}

// DeleteOrphans 清理账号已不存在的挂载（老版本删除账号未级联的历史残留）。
func (r *EmbyMountRepository) DeleteOrphans(ctx context.Context) (int64, error) {
	var count int64
	err := withSQLiteBusyRetry(ctx, func() error {
		res := r.db.WithContext(ctx).
			Where("account_id NOT IN (SELECT id FROM strm_accounts)").
			Delete(&model.EmbyMount{})
		count = res.RowsAffected
		return res.Error
	})
	return count, err
}
