package service

import (
	"context"
	"errors"
	"strings"
	"sync"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// 请求级 payload 构建缓存：/Items 列表为每行构建 payload 时，
// mediaShouldBeEpisode 需要库类型、剧集 payload 需要 series 标题。
// 一次页面请求内这些值高度重复（同一库、同一部剧），挂在 ctx 上的
// 小缓存可以把每条目 2-3 次 DB 查询降为整个请求各 1 次预取。

type embyPayloadCacheKey struct{}

type embyLibraryTypeEntry struct {
	typ   string
	found bool // 库不存在时 found=false，调用方可退回计数启发式
}

type embyPayloadSeriesEntry struct {
	title       string
	posterURL   string
	backdropURL string
	found       bool
}

type embyPayloadCache struct {
	mu       sync.Mutex
	libTypes map[string]embyLibraryTypeEntry
	series   map[string]embyPayloadSeriesEntry
}

func (c *embyPayloadCache) libraryType(id string) (embyLibraryTypeEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.libTypes[id]
	return entry, ok
}

func (c *embyPayloadCache) setLibraryType(id string, entry embyLibraryTypeEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.libTypes[id] = entry
}

func (c *embyPayloadCache) seriesTitle(id string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.series[id]
	if !ok {
		return "", false
	}
	return entry.title, true
}

func (c *embyPayloadCache) seriesEntry(id string) (embyPayloadSeriesEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.series[id]
	return entry, ok
}

func (c *embyPayloadCache) setSeriesEntry(id string, entry embyPayloadSeriesEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.series[id] = entry
}

func (c *embyPayloadCache) setSeriesTitle(id, title string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.series[id]
	entry.title = title
	entry.found = true
	c.series[id] = entry
}

// withPayloadCache attaches a fresh request-scoped cache if none exists yet.
func (e *EmbyService) withPayloadCache(ctx context.Context) context.Context {
	if e == nil || e.repo == nil {
		return ctx
	}
	if ctx.Value(embyPayloadCacheKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, embyPayloadCacheKey{}, &embyPayloadCache{
		libTypes: map[string]embyLibraryTypeEntry{},
		series:   map[string]embyPayloadSeriesEntry{},
	})
}

// prefetchPayloadCache warms the cache for the given media rows with two bulk
// queries (library types, series titles) instead of per-item lookups.
func (e *EmbyService) prefetchPayloadCache(ctx context.Context, rows []model.Media) {
	cache, ok := ctx.Value(embyPayloadCacheKey{}).(*embyPayloadCache)
	if !ok || len(rows) == 0 {
		return
	}
	libIDs := make([]string, 0, 8)
	seriesIDs := make([]string, 0, 8)
	seenLib := map[string]struct{}{}
	seenSeries := map[string]struct{}{}
	for i := range rows {
		row := &rows[i]
		if id := strings.TrimSpace(row.LibraryID); id != "" {
			if _, done := seenLib[id]; !done {
				// 已在缓存中的库不必再查。
				if _, hit := cache.libraryType(id); !hit {
					seenLib[id] = struct{}{}
					libIDs = append(libIDs, id)
				}
			}
		}
		if id := strings.TrimSpace(row.SeriesID); id != "" {
			if _, done := seenSeries[id]; !done {
				if _, hit := cache.seriesTitle(id); !hit {
					seenSeries[id] = struct{}{}
					seriesIDs = append(seriesIDs, id)
				}
			}
		}
	}
	if len(libIDs) > 0 {
		var libs []model.Library
		if err := e.repo.DB.WithContext(ctx).Select("id, type").Where("id IN ?", libIDs).Find(&libs).Error; err == nil {
			found := map[string]string{}
			for _, lib := range libs {
				found[lib.ID] = lib.Type
			}
			for _, id := range libIDs {
				typ, ok := found[id]
				cache.setLibraryType(id, embyLibraryTypeEntry{typ: typ, found: ok})
			}
		}
	}
		if len(seriesIDs) > 0 {
			var series []model.Series
			if err := e.repo.DB.WithContext(ctx).Select("id, title, poster_url, backdrop_url").Where("id IN ?", seriesIDs).Find(&series).Error; err == nil {
				for _, s := range series {
					cache.setSeriesEntry(s.ID, embyPayloadSeriesEntry{
						title:       s.Title,
						posterURL:   s.PosterURL,
						backdropURL: s.BackdropURL,
						found:       true,
					})
				}
			}
		}
	}

	// payloadLibraryType resolves a library type through the request cache,
// falling back to a direct lookup when no cache is attached. found=false
// means the library row does not exist (soft-deleted or orphaned id).
func (e *EmbyService) payloadLibraryType(ctx context.Context, libraryID string) (typ string, found bool, err error) {
	if cache, ok := ctx.Value(embyPayloadCacheKey{}).(*embyPayloadCache); ok {
		if entry, hit := cache.libraryType(libraryID); hit {
			return entry.typ, entry.found, nil
		}
		var lib model.Library
		if dbErr := e.repo.DB.WithContext(ctx).Select("id, type").Where("id = ?", libraryID).First(&lib).Error; dbErr != nil {
			cache.setLibraryType(libraryID, embyLibraryTypeEntry{})
			return "", false, nil
		}
		cache.setLibraryType(lib.ID, embyLibraryTypeEntry{typ: lib.Type, found: true})
		return lib.Type, true, nil
	}
	var lib model.Library
	if err = e.repo.DB.WithContext(ctx).Select("id, type").Where("id = ?", libraryID).First(&lib).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return lib.Type, true, nil
}

// payloadSeriesTitle resolves a series title through the request cache,
// falling back to a direct lookup when no cache is attached.
func (e *EmbyService) payloadSeriesTitle(ctx context.Context, seriesID string) (string, bool, error) {
	entry, ok, err := e.payloadSeriesEntry(ctx, seriesID)
	if err != nil || !ok {
		return "", false, err
	}
	return entry.title, true, nil
}

	// payloadSeriesEntry resolves a series entry through the request cache,
	// falling back to a direct lookup when no cache is attached.
	func (e *EmbyService) payloadSeriesEntry(ctx context.Context, seriesID string) (embyPayloadSeriesEntry, bool, error) {
		if cache, ok := ctx.Value(embyPayloadCacheKey{}).(*embyPayloadCache); ok {
			if entry, hit := cache.seriesEntry(seriesID); hit {
				return entry, entry.found, nil
			}
			if e.repo != nil && e.repo.Series != nil {
				s, err := e.repo.Series.FindByID(ctx, seriesID)
				if err != nil || s == nil {
					cache.setSeriesEntry(seriesID, embyPayloadSeriesEntry{})
					return embyPayloadSeriesEntry{}, false, err
				}
				entry := embyPayloadSeriesEntry{
					title:       s.Title,
					posterURL:   s.PosterURL,
					backdropURL: s.BackdropURL,
					found:       true,
				}
				cache.setSeriesEntry(seriesID, entry)
				return entry, true, nil
			}
		}
		if e.repo != nil && e.repo.Series != nil {
			series, err := e.repo.Series.FindByID(ctx, seriesID)
			if err != nil {
				return embyPayloadSeriesEntry{}, false, err
			}
			if series == nil {
				return embyPayloadSeriesEntry{}, false, nil
			}
			return embyPayloadSeriesEntry{
				title:       series.Title,
				posterURL:   series.PosterURL,
				backdropURL: series.BackdropURL,
				found:       true,
			}, true, nil
		}
		return embyPayloadSeriesEntry{}, false, nil
	}
