package repository

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// MediaUpsertItem carries a media row and optional sibling paths from an
// earlier keep_ext naming mode. An alias is only used when the exact new path
// does not exist in the database; in that case the existing row is migrated to
// the new path so scraped metadata survives renames such as:
//
//	foo.strm -> foo.mkv.strm
//	foo.mkv.strm -> foo.strm
type MediaUpsertItem struct {
	Media      *model.Media
	AliasPaths []string
}

// Upsert inserts or updates a media row keyed by Path (unique index).
//
// 重要：当一条行已经存在时，scanner 重扫只应该刷新文件级元数据
// （时长、宽高、编码、容器、大小），不能把刮削器维护的字段（标题改写、
// 海报、TMDb/Bangumi ID、scrape_status 等）覆盖回零值。
//
// 之前用 Assign(*m).FirstOrCreate(m) 会把整张零值结构体写回，导致：
//  1. scrape_status 从 'matched' / 'no_match' 被清空成 ”；
//  2. 新建行使 GORM `default:pending` 也得不到应用（因为 zero value 被
//     显式写入）。这两个问题都让 EnrichLibrary(WHERE scrape_status='pending')
//     永远捞不到数据。
func (r *MediaRepository) Upsert(ctx context.Context, m *model.Media) error {
	return r.UpsertWithAliases(ctx, m, nil)
}

// UpsertWithAliases is Upsert with explicit, filesystem-verified STRM sibling
// aliases. It preserves the old row's ID, CreatedAt and scraped metadata while
// moving it to the current path.
func (r *MediaRepository) UpsertWithAliases(ctx context.Context, m *model.Media, aliasPaths []string) error {
	return r.UpsertBatchWithAliases(ctx, []MediaUpsertItem{{Media: m, AliasPaths: aliasPaths}})
}

// UpsertBatch 在单个事务里逐条执行 Upsert：扫描一批只提交（fsync）一次，
// 而不是每条一个隐式事务。任一条目落库失败不影响批内已成功的条目——
// 事务回滚后由调用方退回逐条 Upsert 兜底。
//
// OpenSearch 索引同步（HTTP，4s 超时）必须在事务提交之后统一执行：放在
// 事务内会把 SQLite 写锁挂起在网络 IO 上，且批内用非事务连接回读只能
// 拿到提交前的旧版本数据，把陈旧内容写进索引。
func (r *MediaRepository) UpsertBatch(ctx context.Context, items []*model.Media) error {
	mapped := make([]MediaUpsertItem, 0, len(items))
	for _, item := range items {
		if item != nil {
			mapped = append(mapped, MediaUpsertItem{Media: item})
		}
	}
	return r.UpsertBatchWithAliases(ctx, mapped)
}

