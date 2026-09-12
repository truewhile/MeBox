// Package service — Emby/Jellyfin compatibility shim.
//
// EmbyService produces JSON envelopes shaped like the most-consumed
// Emby-API endpoints so existing players (Infuse / Yamby / Hills /
// Senplayer / Kodi NextPVR extension / iOS native clients) can talk to
// MeBox without a custom plugin.
//
// The shim is read-mostly: items, images, playback are fully covered;
// 播放进度上报 / 收藏切换 是写路径但走我们自己的 PlaybackHistory /
// Favorite 表，所以 Emby 客户端的"标记已看 / 收藏"也会反向同步到
// 我们自己的 React UI。
package service

import (
	"context"
	"regexp"
	"sync"
	"time"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"go.uber.org/zap"
)

// 用一个固定的 ServerId 字符串。Emby 客户端会缓存这个 id，第一次见到
// 该 id 后会把所有派生数据（cookie/收藏/历史）和它绑定。
const embyServerID = "mebox-001"

// embyCompatVersion deliberately reports an Emby 4.x server. Official Emby
// clients reject Jellyfin-style 10.x identities as unsupported/too old during
// the login handshake, even when the API shape is compatible enough for us.
const embyCompatVersion = "4.8.10.0"

const (
	embyLocalAuthenticationProviderID = "Emby.Server.Implementations.LocalAuthenticationProvider" // #nosec G101 -- Emby provider identifier, not a credential.
	embyLocalPasswordResetProviderID  = "Emby.Server.Implementations.LocalPasswordResetProvider"  // #nosec G101 -- Emby provider identifier, not a credential.
)

// PlaybackDirectOnlySettingKey 控制「客户端直连解码」模式：开启后宿主机
// 不再提供转码，所有播放交给第三方客户端本地解码（direct play / 302 直链），
// 以释放宿主机 CPU 资源。
const PlaybackDirectOnlySettingKey = "playback.direct_only"

// EmbyService produces Emby-shaped JSON.
type EmbyService struct {
	cfg      *config.Config
	log      *zap.Logger
	repo     *repository.Container
	cache    *RuntimeCacheService
	subtitle *SubtitleService
	remote   *EmbyRemoteService // 远程 Emby 联邦聚合（可为 nil：未启用）

	virtualMu      sync.RWMutex
	virtualSeries  map[string]embySeriesCacheEntry
	virtualSeasons map[string]embySeasonCacheEntry
	virtualArtwork map[string]embyArtworkCacheEntry

	visibilityMu    sync.RWMutex
	visibilityCache map[string]embyVisibilityCacheEntry

	libraryCoverMu    sync.Mutex
	libraryCoverCache map[string]embyArtworkCacheEntry

	peopleMu    sync.RWMutex
	peopleCache map[string]embyPeopleCacheEntry
}

// NewEmbyService is the constructor.
func NewEmbyService(cfg *config.Config, log *zap.Logger, repo *repository.Container) *EmbyService {
	return &EmbyService{cfg: cfg, log: log, repo: repo}
}

// SetEmbyRemote 注入远程 Emby 联邦聚合服务（nil 表示未启用）。
func (e *EmbyService) SetEmbyRemote(remote *EmbyRemoteService) *EmbyService {
	if e != nil {
		e.remote = remote
	}
	return e
}

func (e *EmbyService) SetRuntimeCache(cache *RuntimeCacheService) *EmbyService {
	if e != nil {
		e.cache = cache
	}
	return e
}

// SetSubtitleService wires the external-subtitle discovery service into the
// Emby shim so MediaStreams can advertise sideloaded subtitle tracks. It is
// nil-safe: when subtitle is nil, mediaStreams simply emits no subtitle
// streams and keeps the pre-existing Video/Audio behaviour.
func (e *EmbyService) SetSubtitleService(subtitle *SubtitleService) *EmbyService {
	if e != nil {
		e.subtitle = subtitle
	}
	return e
}

// ─── Items ───────────────────────────────────────────────────────────────────

// ItemsParams 是 /Items 与 /Users/{uid}/Items 共用的查询参数。
type ItemsParams struct {
	UserID           string
	ParentID         string
	IDs              []string
	SearchTerm       string
	IncludeItemTypes []string
	Filters          []string
	Recursive        bool
	SortBy           string
	SortOrder        string
	Limit            int
	StartIndex       int
}

