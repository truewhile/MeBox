// Package repository — 阅读子系统仓储（书源 / 书架 / 章节 / 替换规则）。
package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// ReaderRepository 阅读子系统 GORM 封装。
type ReaderRepository struct {
	db *gorm.DB
}

// ListSources 书源列表（按 customOrder 排序）。
func (r *ReaderRepository) ListSources(ctx context.Context) ([]model.ReaderBookSource, error) {
	var out []model.ReaderBookSource
	err := r.db.WithContext(ctx).Order("custom_order ASC, updated_at DESC").Find(&out).Error
	return out, err
}

// GetSource 按 ID 取书源。
func (r *ReaderRepository) GetSource(ctx context.Context, id string) (*model.ReaderBookSource, error) {
	var out model.ReaderBookSource
	if err := r.db.WithContext(ctx).First(&out, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSourceByURL 按书源 URL 取书源（导入去重用）。
func (r *ReaderRepository) GetSourceByURL(ctx context.Context, sourceURL string) (*model.ReaderBookSource, error) {
	var out model.ReaderBookSource
	err := r.db.WithContext(ctx).First(&out, "source_url = ?", sourceURL).Error
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateSource 新增书源。
func (r *ReaderRepository) CreateSource(ctx context.Context, src *model.ReaderBookSource) error {
	return r.db.WithContext(ctx).Create(src).Error
}

// UpdateSource 更新书源字段。
func (r *ReaderRepository) UpdateSource(ctx context.Context, src *model.ReaderBookSource) error {
	return r.db.WithContext(ctx).Save(src).Error
}

// DeleteSource 删除书源（连带清理其会话状态）。
func (r *ReaderRepository) DeleteSource(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		src := &model.ReaderBookSource{}
		if err := tx.First(src, "id = ?", id).Error; err == nil && src.SourceURL != "" {
			if err := tx.Delete(&model.ReaderSourceState{}, "source_url = ?", src.SourceURL).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&model.ReaderBookSource{}, "id = ?", id).Error
	})
}

// GetSourceState 取书源会话状态；不存在返回 (nil, nil)。
func (r *ReaderRepository) GetSourceState(ctx context.Context, sourceURL string) (*model.ReaderSourceState, error) {
	var out model.ReaderSourceState
	err := r.db.WithContext(ctx).First(&out, "source_url = ?", sourceURL).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// SaveSourceState 覆盖保存书源会话状态（不存在则新建）。
func (r *ReaderRepository) SaveSourceState(ctx context.Context, st *model.ReaderSourceState) error {
	var existing model.ReaderSourceState
	err := r.db.WithContext(ctx).First(&existing, "source_url = ?", st.SourceURL).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.WithContext(ctx).Create(st).Error
	}
	if err != nil {
		return err
	}
	st.ID = existing.ID
	st.CreatedAt = existing.CreatedAt
	return r.db.WithContext(ctx).Save(st).Error
}

// ListBooks 用户书架（按 order 排序）。
func (r *ReaderRepository) ListBooks(ctx context.Context, userID string) ([]model.ReaderBook, error) {
	var out []model.ReaderBook
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("`order` ASC, updated_at DESC").Find(&out).Error
	return out, err
}

// GetBook 按 ID 取书。
func (r *ReaderRepository) GetBook(ctx context.Context, id string) (*model.ReaderBook, error) {
	var out model.ReaderBook
	if err := r.db.WithContext(ctx).First(&out, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// FindBookByURL 按用户 + 书源 + 书 URL 查书（加书架去重）。
func (r *ReaderRepository) FindBookByURL(ctx context.Context, userID, origin, bookURL string) (*model.ReaderBook, error) {
	var out model.ReaderBook
	err := r.db.WithContext(ctx).First(&out, "user_id = ? AND origin = ? AND book_url = ?", userID, origin, bookURL).Error
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateBook / UpdateBook / DeleteBook。
func (r *ReaderRepository) CreateBook(ctx context.Context, b *model.ReaderBook) error {
	return r.db.WithContext(ctx).Create(b).Error
}

func (r *ReaderRepository) UpdateBook(ctx context.Context, b *model.ReaderBook) error {
	return r.db.WithContext(ctx).Save(b).Error
}

func (r *ReaderRepository) DeleteBook(ctx context.Context, userID, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.ReaderBook{}, "id = ? AND user_id = ?", id, userID).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ReaderChapter{}, "book_id = ?", id).Error
	})
}

