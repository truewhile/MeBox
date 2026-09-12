package service

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

type embyItemsCacheValue struct {
	Items            []map[string]any          `json:"items"`
	TotalRecordCount int64                     `json:"total_record_count"`
	StartIndex       int                       `json:"start_index"`
	Artwork          map[string]embyArtworkRef `json:"artwork,omitempty"`
}

type embyLatestCacheValue struct {
	Items   []map[string]any          `json:"items"`
	Artwork map[string]embyArtworkRef `json:"artwork,omitempty"`
}

type embyCountsCacheValue struct {
	MovieCount   int64 `json:"movie_count"`
	SeriesCount  int64 `json:"series_count"`
	EpisodeCount int64 `json:"episode_count"`
	ItemCount    int64 `json:"item_count"`
}

func (e *EmbyService) embyItemsCacheKey(kind string, p ItemsParams) string {
	includeTypes := append([]string(nil), p.IncludeItemTypes...)
	filters := append([]string(nil), p.Filters...)
	ids := append([]string(nil), p.IDs...)
	sort.Strings(includeTypes)
	sort.Strings(filters)
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join([]string{
		kind,
		p.UserID,
		p.ParentID,
		strings.Join(ids, ","),
		p.SearchTerm,
		strings.Join(includeTypes, ","),
		strings.Join(filters, ","),
		strconv.FormatBool(p.Recursive),
		p.SortBy,
		p.SortOrder,
		strconv.Itoa(p.StartIndex),
		strconv.Itoa(p.Limit),
	}, "|")))
	return "media:emby:" + hex.EncodeToString(sum[:])
}

func (e *EmbyService) embyLatestCacheKey(userID, parentID string, limit int) string {
	// v2: payload tags for virtual artwork changed so clients drop cached placeholders.
	sum := sha256.Sum256([]byte(strings.Join([]string{"latest-v2", userID, parentID, strconv.Itoa(limit)}, "|")))
	return "media:emby:" + hex.EncodeToString(sum[:])
}

// defaultEmbyLatestCacheTTLSeconds 是 Emby「最新添加」缓存的兜底时长。
const defaultEmbyLatestCacheTTLSeconds = 300

// embyLatestCacheTTLSeconds 返回「最新添加」列表的缓存时长。它刻意比通用
// 媒体缓存更长：客户端刷新首页时会同时请求全部媒体库的 Latest（生产环境
// 观察到 73 个并发），缓存一旦集中过期，这批请求会同时穿透并各自重建
// payload。延长后稳态下几乎全部命中缓存，冷启动频率也随之下降。
func (e *EmbyService) embyLatestCacheTTLSeconds() int {
	if e == nil || e.cfg == nil || e.cfg.Cache.EmbyLatestTTLSeconds < 1 {
		return defaultEmbyLatestCacheTTLSeconds
	}
	return e.cfg.Cache.EmbyLatestTTLSeconds
}

func (e *EmbyService) mediaCacheTTLSeconds() int {
	if e == nil || e.cfg == nil || e.cfg.Cache.MediaTTLSeconds < 1 {
		return 90
	}
	return e.cfg.Cache.MediaTTLSeconds
}