const (
	embyVirtualSeriesPrefix = "msgo-series-"
	embyVirtualSeasonPrefix = "msgo-season-"
	embyVirtualCacheTTL     = 10 * time.Minute
	embyVisibilityCacheTTL  = 30 * time.Second
	embySeriesGroupingLimit = maxMediaSearchLimit
	// Virtual artwork used to be wiped entirely once the in-memory map crossed
	// a few thousand entries. A homepage refresh asks Latest for every library
	// at once, so that wipe dropped the series the client was about to paint.
	// Caps are sized for that fan-out; overflow evicts the oldest entries only.
	embyVirtualSeriesCap  = 8000
	embyVirtualSeasonCap  = 16000
	embyVirtualArtworkCap = 24000
	// Clients that already cached the 1x1 placeholder treat a stable tag as
	// immutable. Virtual ids are the ones that served that placeholder, so
	// only those tags get a suffix that forces a refetch.
	embyVirtualPrimaryTagSuffix  = "-p2"
	embyVirtualBackdropTagSuffix = "-bd2"
)

var (
	embySeasonDirRE    = regexp.MustCompile(`(?i)^(season[\s._-]*\d+|s\d+|specials?|sp|ovas?|oads?|ovds?|onas?|extras?|bonus(?:es)?|omake|picture[\s._-]*drama|ncop|nced|第\s*[0-9一二三四五六七八九十百零两]+\s*季|特别篇|特別篇|番外|特典|画像特典)$`)
	embySeasonSuffixRE = regexp.MustCompile(`(?i)(?:[\s._-]+(?:season[\s._-]*\d+|s\d+|第\s*[0-9一二三四五六七八九十百零两]+\s*季|specials?|sp|ovas?|oads?|ovds?|onas?|extras?|bonus(?:es)?|omake|picture[\s._-]*drama|ncop|nced|特别篇|特別篇|番外|特典|画像特典)|\s*第\s*[0-9一二三四五六七八九十百零两]+\s*季)\s*$`)
	embyYearSuffixRE   = regexp.MustCompile(`\s*[\(（\[]\d{4}[\)）\]]\s*$`)
	embyEpisodeTitleRE = regexp.MustCompile(`(?i)\s*[-_ ]*s\d{1,2}e\d{1,3}.*$`)
)

type embyVisibilityCacheEntry struct {
	visibility MediaVisibility
	expiresAt  time.Time
}

// embyPeopleCacheEntry avoids re-statting/decoding the same NFO for every
// list refresh. TV clients commonly request the same posters/items repeatedly.
type embyPeopleCacheEntry struct {
	people    []map[string]any
	expiresAt time.Time
}