// ReplaceChapters 覆盖式刷新章节列表。
func (r *ReaderRepository) ReplaceChapters(ctx context.Context, bookID string, chapters []model.ReaderChapter) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 章节是纯缓存（软删会留下行，撞上 (book_id, index) 唯一索引），这里物理删除
		if err := tx.Unscoped().Delete(&model.ReaderChapter{}, "book_id = ?", bookID).Error; err != nil {
			return err
		}
		if len(chapters) == 0 {
			return nil
		}
		return tx.CreateInBatches(chapters, 500).Error
	})
}

// ListChapters 按序取章节。
func (r *ReaderRepository) ListChapters(ctx context.Context, bookID string) ([]model.ReaderChapter, error) {
	var out []model.ReaderChapter
	err := r.db.WithContext(ctx).Where("book_id = ?", bookID).Order("`index` ASC").Find(&out).Error
	return out, err
}

// CountChaptersByBook 一次统计多本书已缓存的章节数（书架显示未读章数用，避免逐本查询）。
// 没有目录缓存的书籍不会出现在返回结果里。
func (r *ReaderRepository) CountChaptersByBook(ctx context.Context, bookIDs []string) (map[string]int, error) {
	out := make(map[string]int, len(bookIDs))
	if len(bookIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		BookID string
		Total  int
	}
	err := r.db.WithContext(ctx).
		Model(&model.ReaderChapter{}).
		Select("book_id, COUNT(*) AS total").
		Where("book_id IN ?", bookIDs).
		Group("book_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.BookID] = row.Total
	}
	return out, nil
}

// GetChapter 取指定章节。
func (r *ReaderRepository) GetChapter(ctx context.Context, bookID string, index int) (*model.ReaderChapter, error) {
	var out model.ReaderChapter
	if err := r.db.WithContext(ctx).First(&out, "book_id = ? AND `index` = ?", bookID, index).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// GetReaderProfile 取用户阅读器偏好；没有记录时返回 (nil, nil)，
// 由调用方决定是否用前端上送的当前值播种。
func (r *ReaderRepository) GetReaderProfile(ctx context.Context, userID string) (*model.ReaderProfile, error) {
	var out model.ReaderProfile
	err := r.db.WithContext(ctx).First(&out, "user_id = ?", userID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// SaveReaderProfile 覆盖保存用户阅读器偏好（不存在则新建）。
func (r *ReaderRepository) SaveReaderProfile(ctx context.Context, p *model.ReaderProfile) error {
	var existing model.ReaderProfile
	err := r.db.WithContext(ctx).First(&existing, "user_id = ?", p.UserID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.WithContext(ctx).Create(p).Error
	}
	if err != nil {
		return err
	}
	p.ID = existing.ID
	p.CreatedAt = existing.CreatedAt
	return r.db.WithContext(ctx).Save(p).Error
}

// GetBookGroups 取用户的书架分组；没有记录时返回 (nil, nil)。
func (r *ReaderRepository) GetBookGroups(ctx context.Context, userID string) (*model.ReaderBookGroups, error) {
	var out model.ReaderBookGroups
	err := r.db.WithContext(ctx).First(&out, "user_id = ?", userID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// SaveBookGroups 覆盖保存用户的书架分组（不存在则新建）。
func (r *ReaderRepository) SaveBookGroups(ctx context.Context, row *model.ReaderBookGroups) error {
	var existing model.ReaderBookGroups
	err := r.db.WithContext(ctx).First(&existing, "user_id = ?", row.UserID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.WithContext(ctx).Create(row).Error
	}
	if err != nil {
		return err
	}
	row.ID = existing.ID
	row.CreatedAt = existing.CreatedAt
	return r.db.WithContext(ctx).Save(row).Error
}

// ListReplaceRules 用户替换规则（按 order 排序）。
func (r *ReaderRepository) ListReplaceRules(ctx context.Context, userID string) ([]model.ReaderReplaceRule, error) {
	var out []model.ReaderReplaceRule
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("`order` DESC, created_at ASC").Find(&out).Error
	return out, err
}

// CreateReplaceRule / UpdateReplaceRule / DeleteReplaceRule。
func (r *ReaderRepository) CreateReplaceRule(ctx context.Context, rule *model.ReaderReplaceRule) error {
	return r.db.WithContext(ctx).Create(rule).Error
}

func (r *ReaderRepository) UpdateReplaceRule(ctx context.Context, rule *model.ReaderReplaceRule) error {
	return r.db.WithContext(ctx).Save(rule).Error
}

func (r *ReaderRepository) DeleteReplaceRule(ctx context.Context, userID, id string) error {
	return r.db.WithContext(ctx).Delete(&model.ReaderReplaceRule{}, "id = ? AND user_id = ?", id, userID).Error
}