// UpsertBatchWithAliases runs alias-aware upserts in one transaction.
func (r *MediaRepository) UpsertBatchWithAliases(ctx context.Context, items []MediaUpsertItem) error {
	if len(items) == 0 {
		return nil
	}
	indexIDs := make([]string, 0, len(items))
	err := withSQLiteBusyRetry(ctx, func() error {
		indexIDs = indexIDs[:0]
		return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, item := range items {
				if item.Media == nil {
					continue
				}
				id, err := r.upsertWithDB(ctx, tx, item.Media, item.AliasPaths)
				if err != nil {
					return err
				}
				if id != "" {
					indexIDs = append(indexIDs, id)
				}
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	r.indexByIDBestEffort(ctx, indexIDs)
	return nil
}

// indexByIDBestEffort 在事务提交后按 ID 回读最新行并同步搜索索引。
func (r *MediaRepository) indexByIDBestEffort(ctx context.Context, ids []string) {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if fresh, err := r.FindByID(ctx, id); err == nil && fresh != nil {
			r.indexMediaBestEffort(ctx, *fresh)
		}
	}
}

// upsertWithDB 落库（新建、更新或从旧路径迁移），返回需要重建索引的媒体 ID。
func (r *MediaRepository) upsertWithDB(ctx context.Context, db *gorm.DB, m *model.Media, aliasPaths []string) (string, error) {
	existing, created, adopted, err := r.findOrCreateMediaByPath(ctx, db, m, aliasPaths)
	if err != nil {
		return "", err
	}
	if created {
		return m.ID, nil
	}

	updates := mediaUpsertUpdates(existing, *m)
	if adopted {
		updates["path"] = m.Path
		if existing.DeletedAt.Valid {
			updates["deleted_at"] = nil
		}
	}
	if len(updates) == 0 {
		*m = existing
		return "", nil
	}
	if err := db.WithContext(ctx).Unscoped().Model(&model.Media{}).
		Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
		return "", err
	}
	// 回写 ID / 不可变字段，让 caller 拿到完整的现有行。
	if adopted {
		existing.Path = m.Path
		existing.DeletedAt = gorm.DeletedAt{}
	}
	*m = existing
	return existing.ID, nil
}

func (r *MediaRepository) findOrCreateMediaByPath(ctx context.Context, db *gorm.DB, m *model.Media, aliasPaths []string) (model.Media, bool, bool, error) {
	var existing model.Media
	err := db.WithContext(ctx).Unscoped().Where("path = ?", m.Path).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if alias, aliasErr := r.findMediaByAlias(ctx, db, m.Path, aliasPaths); aliasErr == nil {
			return alias, false, true, nil
		} else if !errors.Is(aliasErr, gorm.ErrRecordNotFound) {
			return model.Media{}, false, false, aliasErr
		}
		// 新行：保证 scrape_status 走 GORM default:pending（即留空让数据库填）。
		if m.ScrapeStatus == "" {
			m.ScrapeStatus = "pending"
		}
		if createErr := db.WithContext(ctx).Create(m).Error; createErr == nil {
			return *m, true, false, nil
		} else if retryErr := db.WithContext(ctx).Unscoped().Where("path = ?", m.Path).First(&existing).Error; retryErr != nil {
			return model.Media{}, false, false, createErr
		} else {
			// 并发插入竞态：重查已命中既有行，直接走更新分支。
			return existing, false, false, nil
		}
	}
	if err != nil {
		return model.Media{}, false, false, err
	}
	return existing, false, false, nil
}