// Items paginates media in Emby's hierarchy. Episodic libraries are exposed as
// Series -> Season -> Episode so Infuse/Vidhub/SenPlayer stop treating every
// episode as a separate movie card. 带 embyremote~ 前缀的 ParentID / 搜索自动
// 路由到远程 Emby（联邦聚合，远程数据不落库）。
func (e *EmbyService) Items(ctx context.Context, p ItemsParams) (map[string]any, error) {
	if p.Limit <= 0 || p.Limit > 500 {
		p.Limit = 50
	}
	if p.StartIndex < 0 {
		p.StartIndex = 0
	}
	if len(p.IncludeItemTypes) > 0 && !containsSupportedEmbyItemType(p.IncludeItemTypes) {
		return emptyItemsEnvelope(p.StartIndex), nil
	}

	if e.remote != nil {
		// 远程目录浏览：ParentId 带远程前缀 → 完整转发给远程 Emby 承接分页。
		if IsEmbyRemoteID(p.ParentID) {
			if containsEmbyFilter(p.Filters, "IsFavorite") {
				return e.favoriteItems(ctx, p)
			}
			mountID, _, _ := DecodeEmbyRemoteID(p.ParentID)
			mount, acct, _ := e.remote.ResolveMount(ctx, mountID)
			if mount == nil || acct == nil {
				return emptyItemsEnvelope(p.StartIndex), nil
			}
			if !EmbyMountLibraryAllowed(e.mediaVisibility(ctx, p.UserID), mount) {
				return emptyItemsEnvelope(p.StartIndex), nil
			}
			out, err := e.remote.RemoteItems(ctx, mount, acct, p)
			if err != nil {
				return nil, err
			}
			if err := e.mergeRemoteUserData(ctx, p.UserID, out); err != nil {
				return nil, err
			}
			return out, nil
		}
		// 全局搜索：无 ParentId 且带搜索词 → 聚合本地 + 全部远程。
		if p.ParentID == "" && p.SearchTerm != "" {
			return e.aggregatedSearch(ctx, p)
		}
	}

	if containsEmbyFilter(p.Filters, "IsResumable") {
		return e.resumableItems(ctx, p)
	}
	if containsEmbyFilter(p.Filters, "IsFavorite") {
		return e.favoriteItems(ctx, p)
	}

	if len(p.IDs) > 0 {
		items := make([]map[string]any, 0, len(p.IDs))
		for _, id := range p.IDs {
			item, err := e.Item(ctx, id, p.UserID)
			if err != nil {
				return nil, err
			}
			if item != nil {
				items = append(items, item)
			}
		}
		return map[string]any{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0}, nil
	}

	if containsOnlyFolderItemTypes(p.IncludeItemTypes) {
		if p.ParentID == "" {
			return e.Views(ctx, p.UserID)
		}
		if episodic, err := e.libraryIsEpisodic(ctx, p.ParentID); err != nil {
			return nil, err
		} else if episodic {
			return e.seriesItemsForLibrary(ctx, p.ParentID, p)
		}
		return map[string]any{"Items": []map[string]any{}, "TotalRecordCount": 0, "StartIndex": p.StartIndex}, nil
	}

	if p.ParentID == "" && p.SearchTerm == "" && !p.Recursive && len(p.IncludeItemTypes) == 0 && len(p.Filters) == 0 {
		return e.Views(ctx, p.UserID)
	}

	if season, ok, err := e.findSeasonGroup(ctx, p.ParentID, p.UserID); err != nil {
		return nil, err
	} else if ok {
		return e.episodeItems(ctx, season.Episodes, p)
	}

	if series, ok, err := e.findSeriesGroup(ctx, p.ParentID, p.UserID); err != nil {
		return nil, err
	} else if ok {
		if p.Recursive || containsItemType(p.IncludeItemTypes, "Episode") {
			return e.episodeItems(ctx, series.Episodes, p)
		}
		seasons := e.seasonsForSeries(series)
		items := make([]map[string]any, 0, len(seasons))
		for _, season := range pageSlice(seasons, p.StartIndex, p.Limit) {
			items = append(items, e.seasonPayload(season))
		}
		return map[string]any{"Items": items, "TotalRecordCount": len(seasons), "StartIndex": p.StartIndex}, nil
	}

	if p.ParentID != "" {
		if episodic, err := e.libraryIsEpisodic(ctx, p.ParentID); err != nil {
			return nil, err
		} else if episodic && !p.Recursive && !containsItemType(p.IncludeItemTypes, "Episode") {
			return e.seriesItemsForLibrary(ctx, p.ParentID, p)
		}
	}

	if containsItemType(p.IncludeItemTypes, "Series") && !containsItemType(p.IncludeItemTypes, "Episode") {
		return e.seriesItemsForLibrary(ctx, p.ParentID, p)
	}

	// 电影库的「常规浏览」(未指定 IncludeItemTypes): 电影库里偶尔混入按
	// Season/SxxE 结构整理的内容(如整合成剧集的剧场版 / 合集动画)。这些行若按
	// 散装单集(Episode)漏出,在 Infuse/yamby 等客户端表现为「整部剧被拆成一堆
	// 单集卡片」。方案 B: 把它们按整剧聚成 Series 卡片,与真正的电影(Movie)并列
	// 展示在同一电影库视图。仅在该电影库确实含此类内容时才走此分支,普通电影库
	// 仍走 mediaItems(保留其缓存 / 版本合并逻辑)。
	if p.ParentID != "" && !p.Recursive && len(p.IncludeItemTypes) == 0 {
		if episodic, err := e.libraryIsEpisodic(ctx, p.ParentID); err == nil && !episodic {
			if has, err := e.movieLibraryHasEpisodicContent(ctx, p.ParentID); err == nil && has {
				return e.movieLibraryItems(ctx, p)
			}
		}
	}
	return e.mediaItems(ctx, p)
}

