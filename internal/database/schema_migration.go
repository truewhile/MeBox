package database

import (
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// AutoMigrate creates tables for every model registered in the model package.
func AutoMigrate(db *gorm.DB) error {
	// 必须先于 AutoMigrate：旧库中可能已有重复的 (user_id, media_id) 历史行，
	// 不去重会导致唯一索引 uniq_user_history 创建失败。
	if err := dedupePlaybackHistories(db); err != nil {
		return err
	}
	// 书源从「全局唯一」改成「按用户独立」：先删掉 source_url 上的旧唯一索引。
	// AutoMigrate 只会按索引名创建新索引，不会删除已改名的旧唯一索引，留着它
	// 第二个用户导入同一书源就会撞唯一约束。
	if err := dropLegacyReaderSourceUniqueIndex(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		return err
	}
	// 回填书源归属：老库的书源没有 user_id，按「谁书架上有这本书」分配或复制副本。
	if err := backfillReaderSourceOwnership(db); err != nil {
		return err
	}
	if err := ensurePostgresColumnCompatibility(db); err != nil {
		return err
	}
	if err := ensurePerformanceIndexes(db); err != nil {
		return err
	}
	if err := ensureLibraryRootsCompatibility(db); err != nil {
		return err
	}
	if err := ensureEmbyMountsCompatibility(db); err != nil {
		return err
	}
	if isSQLite(db) {
		if err := ensureMediaSearchIndex(db); err != nil {
			return err
		}
		return ensureSQLiteQueryOptimizer(db)
	}
	return nil
}

func ensureSQLiteQueryOptimizer(db *gorm.DB) error {
	// Refresh planner statistics so indexes on large media tables are used.
	return db.Exec("ANALYZE").Error
}

// dropLegacyReaderSourceUniqueIndex 删除书源表上旧版 source_url 唯一索引。
//
// 书源改为按用户独立后唯一键是 (user_id, source_url)；SQLite 的 RenameTable 与
// AutoMigrate 都不会删除按旧名创建的索引（实测旧唯一索引在改名后依然生效），
// 必须显式删除，否则第二个用户导入同一书源会撞唯一约束。
func dropLegacyReaderSourceUniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.ReaderBookSource{}) {
		return nil
	}
	for _, name := range []string{"idx_reader_book_sources_source_url", "reader_book_sources_source_url_key"} {
		if db.Migrator().HasIndex(&model.ReaderBookSource{}, name) {
			if err := db.Migrator().DropIndex(&model.ReaderBookSource{}, name); err != nil {
				return err
			}
		}
	}
	// 旧版 DeleteSource 是软删：这些历史行会继续占用 (user_id, source_url) 唯一键，
	// 让「删掉再导入同一书源」失败。书源是可重新导入的数据，这里一次性清掉。
	return db.Unscoped().Where("deleted_at IS NOT NULL").Delete(&model.ReaderBookSource{}).Error
}

// backfillReaderSourceOwnership 给老库的书源补上归属用户。
//
// 书源原先全局唯一，现在按用户独立：按「谁的书架上有这本书」把书源分配给该用户；
// 同一书源被多个用户引用时，给每个用户复制一份副本（各自启停/排序/改规则），
// 这样谁都不会因为别人删源而失去书源。
//
// 没有 user_id 且无人引用的书源保持原样（user_id 为空）——它不再出现在任何人的
// 列表里，但数据仍在，需要时可以人工认领。
func backfillReaderSourceOwnership(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.ReaderBookSource{}) {
		return nil
	}
	var pending []model.ReaderBookSource
	if err := db.Where("user_id = '' OR user_id IS NULL").Find(&pending).Error; err != nil {
		return err
	}
	for i := range pending {
		src := pending[i]
		if err := assignOrCloneReaderSource(db, &src); err != nil {
			return err
		}
	}
	return nil
}

func assignOrCloneReaderSource(db *gorm.DB, src *model.ReaderBookSource) error {
	if src == nil || src.SourceURL == "" {
		return nil
	}
	// 引用该书源的书籍分布（去重到用户）。
	type ownerCount struct {
		UserID string
		N      int64
	}
	var owners []ownerCount
	if err := db.Model(&model.ReaderBook{}).
		Select("user_id, COUNT(*) AS n").
		Where("origin = ?", src.SourceURL).
		Group("user_id").Order("n DESC").Scan(&owners).Error; err != nil {
		return err
	}
	if len(owners) == 0 {
		return nil // 无人引用：保持无归属
	}
	// 第一个用户（引用最多）直接接手这一行。
	owner := owners[0].UserID
	if owner == "" {
		return nil
	}
	if err := db.Model(&model.ReaderBookSource{}).
		Where("id = ?", src.ID).
		Updates(map[string]any{"user_id": owner}).Error; err != nil {
		return err
	}
	// 其余用户各复制一份副本（新行 ID，其余字段全量拷贝）。
	for _, o := range owners[1:] {
		if o.UserID == "" || o.UserID == owner {
			continue
		}
		var exists int64
		if err := db.Model(&model.ReaderBookSource{}).
			Where("user_id = ? AND source_url = ?", o.UserID, src.SourceURL).
			Count(&exists).Error; err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		clone := *src
		clone.ID = ""
		clone.UserID = o.UserID
		if err := db.Create(&clone).Error; err != nil {
			return err
		}
	}
	return nil
}