func (r *MediaRepository) findMediaByAlias(ctx context.Context, db *gorm.DB, currentPath string, aliasPaths []string) (model.Media, error) {
	aliases := make([]string, 0, len(aliasPaths))
	seen := make(map[string]struct{}, len(aliasPaths))
	for _, alias := range aliasPaths {
		alias = filepath.Clean(strings.TrimSpace(alias))
		if alias == "" || alias == "." || alias == filepath.Clean(currentPath) {
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	if len(aliases) == 0 {
		return model.Media{}, gorm.ErrRecordNotFound
	}
	var existing model.Media
	err := db.WithContext(ctx).Unscoped().
		Where("path IN ?", aliases).
		Order("CASE WHEN scrape_status = 'matched' THEN 0 ELSE 1 END ASC, " +
			"CASE WHEN COALESCE(poster_url, '') <> '' OR COALESCE(overview, '') <> '' THEN 0 ELSE 1 END ASC, " +
			"CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END ASC, updated_at DESC, created_at DESC").
		First(&existing).Error
	return existing, err
}

func mediaUpsertUpdates(existing, incoming model.Media) map[string]any {
	updates := map[string]any{}
	addMediaFileScanUpdates(updates, existing, incoming)
	addMediaTitleUpdates(updates, existing, incoming)
	addMediaExternalIDUpdates(updates, existing, incoming)
	addMatchedMediaMetadataUpdates(updates, existing, incoming)
	addMediaArtworkUpdates(updates, existing, incoming)
	addMediaPlacementUpdates(updates, existing, incoming)
	addMediaSTRMUpdate(updates, existing, incoming)
	return updates
}

func addMediaFileScanUpdates(updates map[string]any, existing, incoming model.Media) {
	// 已存在：仅刷新文件层面的字段。
	setIfChanged(updates, "size_bytes", existing.SizeBytes, incoming.SizeBytes)
	setIfChanged(updates, "duration_sec", existing.DurationSec, incoming.DurationSec)
	setIfChanged(updates, "width", existing.Width, incoming.Width)
	setIfChanged(updates, "height", existing.Height, incoming.Height)
	setIfChanged(updates, "video_codec", existing.VideoCodec, incoming.VideoCodec)
	setIfChanged(updates, "audio_codec", existing.AudioCodec, incoming.AudioCodec)
	setIfChanged(updates, "container", existing.Container, incoming.Container)
	if existing.DeletedAt.Valid {
		updates["deleted_at"] = nil
	}
	// 回填硬链接身份标识，便于后续扫描去重（避免重复识别/多倍占用）。
	if incoming.FileID != "" && incoming.FileID != existing.FileID {
		updates["file_id"] = incoming.FileID
	}
}

func addMediaTitleUpdates(updates map[string]any, existing, incoming model.Media) {
	if incoming.Title != "" {
		// scanner 给出的标题只是从路径推导，刮削后 title 已被替换为
		// 真实剧名。仅在 existing 还停留在 'pending'/'' 时回填扫描标题，
		// 避免覆盖刮削结果。
		if incoming.ScrapeStatus == "matched" || existing.ScrapeStatus == "pending" || existing.ScrapeStatus == "" || existing.ScrapeStatus == "no_match" {
			titleChanged := !strings.EqualFold(strings.TrimSpace(existing.Title), strings.TrimSpace(incoming.Title))
			yearChanged := incoming.Year > 0 && existing.Year != incoming.Year
			setIfChanged(updates, "title", existing.Title, incoming.Title)
			if incoming.Year > 0 {
				setIfChanged(updates, "year", existing.Year, incoming.Year)
			}
			if incoming.ReleaseDate != "" {
				setIfChanged(updates, "release_date", existing.ReleaseDate, incoming.ReleaseDate)
			}
			if strings.TrimSpace(existing.ScrapeStatus) == "no_match" && incoming.ScrapeStatus != "matched" && (titleChanged || yearChanged) {
				updates["scrape_status"] = "pending"
			}
		}
	}
}

func addMediaExternalIDUpdates(updates map[string]any, existing, incoming model.Media) {
	status := strings.TrimSpace(existing.ScrapeStatus)
	if !mediaCanRefreshExternalIDs(status, incoming) {
		return
	}
	changedExternalID := addIncomingMediaProviderIDs(updates, existing, incoming)
	if incoming.Year > 0 && existing.Year <= 0 {
		updates["year"] = incoming.Year
	}
	if incoming.ReleaseDate != "" && existing.ReleaseDate == "" {
		updates["release_date"] = incoming.ReleaseDate
	}
	if changedExternalID && (status == "no_match" || status == "matched") && incoming.ScrapeStatus != "matched" {
		updates["scrape_status"] = "pending"
	}
}

func mediaCanRefreshExternalIDs(existingStatus string, incoming model.Media) bool {
	return existingStatus == "pending" || existingStatus == "" || existingStatus == "no_match" ||
		incoming.ScrapeStatus == "matched" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(incoming.Path)), "cloud://")
}

func addMatchedMediaMetadataUpdates(updates map[string]any, existing, incoming model.Media) {
	if incoming.ScrapeStatus == "matched" {
		setIfChanged(updates, "scrape_status", existing.ScrapeStatus, incoming.ScrapeStatus)
		addMatchedMediaDetailUpdates(updates, existing, incoming)
		addIncomingMediaProviderIDs(updates, existing, incoming)
	}
}

func addMatchedMediaDetailUpdates(updates map[string]any, existing, incoming model.Media) {
	setNonEmptyMediaString(updates, "original_name", existing.OriginalName, incoming.OriginalName)
	setNonEmptyMediaString(updates, "episode_title", existing.EpisodeTitle, incoming.EpisodeTitle)
	setNonEmptyMediaString(updates, "poster_url", existing.PosterURL, incoming.PosterURL)
	setNonEmptyMediaString(updates, "backdrop_url", existing.BackdropURL, incoming.BackdropURL)
	setNonEmptyMediaString(updates, "overview", existing.Overview, incoming.Overview)
	setNonEmptyMediaString(updates, "languages", existing.Languages, incoming.Languages)
	setNonEmptyMediaString(updates, "countries", existing.Countries, incoming.Countries)
	setNonEmptyMediaString(updates, "genres", existing.Genres, incoming.Genres)
	if incoming.Rating > 0 {
		setIfChanged(updates, "rating", existing.Rating, incoming.Rating)
	}
	if incoming.Year > 0 {
		setIfChanged(updates, "year", existing.Year, incoming.Year)
	}
	if incoming.ReleaseDate != "" {
		setIfChanged(updates, "release_date", existing.ReleaseDate, incoming.ReleaseDate)
	}
	if incoming.NSFW && !existing.NSFW {
		updates["nsfw"] = true
	}
}