// aggregatedSearch 把本地媒体库与全部启用的远程 Emby 的搜索结果合并为一个
// 分页载荷。本地结果保持原有分页语义，远程各自取一页（Limit 同款）后按
// SortBy 做稳定排序切片。
func (e *EmbyService) aggregatedSearch(ctx context.Context, p ItemsParams) (map[string]any, error) {
	local, err := e.mediaItems(ctx, p)
	if err != nil {
		return nil, err
	}
	type remoteReply struct {
		acct     *model.StrmAccount
		envelope map[string]any
	}
	mounts, aerr := e.remote.ListMounts(ctx)
	replies := make([]*remoteReply, 0, len(mounts))
	if aerr == nil {
		type mountSearchJob struct {
			idx   int
			mount *model.EmbyMount
			acct  *model.StrmAccount
		}
		jobs := make([]*mountSearchJob, 0, len(mounts))
		for i := range mounts {
			m := mounts[i]
			if !m.Enabled {
				continue
			}
			if !EmbyMountLibraryAllowed(e.mediaVisibility(ctx, p.UserID), &m) {
				continue
			}
			acct := e.remote.AccountByID(ctx, m.AccountID)
			if acct == nil {
				continue
			}
			// idx 使用 jobs 内的序号（而非 mounts 下标）：fetched 按
			// len(jobs) 分配，必须与 jobs 下标对齐，否则越界 panic。
			jobs = append(jobs, &mountSearchJob{idx: len(jobs), mount: &mounts[i], acct: acct})
		}
		// 并发搜索各挂载（限并发 + 单挂载超时）：串行时每挂载最多
		// 15s×线路数，多挂载下首屏延迟被成倍放大。结果按挂载顺序合并。
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		fetched := make([]*remoteReply, len(jobs))
		for _, job := range jobs {
			wg.Add(1)
			go func(job *mountSearchJob) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
				defer cancel()
				sp := p
				// 远程只取首页：此前每个远程各自按 StartIndex 分页，拼接后
				// 又被 sliceSearchItems 再切一次——分页被二次偏移，远程结果
				// 首屏不可见、翻页错位。合并后由 sliceSearchItems 单点分页。
				sp.StartIndex = 0
				sp.ParentID = "" // RemoteSearchMount 内部设 ParentId
				remote, rerr := e.remote.RemoteSearchMount(sctx, job.mount, job.acct, sp)
				if rerr != nil {
					if e.log != nil {
						e.log.Warn("remote emby search failed",
							zap.String("account", job.acct.Name), zap.Error(rerr))
					}
					return
				}
				fetched[job.idx] = &remoteReply{acct: job.acct, envelope: remote}
			}(job)
		}
		wg.Wait()
		for _, r := range fetched {
			if r != nil {
				replies = append(replies, r)
			}
		}
	}
	items := make([]any, 0, len(localItemsAsAny(local))+len(replies)*p.Limit)
	items = append(items, localItemsAsAny(local)...)
	for _, reply := range replies {
		if err := e.mergeRemoteUserData(ctx, p.UserID, reply.envelope); err != nil {
			return nil, err
		}
		items = append(items, remoteItemsAsAny(reply.envelope)...)
	}
	return sliceSearchItems(items, p), nil
}

// remoteItemsAsAny 提取远程载荷的 Items 列表（兼容 []any 与 []map 形态）。
func remoteItemsAsAny(envelope map[string]any) []any {
	if envelope == nil {
		return nil
	}
	if raw, ok := envelope["Items"].([]any); ok {
		return raw
	}
	if rawMap, ok := envelope["Items"].([]map[string]any); ok {
		converted := make([]any, 0, len(rawMap))
		for _, m := range rawMap {
			converted = append(converted, any(m))
		}
		return converted
	}
	return nil
}

func localItemsAsAny(envelope map[string]any) []any {
	if envelope == nil {
		return nil
	}
	if raw, ok := envelope["Items"].([]any); ok {
		return raw
	}
	if raw, ok := envelope["Items"].([]map[string]any); ok {
		converted := make([]any, 0, len(raw))
		for _, m := range raw {
			converted = append(converted, any(m))
		}
		return converted
	}
	return nil
}

// sliceSearchItems 对合并结果按请求排序做简单归类后分页。远程返回已按远程
// 排序规则排好，这里保持稳定顺序，只做首/尾切片，避免过度重排造成分页跳动。
func sliceSearchItems(items []any, p ItemsParams) map[string]any {
	total := len(items)
	if p.StartIndex >= total {
		return map[string]any{"Items": []any{}, "TotalRecordCount": total, "StartIndex": p.StartIndex}
	}
	end := minInt(p.StartIndex+p.Limit, total)
	return map[string]any{"Items": items[p.StartIndex:end], "TotalRecordCount": total, "StartIndex": p.StartIndex}
}
