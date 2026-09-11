package service

import (
	"context"
	"path/filepath"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

type localMediaWriteBatch struct {
	scanner *ScannerService
	ctx     context.Context
	res     *ScanResult
	limit   int
	items   []localMediaWriteItem
}

type localMediaWriteItem struct {
	path    string
	media   *model.Media
	aliases []string
	after   func()
}

func newLocalMediaWriteBatch(scanner *ScannerService, ctx context.Context, res *ScanResult, limit int) *localMediaWriteBatch {
	if limit <= 0 {
		limit = 100
	}
	return &localMediaWriteBatch{scanner: scanner, ctx: ctx, res: res, limit: limit}
}

func (b *localMediaWriteBatch) Add(path string, media *model.Media) {
	b.AddWithAfter(path, media, nil)
}

func (b *localMediaWriteBatch) AddWithAfter(path string, media *model.Media, after func()) {
	if b == nil || b.scanner == nil || media == nil {
		return
	}
	if media.ScrapeStatus == "" {
		media.ScrapeStatus = "pending"
	}
	b.items = append(b.items, localMediaWriteItem{
		path:    path,
		media:   media,
		aliases: missingSTRMPathAliases(path),
		after:   after,
	})
	if len(b.items) >= b.limit {
		b.Flush()
	}
}

func (b *localMediaWriteBatch) Flush() {
	if b == nil || len(b.items) == 0 || b.scanner == nil || b.scanner.repo == nil || b.scanner.repo.DB == nil {
		return
	}
	items := b.items
	b.items = nil

	existingPaths, lookupOK := b.existingPathOrAliasSet(items)
	upsertItems := make([]localMediaWriteItem, 0, len(items))
	createItems := make([]localMediaWriteItem, 0, len(items))
	for _, item := range items {
		if item.media == nil {
			continue
		}
		// If the alias lookup failed, route through UpsertWithAliases instead of
		// direct-create. That is slower but cannot create a duplicate row.
		if !lookupOK || mediaPathOrAliasExists(item.media.Path, item.aliases, existingPaths) {
			upsertItems = append(upsertItems, item)
			continue
		}
		createItems = append(createItems, item)
	}

	b.flushUpserts(upsertItems)
	if len(createItems) == 0 {
		b.publish()
		return
	}

	createMedia := make([]model.Media, 0, len(createItems))
	for _, item := range createItems {
		if item.media != nil {
			createMedia = append(createMedia, *item.media)
		}
	}
	if err := b.scanner.repo.DB.WithContext(b.ctx).CreateInBatches(&createMedia, b.limit).Error; err == nil {
		b.res.Added += len(createMedia)
		for _, item := range createItems {
			if item.after != nil {
				item.after()
			}
		}
		b.publish()
		return
	}

	for _, item := range createItems {
		if item.media == nil {
			continue
		}
		wasExisting := b.mediaPathExists(item.media.Path)
		if err := b.scanner.repo.Media.UpsertWithAliases(b.ctx, item.media, item.aliases); err != nil {
			addScanError(b.res, item.path, err)
			b.scanner.log.Warn("upsert media failed", zap.String("path", item.path), zap.Error(err))
			continue
		}
		if wasExisting {
			b.res.Updated++
		} else {
			b.res.Added++
		}
		if item.after != nil {
			item.after()
		}
	}
	b.publish()
}

// existingPathOrAliasSet loads exact and alias paths in bounded query chunks. Aliases
// have already been filtered against the filesystem by AddWithAfter, so an
// existing keep_ext sibling is never treated as a rename.
func (b *localMediaWriteBatch) existingPathOrAliasSet(items []localMediaWriteItem) (map[string]bool, bool) {
	out := map[string]bool{}
	if b == nil || b.scanner == nil || b.scanner.repo == nil || b.scanner.repo.DB == nil || len(items) == 0 {
		return out, false
	}
	seen := make(map[string]struct{}, len(items)*2)
	paths := make([]string, 0, len(items)*2)
	add := func(path string) {
		path = filepath.Clean(path)
		if path == "" || path == "." {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	for _, item := range items {
		if item.media == nil || item.media.Path == "" {
			continue
		}
		add(item.media.Path)
		for _, alias := range item.aliases {
			add(alias)
		}
	}
	if len(paths) == 0 {
		return out, true
	}
	// Keep SQL variables below the legacy SQLite limit (999). A batch can
	// contain 100 STRM rows and every row has many sibling aliases.
	const pathLookupChunk = 400
	for start := 0; start < len(paths); start += pathLookupChunk {
		end := start + pathLookupChunk
		if end > len(paths) {
			end = len(paths)
		}
		var rows []string
		if err := b.scanner.repo.DB.WithContext(b.ctx).
			Unscoped().
			Model(&model.Media{}).
			Where("path IN ?", paths[start:end]).
			Pluck("path", &rows).Error; err != nil {
			b.scanner.log.Debug("load existing media paths for scan batch failed", zap.Error(err))
			return nil, false
		}
		for _, path := range rows {
			out[filepath.Clean(path)] = true
		}
	}
	return out, true
}

func mediaPathOrAliasExists(path string, aliases []string, existing map[string]bool) bool {
	if existing[filepath.Clean(path)] {
		return true
	}
	for _, alias := range aliases {
		if existing[filepath.Clean(alias)] {
			return true
		}
	}
	return false
}

// flushUpserts 把已存在或可迁移路径的行攒成一个事务（一次提交/一组 fsync）。
// 整批失败（如单条数据触发约束）时退回逐条 upsert，只丢真正坏的那几条。
func (b *localMediaWriteBatch) flushUpserts(upsertItems []localMediaWriteItem) {
	if len(upsertItems) == 0 {
		return
	}
	batchItems := make([]repository.MediaUpsertItem, 0, len(upsertItems))
	for _, item := range upsertItems {
		batchItems = append(batchItems, repository.MediaUpsertItem{Media: item.media, AliasPaths: item.aliases})
	}
	if err := b.scanner.repo.Media.UpsertBatchWithAliases(b.ctx, batchItems); err == nil {
		b.res.Updated += len(upsertItems)
		for _, item := range upsertItems {
			if item.after != nil {
				item.after()
			}
		}
		return
	} else if b.scanner.log != nil {
		b.scanner.log.Warn("batch upsert failed; falling back to per-item upsert",
			zap.Int("items", len(upsertItems)))
	}
	for _, item := range upsertItems {
		b.upsertExistingItem(item)
	}
}

func (b *localMediaWriteBatch) upsertExistingItem(item localMediaWriteItem) {
	if item.media == nil {
		return
	}
	if err := b.scanner.repo.Media.UpsertWithAliases(b.ctx, item.media, item.aliases); err != nil {
		addScanError(b.res, item.path, err)
		b.scanner.log.Warn("upsert media failed", zap.String("path", item.path), zap.Error(err))
		return
	}
	b.res.Updated++
	if item.after != nil {
		item.after()
	}
}

func (b *localMediaWriteBatch) mediaPathExists(path string) bool {
	if b == nil || b.scanner == nil || b.scanner.repo == nil || b.scanner.repo.DB == nil || path == "" {
		return false
	}
	var count int64
	err := b.scanner.repo.DB.WithContext(b.ctx).
		Unscoped().
		Model(&model.Media{}).
		Where("path = ?", path).
		Count(&count).Error
	return err == nil && count > 0
}

func (b *localMediaWriteBatch) publish() {
	if b == nil || b.scanner == nil || b.scanner.hub == nil || b.res == nil {
		return
	}
	b.scanner.hub.Publish("scan", map[string]any{
		"library_id": b.res.LibraryID,
		"visited":    b.res.Visited,
		"added":      b.res.Added,
		"updated":    b.res.Updated,
		"probed":     b.res.Probed,
		"local_meta": b.res.LocalMetadata,
		"batched":    true,
	})
}
