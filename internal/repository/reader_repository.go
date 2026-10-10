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

// ListSources 用户的书源列表（书源按用户独立，按 customOrder 排序）。
func (r *ReaderRepository) ListSources(ctx context.Context, userID string) ([]model.ReaderBookSource, error) {
	var out []model.ReaderBookSource
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).
		Order("custom_order ASC, updated_at DESC").Find(&out).Error
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

// GetSourceForUser 按 ID 取书源并校验归属用户。
func (r *ReaderRepository) GetSourceForUser(ctx context.Context, userID, id string) (*model.ReaderBookSource, error) {
	var out model.ReaderBookSource
	if err := r.db.WithContext(ctx).First(&out, "id = ? AND user_id = ?", id, userID).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSourceByURL 按用户 + 书源 URL 取书源（导入去重用）。
func (r *ReaderRepository) GetSourceByURL(ctx context.Context, userID, sourceURL string) (*model.ReaderBookSource, error) {
	var out model.ReaderBookSource
	err := r.db.WithContext(ctx).First(&out, "user_id = ? AND source_url = ?", userID, sourceURL).Error
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSourceAnyByURL 按书源 URL 取任意一份副本（不限定用户）。
//
// 媒体代理、目录刷新等路径只拿得到书籍的 origin（书源 URL）而没有用户上下文；
// 请求目标由 URL 决定，任一副本都够用。多个用户各自改过规则/header 时，
// 取到哪一份只影响「用谁的默认 header」，不影响书籍本身的定位。
func (r *ReaderRepository) GetSourceAnyByURL(ctx context.Context, sourceURL string) (*model.ReaderBookSource, error) {
	var out model.ReaderBookSource
	err := r.db.WithContext(ctx).First(&out, "source_url = ?", sourceURL).Error
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListSourceRowsByURL 取某书源 URL 的全部副本（判断是否仍有其他用户引用）。
func (r *ReaderRepository) ListSourceRowsByURL(ctx context.Context, sourceURL string) ([]model.ReaderBookSource, error) {
	var out []model.ReaderBookSource
	err := r.db.WithContext(ctx).Where("source_url = ?", sourceURL).Find(&out).Error
	return out, err
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
//
// 用物理删除而不是软删：唯一键是 (user_id, source_url)，而索引会覆盖软删行——
// 软删后再导入同一书源会撞唯一约束（表现为「导入失败」），删掉再导入是本模块的
// 正常操作。书源本身是可重新导入的数据，不需要软删保留。
//
// 会话状态表仍按 source_url 一源一条（跨用户共享），因此只有在该 URL 已无任何
// 其他用户的书源副本时才连带清理，否则会把别人的登录态一起删掉。
func (r *ReaderRepository) DeleteSource(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		src := &model.ReaderBookSource{}
		if err := tx.First(src, "id = ?", id).Error; err == nil && src.SourceURL != "" {
			var others int64
			if err := tx.Model(&model.ReaderBookSource{}).
				Where("source_url = ? AND id <> ?", src.SourceURL, id).Count(&others).Error; err != nil {
				return err
			}
			if others == 0 {
				// 会话状态是「一源一条」，而 source_url 上有覆盖软删行的唯一索引：
				// 软删会让这一行继续占着 source_url，之后 SaveSourceState 的
				// First（默认排除软删行）查不到、Create 就会撞唯一约束，
				// 表现为「保存书源会话状态失败: UNIQUE constraint failed」，
				// cookie / 登录态从此再也存不进去。这里必须硬删。
				if err := tx.Unscoped().Delete(&model.ReaderSourceState{}, "source_url = ?", src.SourceURL).Error; err != nil {
					return err
				}
			}
		}
		return tx.Unscoped().Delete(&model.ReaderBookSource{}, "id = ?", id).Error
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
	// 用 Unscoped 连软删行一起找：source_url 的唯一索引覆盖软删行，
	// 只按未删行查会漏掉历史行，随后 Create 必然撞唯一约束，
	// 结果就是该源的会话状态（含 cookie、登录态）永远保存失败。
	var existing model.ReaderSourceState
	err := r.db.WithContext(ctx).Unscoped().First(&existing, "source_url = ?", st.SourceURL).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.WithContext(ctx).Create(st).Error
	}
	if err != nil {
		return err
	}
	st.ID = existing.ID
	st.CreatedAt = existing.CreatedAt
	if existing.DeletedAt.Valid {
		// 历史行被软删过：连 deleted_at 一起写回，把它复活
		st.DeletedAt = gorm.DeletedAt{}
		return r.db.WithContext(ctx).Unscoped().Save(st).Error
	}
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

// ListBooksByOriginAndURL 按书源 + 书本地址查所有用户的书架记录。
// 目录链路的 book.putVariable 需要写回变量，而目录抓取是跨用户共享的
// （同一本书可能被多个用户收藏），所以这里不带 userID 过滤。
func (r *ReaderRepository) ListBooksByOriginAndURL(ctx context.Context, origin, bookURL string) ([]model.ReaderBook, error) {
	var out []model.ReaderBook
	err := r.db.WithContext(ctx).Where("origin = ? AND book_url = ?", origin, bookURL).Find(&out).Error
	return out, err
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

// SwitchBookOrigin 换源：清空旧源章节与更新书籍信息在同一事务内完成。
// 分开提交时若第二步失败，会留下「仍指向旧源、但目录已清空」的中间状态。
func (r *ReaderRepository) SwitchBookOrigin(ctx context.Context, book *model.ReaderBook) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Delete(&model.ReaderChapter{}, "book_id = ?", book.ID).Error; err != nil {
			return err
		}
		return tx.Save(book).Error
	})
}

// ListChapters 按序取章节。
func (r *ReaderRepository) ListChapters(ctx context.Context, bookID string) ([]model.ReaderChapter, error) {
	var out []model.ReaderChapter
	err := r.db.WithContext(ctx).Where("book_id = ?", bookID).Order("`index` ASC").Find(&out).Error
	return out, err
}

// ─── 正文持久缓存索引 ──────────────────────────────────────────────────────

// GetContentCache 按 (bookKey, chapterKey, contentType) 取缓存索引行。
func (r *ReaderRepository) GetContentCache(ctx context.Context, bookKey, chapterKey, contentType string) (*model.ReaderContentCache, error) {
	var out model.ReaderContentCache
	err := r.db.WithContext(ctx).
		Where("book_key = ? AND chapter_key = ? AND content_type = ?", bookKey, chapterKey, contentType).
		First(&out).Error
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetContentCacheByChapter 按 (bookKey, chapterKey) 取缓存索引行。
// contentType 为空表示不限类型（正文链路不需要预知类型）；给出类型时精确匹配。
func (r *ReaderRepository) GetContentCacheByChapter(ctx context.Context, bookKey, chapterKey, contentType string) (*model.ReaderContentCache, error) {
	q := r.db.WithContext(ctx).Where("book_key = ? AND chapter_key = ?", bookKey, chapterKey)
	if contentType != "" {
		q = q.Where("content_type = ?", contentType)
	}
	var out model.ReaderContentCache
	if err := q.Order("last_access_at DESC").First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// UpsertContentCache 写入或更新缓存索引行（按 bookKey + chapterKey + contentType 去重）。
func (r *ReaderRepository) UpsertContentCache(ctx context.Context, row *model.ReaderContentCache) error {
	existing, err := r.GetContentCache(ctx, row.BookKey, row.ChapterKey, row.ContentType)
	if err == nil && existing != nil {
		existing.ChapterIdentity = row.ChapterIdentity
		existing.ChapterIndex = row.ChapterIndex
		existing.SourceHash = row.SourceHash
		existing.FormatVersion = row.FormatVersion
		existing.SizeBytes = row.SizeBytes
		existing.AssetCount = row.AssetCount
		existing.ExpiresAt = row.ExpiresAt
		existing.LastAccessAt = row.LastAccessAt
		return r.db.WithContext(ctx).Save(existing).Error
	}
	return r.db.WithContext(ctx).Create(row).Error
}

// TouchContentCache 记录一次命中（更新命中时间与次数）。
func (r *ReaderRepository) TouchContentCache(ctx context.Context, id string, hits int, lastAccess int64) error {
	return r.db.WithContext(ctx).Model(&model.ReaderContentCache{}).
		Where("id = ?", id).
		Updates(map[string]any{"last_access_at": lastAccess, "hits": hits}).Error
}

// ListContentCacheByBook 列出某本书的全部缓存索引（目录刷新后的 remap 用）。
func (r *ReaderRepository) ListContentCacheByBook(ctx context.Context, bookKey string) ([]model.ReaderContentCache, error) {
	var out []model.ReaderContentCache
	err := r.db.WithContext(ctx).Where("book_key = ?", bookKey).Find(&out).Error
	return out, err
}

// DeleteContentCacheByBook 删除某本书的缓存索引，返回被删除的条目（调用方据此清理磁盘文件）。
func (r *ReaderRepository) DeleteContentCacheByBook(ctx context.Context, bookKey string) ([]model.ReaderContentCache, error) {
	rows, err := r.ListContentCacheByBook(ctx, bookKey)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Where("book_key = ?", bookKey).Delete(&model.ReaderContentCache{}).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// DeleteContentCacheRow 删除单条缓存索引（remap 迁移旧键时用）。
func (r *ReaderRepository) DeleteContentCacheRow(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&model.ReaderContentCache{}, "id = ?", id).Error
}

// ListContentCacheExpired 按 TTL 取过期条目。
func (r *ReaderRepository) ListContentCacheExpired(ctx context.Context, now int64, limit int) ([]model.ReaderContentCache, error) {
	var out []model.ReaderContentCache
	err := r.db.WithContext(ctx).
		Where("expires_at > 0 AND expires_at < ?", now).
		Order("last_access_at ASC").Limit(limit).Find(&out).Error
	return out, err
}

// ContentCacheStats 返回条目数与总字节数（容量淘汰用）。
func (r *ReaderRepository) ContentCacheStats(ctx context.Context) (int64, int64, error) {
	var row struct {
		Count int64
		Bytes int64
	}
	err := r.db.WithContext(ctx).Model(&model.ReaderContentCache{}).
		Select("COUNT(*) AS count, COALESCE(SUM(size_bytes), 0) AS bytes").Scan(&row).Error
	return row.Count, row.Bytes, err
}

// ListContentCacheOldest 按最近命中时间取最旧的一批（LRU 淘汰用）。
func (r *ReaderRepository) ListContentCacheOldest(ctx context.Context, limit int) ([]model.ReaderContentCache, error) {
	var out []model.ReaderContentCache
	err := r.db.WithContext(ctx).Order("last_access_at ASC").Limit(limit).Find(&out).Error
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