func addMediaArtworkUpdates(updates map[string]any, existing, incoming model.Media) {
	if incoming.PosterURL != "" {
		setIfChanged(updates, "poster_url", existing.PosterURL, incoming.PosterURL)
	}
	if incoming.BackdropURL != "" {
		setIfChanged(updates, "backdrop_url", existing.BackdropURL, incoming.BackdropURL)
	}
}

func addMediaPlacementUpdates(updates map[string]any, existing, incoming model.Media) {
	// 云盘媒体：同一 cloud:// 文件可能先被父目录库扫描入库，之后用户按二级
	// 分类重新挂载/扫描到更精确的分类库。此时让 library_id 迁移到当前扫描库，
	// 否则媒体被钉死在旧库、新分类库里看不到(表现为"媒体部分消失")。
	// 本地媒体物理位置固定：仅在原 library_id 为空时回填，不迁移。
	if isCloudMediaPath := strings.HasPrefix(strings.ToLower(strings.TrimSpace(incoming.Path)), "cloud://"); incoming.LibraryID != "" && incoming.LibraryID != existing.LibraryID {
		if isCloudMediaPath || existing.LibraryID == "" {
			updates["library_id"] = incoming.LibraryID
		}
	}
	if incoming.LibraryRootID != "" && incoming.LibraryRootID != existing.LibraryRootID {
		updates["library_root_id"] = incoming.LibraryRootID
	}
	if incoming.RelativePath != "" && incoming.RelativePath != existing.RelativePath {
		updates["relative_path"] = incoming.RelativePath
	}
	seasonChanged := (incoming.SeasonNum > 0 || incoming.EpisodeNum > 0) && existing.SeasonNum != incoming.SeasonNum
	episodeChanged := incoming.EpisodeNum > 0 && existing.EpisodeNum != incoming.EpisodeNum
	if seasonChanged {
		updates["season_num"] = incoming.SeasonNum
	}
	if episodeChanged {
		updates["episode_num"] = incoming.EpisodeNum
	}
	if strings.TrimSpace(existing.ScrapeStatus) == "no_match" && incoming.ScrapeStatus != "matched" && (seasonChanged || episodeChanged) {
		updates["scrape_status"] = "pending"
	}
}

func addMediaSTRMUpdate(updates map[string]any, existing, incoming model.Media) {
	if incoming.STRMURL != "" {
		setIfChanged(updates, "strm_url", existing.STRMURL, incoming.STRMURL)
	}
}

func addIncomingMediaProviderIDs(updates map[string]any, existing, incoming model.Media) bool {
	changed := false
	if incoming.TMDbID > 0 && existing.TMDbID != incoming.TMDbID {
		updates["tm_db_id"] = incoming.TMDbID
		changed = true
	}
	if incoming.BangumiID > 0 && existing.BangumiID != incoming.BangumiID {
		updates["bangumi_id"] = incoming.BangumiID
		changed = true
	}
	if incoming.DoubanID != "" && strings.TrimSpace(existing.DoubanID) != strings.TrimSpace(incoming.DoubanID) {
		updates["douban_id"] = incoming.DoubanID
		changed = true
	}
	if incoming.TheTVDBID != "" && strings.TrimSpace(existing.TheTVDBID) != strings.TrimSpace(incoming.TheTVDBID) {
		updates["thetvdb_id"] = incoming.TheTVDBID
		changed = true
	}
	return changed
}

func setNonEmptyMediaString(updates map[string]any, key, current, next string) {
	if next != "" {
		setIfChanged(updates, key, current, next)
	}
}

func setIfChanged[T comparable](updates map[string]any, key string, current, next T) {
	if current != next {
		updates[key] = next
	}
}
