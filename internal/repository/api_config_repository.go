package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// ApiConfigRepository persists model.APIConfig records.
type ApiConfigRepository struct{ db *gorm.DB }

// Create inserts a new API config record.
func (r *ApiConfigRepository) Create(ctx context.Context, c *model.APIConfig) error {
	return r.db.WithContext(ctx).Create(c).Error
}

// FindByProvider returns the API config for a provider, or (nil, nil).
func (r *ApiConfigRepository) FindByProvider(ctx context.Context, provider string) (*model.APIConfig, error) {
	var c model.APIConfig
	err := r.db.WithContext(ctx).Where("provider = ?", provider).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// List returns all API configs.
func (r *ApiConfigRepository) List(ctx context.Context) ([]model.APIConfig, error) {
	var rows []model.APIConfig
	err := r.db.WithContext(ctx).Order("provider asc").Find(&rows).Error
	return rows, err
}

// Upsert creates or updates an API config.
// 显式 map 更新：Assign(struct) 会跳过零值字段，导致 Enabled=false、
// 清空 BaseURL/Extra 等撤销操作静默失效。
func (r *ApiConfigRepository) Upsert(ctx context.Context, c *model.APIConfig) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.APIConfig
		err := tx.Where("provider = ?", c.Provider).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(c).Error
		}
		if err != nil {
			return err
		}
		c.ID = existing.ID
		c.CreatedAt = existing.CreatedAt
		return tx.Model(&existing).Updates(map[string]any{
			"api_key":    c.APIKey,
			"base_url":   c.BaseURL,
			"extra":      c.Extra,
			"enabled":    c.Enabled,
			"updated_at": time.Now(),
		}).Error
	})
}

// Update updates an API config.
func (r *ApiConfigRepository) Update(ctx context.Context, c *model.APIConfig) error {
	return r.db.WithContext(ctx).Model(&model.APIConfig{}).
		Where("provider = ?", c.Provider).Updates(map[string]any{
		"api_key":    c.APIKey,
		"base_url":   c.BaseURL,
		"extra":      c.Extra,
		"enabled":    c.Enabled,
		"updated_at": time.Now(),
	}).Error
}

// Delete 物理删除 API 配置。
func (r *ApiConfigRepository) Delete(ctx context.Context, provider string) error {
	return r.db.WithContext(ctx).Unscoped().Where("provider = ?", provider).Delete(&model.APIConfig{}).Error
}

// UpdateTestResult 更新测试结果。
func (r *ApiConfigRepository) UpdateTestResult(ctx context.Context, provider, result string) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&model.APIConfig{}).
		Where("provider = ?", provider).Updates(map[string]any{
		"test_result":    result,
		"last_tested_at": &now,
	}).Error
}
