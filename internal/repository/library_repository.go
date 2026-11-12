package repository

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// LibraryRepository persists model.Library records.
type LibraryRepository struct{ db *gorm.DB }

// Create persists a new library row.
func (r *LibraryRepository) Create(ctx context.Context, l *model.Library) error {
	if l != nil && l.SortOrder == 0 {
		var maxSort int
		_ = r.db.WithContext(ctx).Model(&model.Library{}).Select("COALESCE(MAX(sort_order), -1)").Scan(&maxSort)
		l.SortOrder = maxSort + 1
	}
	return r.db.WithContext(ctx).Create(l).Error
}

func (r *LibraryRepository) CreateWithRoots(ctx context.Context, l *model.Library, roots []model.LibraryRoot) error {
	if !r.hasLibraryRootsTable() {
		return r.Create(ctx, l)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if l != nil && l.SortOrder == 0 {
			var maxSort int
			_ = tx.Model(&model.Library{}).Select("COALESCE(MAX(sort_order), -1)").Scan(&maxSort)
			l.SortOrder = maxSort + 1
		}
		if err := tx.Create(l).Error; err != nil {
			return err
		}
		for i := range roots {
			roots[i].LibraryID = l.ID
			if roots[i].SortOrder == 0 {
				roots[i].SortOrder = i
			}
			enabled := roots[i].Enabled
			if err := tx.Create(&roots[i]).Error; err != nil {
				return err
			}
			if !enabled {
				if err := tx.Model(&model.LibraryRoot{}).Where("id = ?", roots[i].ID).Update("enabled", false).Error; err != nil {
					return err
				}
				roots[i].Enabled = false
			}
		}
		l.Roots = roots
		return nil
	})
}

// List returns all enabled+disabled libraries.
func (r *LibraryRepository) List(ctx context.Context) ([]model.Library, error) {
	var ls []model.Library
	q := r.db.WithContext(ctx).Order("sort_order asc, created_at asc")
	if r.hasLibraryRootsTable() {
		q = q.Preload("Roots", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order asc, created_at asc")
		})
	}
	err := q.Find(&ls).Error
	return ls, err
}

// SetSortOrder assigns sort_order to libraries, preserving position order for
// any library not present in the provided map.
func (r *LibraryRepository) SetSortOrder(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i, id := range ids {
			if err := tx.Model(&model.Library{}).Where("id = ?", id).
				Update("sort_order", i).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// FindByID returns the library, or (nil, nil) when missing.
func (r *LibraryRepository) FindByID(ctx context.Context, id string) (*model.Library, error) {
	var l model.Library
	q := r.db.WithContext(ctx).Where("id = ?", id)
	if r.hasLibraryRootsTable() {
		q = q.Preload("Roots", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order asc, created_at asc")
		})
	}
	err := q.First(&l).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// Delete 物理删除媒体库。
func (r *LibraryRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Unscoped().Delete(&model.Library{}, "id = ?", id).Error
}

func (r *LibraryRepository) ListRoots(ctx context.Context, libraryID string) ([]model.LibraryRoot, error) {
	if !r.hasLibraryRootsTable() {
		return nil, nil
	}
	var roots []model.LibraryRoot
	err := r.db.WithContext(ctx).
		Where("library_id = ?", libraryID).
		Order("sort_order asc, created_at asc").
		Find(&roots).Error
	return roots, err
}

// ListEnabledRoots 返回全部已启用媒体库的全部启用路径。
// 用于「本地目录 → 媒体库」的反查（云下载完成后只扫相关库，而不是全库重扫）。
func (r *LibraryRepository) ListEnabledRoots(ctx context.Context) ([]model.LibraryRoot, error) {
	if !r.hasLibraryRootsTable() {
		return nil, nil
	}
	var roots []model.LibraryRoot
	err := r.db.WithContext(ctx).
		Where("enabled = ?", true).
		Order("sort_order asc, created_at asc").
		Find(&roots).Error
	return roots, err
}

func (r *LibraryRepository) FindRootByID(ctx context.Context, libraryID, rootID string) (*model.LibraryRoot, error) {
	if !r.hasLibraryRootsTable() {
		return nil, nil
	}
	var root model.LibraryRoot
	q := r.db.WithContext(ctx).Where("id = ?", rootID)
	if strings.TrimSpace(libraryID) != "" {
		q = q.Where("library_id = ?", libraryID)
	}
	err := q.First(&root).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &root, nil
}

func (r *LibraryRepository) CreateRoot(ctx context.Context, root *model.LibraryRoot) error {
	if !r.hasLibraryRootsTable() {
		return nil
	}
	enabled := root.Enabled
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(root).Error; err != nil {
			return err
		}
		if !enabled {
			if err := tx.Model(&model.LibraryRoot{}).Where("id = ?", root.ID).Update("enabled", false).Error; err != nil {
				return err
			}
			root.Enabled = false
		}
		return nil
	})
}

func (r *LibraryRepository) UpdateRoot(ctx context.Context, root *model.LibraryRoot, updates map[string]any) error {
	if !r.hasLibraryRootsTable() {
		return nil
	}
	if root == nil || strings.TrimSpace(root.ID) == "" || len(updates) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.LibraryRoot{}).Where("id = ?", root.ID).Updates(updates).Error
}

func (r *LibraryRepository) DeleteRoot(ctx context.Context, libraryID, rootID string) error {
	if !r.hasLibraryRootsTable() {
		return nil
	}
	return r.db.WithContext(ctx).Unscoped().Where("library_id = ?", libraryID).Delete(&model.LibraryRoot{}, "id = ?", rootID).Error
}

func (r *LibraryRepository) hasLibraryRootsTable() bool {
	return r != nil && r.db != nil && r.db.Migrator().HasTable(&model.LibraryRoot{})
}