// dedupePlaybackHistories removes duplicate (user_id, media_id) rows left by
// the former read-then-write upsert, so the uniq_user_history composite unique
// index can be created on existing databases. Keeps the most recent row per
// pair, preferring live rows over soft-deleted ones.
func dedupePlaybackHistories(db *gorm.DB) error {
	if !db.Migrator().HasTable("playback_histories") {
		return nil
	}
	return db.Exec(`
DELETE FROM playback_histories WHERE id IN (
	SELECT id FROM (
		SELECT id, ROW_NUMBER() OVER (
			PARTITION BY user_id, media_id
			ORDER BY deleted_at IS NULL DESC, watched_at DESC, id DESC
		) AS rn
		FROM playback_histories
	) ranked
	WHERE ranked.rn > 1
)`).Error
}

func ensurePostgresColumnCompatibility(db *gorm.DB) error {
	if !isPostgres(db) {
		return nil
	}
	statements := []string{
		`ALTER TABLE media ALTER COLUMN container TYPE varchar(128)`,
		`ALTER TABLE media ALTER COLUMN genres TYPE text`,
		`ALTER TABLE media ALTER COLUMN series_id TYPE varchar(128)`,
		`ALTER TABLE media ALTER COLUMN duplicate_of TYPE varchar(128)`,
		`ALTER TABLE playback_histories ALTER COLUMN media_id TYPE varchar(128)`,
		`ALTER TABLE favorites ALTER COLUMN media_id TYPE varchar(128)`,
		`ALTER TABLE playlist_items ALTER COLUMN media_id TYPE varchar(128)`,
	}
	for _, stmt := range statements {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func ensurePerformanceIndexes(db *gorm.DB) error {
	statements := []string{
		// 完整多级排序索引：媒体库分页与首页预览的 ORDER BY
		// (release_date, year, updated_at, created_at, id) DESC 与索引列完全一致，
		// LIMIT 分页沿索引顺序直取，免去对整库行做临时 B-tree 排序。
		`CREATE INDEX IF NOT EXISTS idx_media_library_recent_active ON media(library_id, release_date DESC, year DESC, updated_at DESC, created_at DESC, id DESC) WHERE deleted_at IS NULL`,
		// 计数覆盖索引：首页 CountByLibraries 的 GROUP BY library_id + nsfw 谓词
		// 全部落在索引键/部分索引条件上，纯索引扫描即可完成，不回表。
		`CREATE INDEX IF NOT EXISTS idx_media_library_nsfw_active ON media(library_id, nsfw) WHERE deleted_at IS NULL`,
		// 旧的两键前缀索引被上面的完整排序索引完全覆盖，删除以降低写放大。
		`DROP INDEX IF EXISTS idx_media_library_release_active`,
		`CREATE INDEX IF NOT EXISTS idx_media_library_created_active ON media(library_id, created_at DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_media_library_episode_active ON media(library_id, season_num, episode_num, created_at DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_media_library_root_active ON media(library_id, library_root_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_media_series_active ON media(series_id, season_num, episode_num) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_media_library_series_active ON media(library_id, series_id, season_num, episode_num) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_media_library_tmdb_active ON media(library_id, tm_db_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_media_library_bangumi_active ON media(library_id, bangumi_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_media_library_douban_active ON media(library_id, douban_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_media_library_thetvdb_active ON media(library_id, thetvdb_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_favorites_user_media_active ON favorites(user_id, media_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_playback_histories_user_media_active ON playback_histories(user_id, media_id, watched_at DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_playback_histories_resume_active ON playback_histories(user_id, completed, watched_at DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_play_profiles_user_created_active ON play_profiles(user_id, created_at DESC) WHERE deleted_at IS NULL`,
	}
	if isSQLite(db) {
		statements = append(statements,
			`CREATE INDEX IF NOT EXISTS idx_media_title_active ON media(title COLLATE NOCASE) WHERE deleted_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS idx_media_original_name_active ON media(original_name COLLATE NOCASE) WHERE deleted_at IS NULL`,
		)
	} else {
		statements = append(statements,
			`CREATE INDEX IF NOT EXISTS idx_media_title_active ON media(title) WHERE deleted_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS idx_media_original_name_active ON media(original_name) WHERE deleted_at IS NULL`,
		)
	}
	for _, stmt := range statements {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func ensureEmbyMountsCompatibility(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.EmbyMount{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&model.EmbyMount{}, "sort_order") {
		if err := db.Migrator().AddColumn(&model.EmbyMount{}, "sort_order"); err != nil {
			return err
		}
	}
	// 针对已有数据：只给 sort_order=0/NULL 的行按创建时间补号（从现有
	// 最大值之后递增），不能整表重排——此前无条件按 created_at 从 0 重新
	// 编号，会把用户自定义的顺序覆盖掉。
	var zeroCount int64
	if err := db.Model(&model.EmbyMount{}).Where("sort_order = 0 OR sort_order IS NULL").Count(&zeroCount).Error; err == nil && zeroCount > 0 {
		// max 只统计非 0 行：sort_order=0 与 NULL 同样视为“未分配”，
		// 全部为 0 时从 0 开始编号（与迁移前的初始化语义一致）。
		var maxOrder int
		_ = db.Raw("SELECT COALESCE(MAX(sort_order), -1) FROM emby_mounts WHERE sort_order > 0").Scan(&maxOrder).Error
		var mounts []model.EmbyMount
		if err := db.Where("sort_order = 0 OR sort_order IS NULL").Order("created_at asc, id asc").Find(&mounts).Error; err == nil {
			for i, m := range mounts {
				_ = db.Exec("UPDATE emby_mounts SET sort_order = ? WHERE id = ?", maxOrder+1+i, m.ID).Error
			}
		}
	}
	return nil
}
