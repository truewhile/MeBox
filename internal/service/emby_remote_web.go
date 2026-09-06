// 网页端远程 Emby 库映射。
//
// 网页端（React UI）的媒体库/媒体浏览走项目自有 REST API（/api/libraries、
// /api/libraries/:id/media、/api/media/:id 等），数据结构为 model.Library /
// model.Media / SeriesCard。远程 Emby 挂载的数据不落库，因此这里把远程
// Emby 的 JSON item 映射为与本地完全一致的结构，让网页端无感知地浏览
// 远程库；播放统一走 /api/stream/{伪装ID}（302 到远程 Emby 原地址）。
package service

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
)

// RemoteLibraryView 是一个网页端可见的远程媒体库（对应一个挂载的远程 View）。
type RemoteLibraryView struct {
	Library        model.Library
	MountID        string
	AccountID      string
	RemoteID       string
	CollectionType string
	AccountName    string
}

// RemoteLibraries 把所有启用挂载的远程媒体库映射为网页媒体库列表
// （只有显式挂载的库才出现在本项目媒体库中）。
func (r *EmbyRemoteService) RemoteLibraries(ctx context.Context) ([]RemoteLibraryView, error) {
	mounts, err := r.ListMounts(ctx)
	if err != nil || len(mounts) == 0 {
		return nil, err
	}
	type accountData struct {
		acct       *model.StrmAccount
		cfg        *EmbyRemoteConfig
		viewByName map[string]map[string]any
	}
	acctData := map[string]*accountData{}
	for i := range mounts {
		m := &mounts[i]
		if !m.Enabled {
			continue
		}
		if _, ok := acctData[m.AccountID]; ok {
			continue
		}
		acct := r.AccountByID(ctx, m.AccountID)
		if acct == nil {
			acctData[m.AccountID] = nil
			continue
		}
		cfg, cfgErr := r.remoteConfigWithToken(ctx, acct)
		if cfgErr != nil {
			acctData[m.AccountID] = nil
			continue
		}
		views, viewErr := r.RemoteViews(ctx, acct)
		if viewErr != nil {
			if r.log != nil {
				r.log.Warn("web remote emby views failed",
					zap.String("account", acct.Name), zap.Error(viewErr))
			}
			acctData[m.AccountID] = nil
			continue
		}
		viewByName := map[string]map[string]any{}
		for _, v := range views {
			viewByName[remoteItemString(v, "Id")] = v
		}
		acctData[m.AccountID] = &accountData{
			acct:       acct,
			cfg:        cfg,
			viewByName: viewByName,
		}
	}

	out := make([]RemoteLibraryView, 0, len(mounts))
	for i := range mounts {
		m := &mounts[i]
		if !m.Enabled {
			continue
		}
		data := acctData[m.AccountID]
		if data == nil || data.viewByName == nil {
			continue
		}
		v, ok := data.viewByName[m.RemoteViewID]
		if !ok {
			continue // 远程已删除该媒体库
		}
		lib := r.mapRemoteMountToLibrary(m, data.acct, data.cfg, v)
		if lib == nil {
			continue
		}
		out = append(out, RemoteLibraryView{
			Library:        *lib,
			MountID:        m.ID,
			AccountID:      data.acct.ID,
			RemoteID:       m.RemoteViewID,
			CollectionType: m.CollectionType,
			AccountName:    data.acct.Name,
		})
	}
	return out, nil
}

// RemoteLibraryByID 按伪装 ID 查远程库视图（详情接口用）。
func (r *EmbyRemoteService) RemoteLibraryByID(ctx context.Context, mountID, remoteViewID string) (*RemoteLibraryView, error) {
	views, err := r.RemoteLibraries(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range views {
		if v.MountID == mountID && v.RemoteID == remoteViewID {
			cp := v
			return &cp, nil
		}
	}
	return nil, nil
}

// mapRemoteMountToLibrary 把挂载信息 + 远程 View item 映射为网页库结构。
func (r *EmbyRemoteService) mapRemoteMountToLibrary(mount *model.EmbyMount, acct *model.StrmAccount, cfg *EmbyRemoteConfig, item map[string]any) *model.Library {
	if mount == nil {
		return nil
	}
	name := strings.TrimSpace(mount.Name)
	if name == "" {
		name = strings.TrimSpace(remoteItemString(item, "Name"))
	}
	if name == "" {
		name = acct.Name
	} else if !strings.Contains(name, acct.Name) {
		name = acct.Name + " · " + name
	}
	libType := "movie"
	switch mount.CollectionType {
	case "tvshows":
		libType = "tv"
	case "music":
		libType = "music"
	}
	lib := &model.Library{
		Base:      model.Base{ID: EncodeEmbyRemoteID(mount.ID, mount.RemoteViewID)},
		Name:      name,
		Type:      libType,
		Enabled:   true,
		SortOrder: 1000 + mount.SortOrder, // 远程库排在本地库之后，且保持挂载库排序
	}
	// 远程媒体库封面只有真实存在图片标签才下发。
	if remoteItemHasImageTag(item, "Primary") {
		lib.CoverURL = r.remoteItemImageURL(cfg, mount.RemoteViewID, "Primary")
	}
	return lib
}

// MapRemoteItemToMedia 把远程 Emby item JSON 映射为本地 Media 结构。
// poster/backdrop 只有在远程确实存在图片标签时才填 URL（避免对无图条目
// 发出必失败的图片请求导致前端破图）；剧集回退到系列海报（SeriesPrimaryImage）。
func (r *EmbyRemoteService) MapRemoteItemToMedia(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, cfg *EmbyRemoteConfig, item map[string]any) model.Media {
	encodeScope := acct.ID
	if mount != nil {
		encodeScope = mount.ID
	}
	remoteID := remoteItemString(item, "Id")
	// 条目可能已被 RewriteEmbyRemoteIDs 伪装（图片/嵌套 ID 需要原始远程 ID）。
	if _, rid, ok := DecodeEmbyRemoteID(remoteID); ok {
		remoteID = rid
	}
	seriesID := remoteItemString(item, "SeriesId")
	if _, rid, ok := DecodeEmbyRemoteID(seriesID); ok {
		seriesID = rid
	}
		rating := remoteItemFloat(item, "CommunityRating")
		if rating == 0 {
			rating = remoteItemFloat(item, "CriticRating")
		}
		year := remoteItemInt(item, "ProductionYear")
		if year == 0 {
			year = remoteItemInt(item, "Year")
		}
		if year == 0 {
			year = remoteItemInt(item, "SeriesProductionYear")
		}
		if year == 0 {
			year = remoteItemInt(item, "SeriesYear")
		}
		media := model.Media{
			Base:         model.Base{ID: EncodeEmbyRemoteID(encodeScope, remoteID)},
			Title:        remoteItemString(item, "Name"),
			OriginalName: remoteItemString(item, "OriginalTitle"),
			Overview:     remoteItemString(item, "Overview"),
			Year:         year,
			Rating:       float32(rating),
			Path:         remoteItemString(item, "Path"),
			Genres:       remoteItemGenres(item),
			ScrapeStatus: "done",
		}
	if date, ok := parseEmbyRemoteDate(remoteItemString(item, "DateCreated")); ok {
		media.CreatedAt = date
		media.UpdatedAt = date
	}
	if date, ok := parseEmbyRemoteDate(remoteItemString(item, "DateLastMediaAdded")); ok {
		media.UpdatedAt = date
	}
	// 只有远程明确存在图片标签才下发图片 URL。
	if remoteItemHasImageTag(item, "Primary") {
		media.PosterURL = r.remoteItemImageURL(cfg, remoteID, "Primary")
	}
	if remoteItemHasImageTag(item, "Backdrop") || len(remoteBackdropTags(item)) > 0 {
		media.BackdropURL = r.remoteItemImageURL(cfg, remoteID, "Backdrop")
	}
	if ticks := remoteItemInt64(item, "RunTimeTicks"); ticks > 0 {
		media.DurationSec = int(ticks / 10_000_000)
	}
	if date, ok := parseEmbyRemoteDate(remoteItemString(item, "PremiereDate")); ok {
		media.ReleaseDate = date.Format("2006-01-02")
		if media.Year == 0 {
			media.Year = date.Year()
		}
	} else if date, ok := embyPremiereDate(remoteItemString(item, "PremiereDate")); ok {
		media.ReleaseDate = date.Format("2006-01-02")
		if media.Year == 0 {
			media.Year = date.Year()
		}
	}
	if providerIDs, ok := item["ProviderIds"].(map[string]any); ok {
		if v := anyString(providerIDs["Tmdb"]); v != "" {
			media.TMDbID, _ = strconv.Atoi(v)
		}
		if v := anyString(providerIDs["Imdb"]); v != "" {
			media.TheTVDBID = v
		}
		if v := anyString(providerIDs["Douban"]); v != "" {
			media.DoubanID = v
		}
	}
	media.Container = remoteItemString(item, "Container")
	media.Width = remoteItemInt(item, "Width")
	media.Height = remoteItemInt(item, "Height")

	extractStreamInfo := func(streams []any) {
		for _, s := range streams {
			sm, ok := s.(map[string]any)
			if !ok {
				continue
			}
			typ := remoteItemString(sm, "Type")
			if strings.EqualFold(typ, "Video") {
				if media.VideoCodec == "" {
					media.VideoCodec = remoteItemString(sm, "Codec")
				}
				if media.Width == 0 {
					media.Width = remoteItemInt(sm, "Width")
				}
				if media.Height == 0 {
					media.Height = remoteItemInt(sm, "Height")
				}
			} else if strings.EqualFold(typ, "Audio") {
				if media.AudioCodec == "" {
					media.AudioCodec = remoteItemString(sm, "Codec")
				}
			}
		}
	}

	if streams, ok := item["MediaStreams"].([]any); ok {
		extractStreamInfo(streams)
	} else if streams, ok := item["MediaStreams"].([]map[string]any); ok {
		anyStreams := make([]any, len(streams))
		for i, v := range streams {
			anyStreams[i] = v
		}
		extractStreamInfo(anyStreams)
	}

	if sources, ok := item["MediaSources"].([]any); ok && len(sources) > 0 {
		if sourceMap, ok := sources[0].(map[string]any); ok {
			if media.Container == "" {
				media.Container = remoteItemString(sourceMap, "Container")
			}
			if media.SizeBytes == 0 {
				media.SizeBytes = remoteItemInt64(sourceMap, "Size")
			}
			if streams, ok := sourceMap["MediaStreams"].([]any); ok && (media.VideoCodec == "" || media.AudioCodec == "") {
				extractStreamInfo(streams)
			}
		}
	}

	switch remoteItemString(item, "Type") {
	case "Episode":
		media.SeasonNum = remoteItemInt(item, "ParentIndexNumber")
		media.EpisodeNum = remoteItemInt(item, "IndexNumber")
		media.EpisodeTitle = remoteItemString(item, "Name")
		if seriesName := remoteItemString(item, "SeriesName"); seriesName != "" {
			media.Title = seriesName
		}
		// 单集通常无独立海报：若远程返回 SeriesPrimaryImageTag（需要
		// Fields=SeriesPrimaryImage）且系列有图，则回退到系列海报。
		if media.PosterURL == "" && seriesID != "" &&
			strings.TrimSpace(remoteItemString(item, "SeriesPrimaryImageTag")) != "" {
			media.PosterURL = r.remoteItemImageURL(cfg, seriesID, "Primary")
		}
	default: // Movie / Series / Season / Folder
		media.SeasonNum = 0
		media.EpisodeNum = 0
	}
	if mount != nil && strings.TrimSpace(mount.RemoteViewID) != "" {
		libID := EncodeEmbyRemoteID(mount.ID, mount.RemoteViewID)
		media.DisplayLibraryID = libID
		media.LibraryID = libID
		libName := strings.TrimSpace(mount.Name)
		if libName == "" {
			libName = strings.TrimSpace(mount.RemoteViewName)
		}
		if libName == "" && acct != nil {
			libName = acct.Name
		} else if acct != nil && acct.Name != "" && !strings.Contains(libName, acct.Name) {
			libName = acct.Name + " · " + libName
		}
		media.LibraryName = libName
		media.DisplayLibraryName = libName
	}
	return media
}

// RemoteLibraryMedia 拉远程库直属条目（电影库=Movie，剧集库=Series），映射分页。
func (r *EmbyRemoteService) RemoteLibraryMedia(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, remoteViewID string, itemTypes string, offset, limit int) ([]model.Media, int64, error) {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return nil, 0, err
	}
	if itemTypes == "" {
		itemTypes = "Movie,Series" // 未知类型时两者都取（前端自行按 episode-like 分组）
	}
	cacheKey := r.remoteCacheKey("library-media", acct.ID, mount.ID, remoteViewID, itemTypes, strconv.Itoa(offset), strconv.Itoa(limit))
	var cached struct {
		Items            []model.Media `json:"items"`
		TotalRecordCount int64         `json:"total_record_count"`
	}
	if r.cache != nil && r.cache.GetJSON(ctx, cacheKey, &cached) {
		return cached.Items, cached.TotalRecordCount, nil
	}
	q := url.Values{}
	q.Set("ParentId", remoteViewID)
	q.Set("IncludeItemTypes", itemTypes)
	q.Set("Recursive", "false")
	q.Set("StartIndex", strconv.Itoa(offset))
	q.Set("Limit", strconv.Itoa(limit))
	q.Set("Fields", "Overview,Genres,ProviderIds,Path,SeriesPrimaryImage,MediaStreams,MediaSources,DateCreated,PremiereDate,ProductionYear,CommunityRating,CriticRating")
	var body struct {
		Items            []map[string]any `json:"Items"`
		TotalRecordCount int64            `json:"TotalRecordCount"`
	}
	if err := r.doGet(ctx, acct, cfg, "/Users/"+url.PathEscape(r.remoteUserID(cfg))+"/Items", q, &body); err != nil {
		return nil, 0, err
	}
	items := make([]model.Media, 0, len(body.Items))
	for _, it := range body.Items {
		RewriteEmbyRemoteIDs(it, mount.ID) // 嵌套/关联 ID 一并伪装
		items = append(items, r.MapRemoteItemToMedia(ctx, mount, acct, cfg, it))
	}
	if r.cache != nil {
		r.cache.SetJSON(ctx, cacheKey, struct {
			Items            []model.Media `json:"items"`
			TotalRecordCount int64         `json:"total_record_count"`
		}{Items: items, TotalRecordCount: body.TotalRecordCount}, r.remoteMediaCacheTTL())
	}
	return items, body.TotalRecordCount, nil
}

// RemoteMediaDetail 拉远程单条目映射为 Media（网页详情页）。
func (r *EmbyRemoteService) RemoteMediaDetail(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, remoteID string) (*model.Media, error) {
	m, _, err := r.remoteMediaDetailRaw(ctx, mount, acct, remoteID)
	return m, err
}

// remoteMediaDetailRaw 拉取远程条目详情，同时返回原始载荷（ID 已伪装），
// 供调用方免二次请求读取 Type / SeriesId 等字段。
func (r *EmbyRemoteService) remoteMediaDetailRaw(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, remoteID string) (*model.Media, map[string]any, error) {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return nil, nil, err
	}
	path := "/Users/" + url.PathEscape(r.remoteUserID(cfg)) + "/Items/" + url.PathEscape(remoteID)
	path += "?Fields=Overview,Genres,ProviderIds,People,Studios,Path,MediaStreams,MediaSources,DateCreated,PremiereDate,ProductionYear,CommunityRating,CriticRating"
	var out map[string]any
	if err := r.doGet(ctx, acct, cfg, path, nil, &out); err != nil {
		return nil, nil, err
	}
	RewriteEmbyRemoteIDs(out, mount.ID)
	m := r.MapRemoteItemToMedia(ctx, mount, acct, cfg, out)
	return &m, out, nil
}

// RemoteEpisodes 拉远程条目下的集列表（Series/Season/Folder→子集；Episode→同系列；
// Movie→自身单条），按季/集排序，与本地 ListMediaEpisodes 行为一致。
func (r *EmbyRemoteService) RemoteEpisodes(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, remoteID string) ([]model.Media, error) {
	detail, rawDetail, err := r.remoteMediaDetailRaw(ctx, mount, acct, remoteID)
	if err != nil {
		return nil, err
	}
	// 用远程详情载荷精判类型（Episode→同系列；Series/Season/Folder→子集；Movie→单条）。
	// Type/SeriesId 都在详情载荷里现成可用，不再为判定类型/系列额外发起
	// 两次重复的远程全量 GET（远程慢时页面延迟直接×3）。
	itemType := remoteItemString(rawDetail, "Type")
	if itemType == "" {
		itemType = remoteItemTypeOf(detail)
	}
	var parentID string
	switch itemType {
	case "Episode":
		parentID = remoteItemString(rawDetail, "SeriesId")
		if _, rid, ok := DecodeEmbyRemoteID(parentID); ok {
			parentID = rid // 载荷 ID 已伪装，远程查询需要原始 ID
		}
		if parentID == "" {
			parentID = r.remoteItemSeriesID(ctx, acct, remoteID)
		}
		if parentID == "" {
			parentID = remoteID
		}
	case "Season", "Folder", "Series":
		parentID = remoteID
	default: // Movie
		return []model.Media{*detail}, nil
	}
	rows, _, err := r.remoteEpisodesOf(ctx, mount, acct, parentID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].SeasonNum != rows[j].SeasonNum {
			return rows[i].SeasonNum < rows[j].SeasonNum
		}
		if rows[i].EpisodeNum != rows[j].EpisodeNum {
			return rows[i].EpisodeNum < rows[j].EpisodeNum
		}
		return rows[i].Title < rows[j].Title
	})
	return rows, nil
}

func (r *EmbyRemoteService) remoteEpisodesOf(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, parentID string) ([]model.Media, int64, error) {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return nil, 0, err
	}
	q := url.Values{}
	q.Set("ParentId", parentID)
	q.Set("IncludeItemTypes", "Episode")
	q.Set("Recursive", "true")
	q.Set("Fields", "Overview,Genres,ProviderIds,Path,SeriesPrimaryImage,MediaStreams,MediaSources,DateCreated,PremiereDate,ProductionYear,CommunityRating,CriticRating")
	items := make([]model.Media, 0, 64)
	total := int64(0)
	// 每页 200 循环拉全：MediaStreams/MediaSources 重字段下单页 500 条
	// 已贴近 8MB 截断上限；单次大页超限会静默解析失败。
	const episodePageSize = 200
	for startIndex := 0; ; startIndex += episodePageSize {
		q.Set("StartIndex", strconv.Itoa(startIndex))
		q.Set("Limit", strconv.Itoa(episodePageSize))
		var body struct {
			Items            []map[string]any `json:"Items"`
			TotalRecordCount int64            `json:"TotalRecordCount"`
		}
		if err := r.doGet(ctx, acct, cfg, "/Users/"+url.PathEscape(r.remoteUserID(cfg))+"/Items", q, &body); err != nil {
			return nil, 0, err
		}
		total = body.TotalRecordCount
		if len(body.Items) == 0 {
			break
		}
		for _, it := range body.Items {
			RewriteEmbyRemoteIDs(it, mount.ID)
			m := r.MapRemoteItemToMedia(ctx, mount, acct, cfg, it)
			items = append(items, m)
		}
		if len(body.Items) < episodePageSize {
			break
		}
	}
	return items, total, nil
}

// RemoteSeriesCards 远程剧集库的系列卡片（ChildCount 作为集数）。
//
// 远程 Emby 的 Series DTO 不会返回 DateLastMediaAdded 字段（即使请求 Fields
// 也缺失），但其服务端排序支持 SortBy=DateLastContentAdded——即客户端"上次
// 添加集日期"排序。因此这里直接按该键倒序分页拉全量，返回的卡片顺序与对方
// Emby 客户端选择"上次添加集日期"完全一致；LastAddedAt 在远程提供字段时
// 才填充，否则保持 nil（前端对无该值的卡片维持服务器顺序，不再回退加入日期）。
func (r *EmbyRemoteService) RemoteSeriesCards(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, remoteViewID string) ([]SeriesCard, error) {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return nil, err
	}
	cacheKey := r.remoteCacheKey("series-cards", acct.ID, mount.ID, remoteViewID)
	var cached []SeriesCard
	if r.cache != nil && r.cache.GetJSON(ctx, cacheKey, &cached) {
		return cached, nil
	}
	q := url.Values{}
	q.Set("ParentId", remoteViewID)
	q.Set("IncludeItemTypes", "Series")
	q.Set("Recursive", "false")
	q.Set("SortBy", "DateLastContentAdded")
	q.Set("SortOrder", "Descending")
	// 每页 200：Fields 带全量重字段（Overview/MediaStreams 等）时单页 1000
	// 条的载荷会超过 doGet 的 8MB 截断上限，JSON 被静默截断直接解析失败。
	q.Set("Limit", "200")
	q.Set("Fields", "Overview,Genres,ProviderIds,Path,RecursiveItemCount,SeriesPrimaryImage,DateCreated,DateLastMediaAdded,PremiereDate,ProductionYear,CommunityRating,CriticRating")
	var body struct {
		Items            []map[string]any `json:"Items"`
		TotalRecordCount int64            `json:"TotalRecordCount"`
	}
	cards := make([]SeriesCard, 0)
	for startIndex := 0; ; startIndex += 200 {
		q.Set("StartIndex", strconv.Itoa(startIndex))
		body.Items = nil
		if err := r.doGet(ctx, acct, cfg, "/Users/"+url.PathEscape(r.remoteUserID(cfg))+"/Items", q, &body); err != nil {
			return nil, err
		}
		if len(body.Items) == 0 {
			break
		}
		for _, it := range body.Items {
			RewriteEmbyRemoteIDs(it, mount.ID)
			m := r.MapRemoteItemToMedia(ctx, mount, acct, cfg, it)
			// 集数优先用递归条目数（ChildCount 只算直属 Season 文件夹数）。
			count := remoteItemInt(it, "RecursiveItemCount")
			if count == 0 {
				count = remoteItemInt(it, "ChildCount")
			}
			if count == 0 {
				count = 1
			}
			var lastAdded *time.Time
			if date, ok := parseEmbyRemoteDate(remoteItemString(it, "DateLastMediaAdded")); ok {
				lastAdded = &date
			}
			cards = append(cards, SeriesCard{Key: m.ID, Rep: m, LinkMedia: m, Count: count, LastAddedAt: lastAdded})
		}
		if int64(len(cards)) >= body.TotalRecordCount || len(body.Items) < 1000 {
			break
		}
	}
	if r.cache != nil {
		r.cache.SetJSON(ctx, cacheKey, cards, r.remoteMediaCacheTTL())
	}
	return cards, nil
}

// RemoteLatestCards 远程库最新条目（首页预览卡片），映射 SeriesCard。
func (r *EmbyRemoteService) RemoteLatestCards(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, remoteViewID string, limit int) ([]SeriesCard, error) {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return nil, err
	}
	cacheKey := r.remoteCacheKey("latest-cards", acct.ID, mount.ID, remoteViewID, strconv.Itoa(limit))
	var cached []SeriesCard
	if r.cache != nil && r.cache.GetJSON(ctx, cacheKey, &cached) {
		return cached, nil
	}

	// 剧集类媒体库：直接拉取最新入库/更新的 Series 剧集本身（按上次添加集日期倒序）。
	// 避免 Emby /Items/Latest 默认返回无年份/无系列海报的单集（Episode）。
	if mount != nil && (mount.CollectionType == "tvshows" || mount.CollectionType == "tv") {
		q := url.Values{}
		q.Set("ParentId", remoteViewID)
		q.Set("IncludeItemTypes", "Series")
		q.Set("Recursive", "false")
		q.Set("SortBy", "DateLastContentAdded")
		q.Set("SortOrder", "Descending")
		q.Set("Limit", strconv.Itoa(limit))
		q.Set("Fields", "Overview,Genres,ProviderIds,Path,RecursiveItemCount,SeriesPrimaryImage,DateCreated,DateLastMediaAdded,PremiereDate,ProductionYear,CommunityRating,CriticRating")
		var body struct {
			Items []map[string]any `json:"Items"`
		}
		if err := r.doGet(ctx, acct, cfg, "/Users/"+url.PathEscape(r.remoteUserID(cfg))+"/Items", q, &body); err == nil && len(body.Items) > 0 {
			cards := make([]SeriesCard, 0, len(body.Items))
			for _, it := range body.Items {
				RewriteEmbyRemoteIDs(it, mount.ID)
				m := r.MapRemoteItemToMedia(ctx, mount, acct, cfg, it)
				count := remoteItemInt(it, "RecursiveItemCount")
				if count == 0 {
					count = remoteItemInt(it, "ChildCount")
				}
				if count == 0 {
					count = 1
				}
				var lastAdded *time.Time
				if date, ok := parseEmbyRemoteDate(remoteItemString(it, "DateLastMediaAdded")); ok {
					lastAdded = &date
				}
				cards = append(cards, SeriesCard{Key: m.ID, Rep: m, LinkMedia: m, Count: count, LastAddedAt: lastAdded})
			}
			if r.cache != nil {
				r.cache.SetJSON(ctx, cacheKey, cards, r.remoteMediaCacheTTL())
			}
			return cards, nil
		}
	}

	items, err := r.RemoteLatest(ctx, mount, acct, remoteViewID, limit)
	if err != nil {
		return nil, err
	}
	cards := make([]SeriesCard, 0, len(items))
	for _, it := range items {
		m := r.MapRemoteItemToMedia(ctx, mount, acct, cfg, it)
		var lastAdded *time.Time
		if !m.UpdatedAt.IsZero() {
			t := m.UpdatedAt
			lastAdded = &t
		} else if !m.CreatedAt.IsZero() {
			t := m.CreatedAt
			lastAdded = &t
		}
		count := remoteItemInt(it, "RecursiveItemCount")
		if count == 0 {
			count = remoteItemInt(it, "ChildCount")
		}
		cards = append(cards, SeriesCard{Key: m.ID, Rep: m, LinkMedia: m, Count: count, LastAddedAt: lastAdded})
	}
	if r.cache != nil {
		r.cache.SetJSON(ctx, cacheKey, cards, r.remoteMediaCacheTTL())
	}
	return cards, nil
}

// RemoteSearchMedia 在全部启用的挂载库中并发搜索影视条目（Movie,Series），
// 并将远程结果映射为 model.Media。遵循当前用户的 MediaVisibility 权限规则。
func (r *EmbyRemoteService) RemoteSearchMedia(ctx context.Context, query string, limit int, visibility MediaVisibility) ([]model.Media, error) {
	if r == nil {
		return nil, nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	} else if limit > maxMediaSearchLimit {
		limit = maxMediaSearchLimit
	}

	mounts, err := r.ListMounts(ctx)
	if err != nil || len(mounts) == 0 {
		return nil, err
	}

	type mountTarget struct {
		mount model.EmbyMount
		acct  *model.StrmAccount
		cfg   *EmbyRemoteConfig
	}
	var targets []mountTarget
	for _, m := range mounts {
		if !m.Enabled {
			continue
		}
		libID := EncodeEmbyRemoteID(m.ID, m.RemoteViewID)
		hidden := false
		for _, hid := range visibility.HiddenLibraryIDs {
			if hid == libID {
				hidden = true
				break
			}
		}
		if hidden {
			continue
		}
		if len(visibility.AllowedLibraryIDs) > 0 {
			allowed := false
			for _, aid := range visibility.AllowedLibraryIDs {
				if aid == libID {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}

		acct := r.AccountByID(ctx, m.AccountID)
		if acct == nil {
			continue
		}
		cfg, cfgErr := r.remoteConfigWithToken(ctx, acct)
		if cfgErr != nil {
			continue
		}
		targets = append(targets, mountTarget{mount: m, acct: acct, cfg: cfg})
	}
	if len(targets) == 0 {
		return nil, nil
	}

	searchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	type searchResult struct {
		items []model.Media
	}
	results := make([]searchResult, len(targets))
	for i, t := range targets {
		wg.Add(1)
		go func(idx int, target mountTarget) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-searchCtx.Done():
				return
			}

			helper.Run(r.log, "emby.remoteSearch", func() {
				q := url.Values{}
				q.Set("ParentId", target.mount.RemoteViewID)
				q.Set("Recursive", "true")
				q.Set("SearchTerm", query)
				q.Set("IncludeItemTypes", "Movie,Series")
				q.Set("Fields", "Overview,Genres,ProviderIds,Path,SeriesPrimaryImage,MediaStreams,MediaSources,DateCreated,DateLastMediaAdded,PremiereDate,ProductionYear,CommunityRating,CriticRating")
				q.Set("Limit", strconv.Itoa(limit))
				q.Set("StartIndex", "0")

				var body struct {
					Items []map[string]any `json:"Items"`
				}
				if err := r.doGet(searchCtx, target.acct, target.cfg, "/Users/"+url.PathEscape(r.remoteUserID(target.cfg))+"/Items", q, &body); err != nil {
					if r.log != nil {
						r.log.Warn("remote search failed",
							zap.String("mount", target.mount.RemoteViewName), zap.Error(err))
					}
					return
				}
				medias := make([]model.Media, 0, len(body.Items))
				for _, it := range body.Items {
					RewriteEmbyRemoteIDs(it, target.mount.ID)
					m := r.MapRemoteItemToMedia(searchCtx, &target.mount, target.acct, target.cfg, it)
					medias = append(medias, m)
				}
				results[idx] = searchResult{items: medias}
			})
		}(i, t)
	}
	wg.Wait()

	var out []model.Media
	for _, res := range results {
		out = append(out, res.items...)
	}
	return out, nil
}

// WebStreamURL 远程条目的网页播放地址（302 直连远程 Emby 流端点）。
func (r *EmbyRemoteService) WebStreamURL(ctx context.Context, acct *model.StrmAccount, remoteID string) (string, error) {
	cfg, err := r.configOf(acct)
	if err != nil {
		return "", err
	}
	if err := r.ensureToken(ctx, acct, cfg); err != nil {
		return "", err
	}
	return r.embyBase(cfg) + "/Videos/" + url.PathEscape(remoteID) +
		"/stream?api_key=" + url.QueryEscape(cfg.Token) + "&Static=true", nil
}

// remoteItemType 轻量查询远程条目 Type（避免依赖映射载荷）。
func (r *EmbyRemoteService) remoteItemType(ctx context.Context, acct *model.StrmAccount, remoteID string) string {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return ""
	}
	var out map[string]any
	if err := r.doGet(ctx, acct, cfg, "/Users/"+url.PathEscape(r.remoteUserID(cfg))+"/Items/"+url.PathEscape(remoteID), nil, &out); err != nil {
		return ""
	}
	return remoteItemString(out, "Type")
}

// remoteItemSeriesID 轻量查询 Episode 的 SeriesId。
func (r *EmbyRemoteService) remoteItemSeriesID(ctx context.Context, acct *model.StrmAccount, remoteID string) string {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return ""
	}
	var out map[string]any
	if err := r.doGet(ctx, acct, cfg, "/Users/"+url.PathEscape(r.remoteUserID(cfg))+"/Items/"+url.PathEscape(remoteID), nil, &out); err != nil {
		return ""
	}
	return remoteItemString(out, "SeriesId")
}

// ─── 远程 item JSON 取值辅助 ────────────────────────────────────────────────

func anyString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func remoteItemString(item map[string]any, key string) string {
	if item == nil {
		return ""
	}
	if s, ok := item[key].(string); ok {
		return s
	}
	return ""
}

func remoteItemInt(item map[string]any, key string) int {
	if item == nil {
		return 0
	}
	switch v := item[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

func remoteItemInt64(item map[string]any, key string) int64 {
	if item == nil {
		return 0
	}
	switch v := item[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

func remoteItemFloat(item map[string]any, key string) float64 {
	if item == nil {
		return 0
	}
	switch v := item[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

// remoteItemGenres 合并 GenreItems / Genres 数组为逗号分隔字符串（前端 parseCSV 消费）。
func remoteItemGenres(item map[string]any) string {
	seen := map[string]bool{}
	var parts []string
	collect := func(arr any) {
		list, ok := arr.([]any)
		if !ok {
			return
		}
		for _, it := range list {
			var name string
			if m, isMap := it.(map[string]any); isMap {
				name = remoteItemString(m, "Name")
			} else if s, isStr := it.(string); isStr {
				name = s
			}
			if name != "" && !seen[name] {
				seen[name] = true
				parts = append(parts, name)
			}
		}
	}
	collect(item["GenreItems"])
	collect(item["Genres"])
	return strings.Join(parts, ",")
}

// remoteItemImageURL 构造远程条目图片绝对地址（带 api_key；前端经 /api/img 代理）。
func (r *EmbyRemoteService) remoteItemImageURL(cfg *EmbyRemoteConfig, remoteID, imageType string) string {
	if remoteID == "" {
		return ""
	}
	imageType = strings.ToLower(imageType)
	if imageType == "" {
		imageType = "primary"
	}
	return r.embyBase(cfg) + "/Items/" + url.PathEscape(remoteID) + "/Images/" + url.PathEscape(imageType) +
		"?api_key=" + url.QueryEscape(cfg.Token)
}

// remoteItemHasImageTag 远程 item 是否带某类型图片标签（Emby 的 ImageTags map）。
func remoteItemHasImageTag(item map[string]any, typ string) bool {
	if item == nil {
		return false
	}
	switch tags := item["ImageTags"].(type) {
	case map[string]any:
		_, ok := tags[typ]
		return ok
	case map[string]string:
		_, ok := tags[typ]
		return ok
	}
	return false
}

// remoteBackdropTags 远程 item 的 BackdropImageTags 数组。
func remoteBackdropTags(item map[string]any) []any {
	if item == nil {
		return nil
	}
	switch tags := item["BackdropImageTags"].(type) {
	case []any:
		return tags
	case []string:
		out := make([]any, 0, len(tags))
		for _, s := range tags {
			out = append(out, s)
		}
		return out
	}
	return nil
}

// remoteItemTypeOf 从映射后的 Media 推断远程类型（无详情载荷时兜底）。
func remoteItemTypeOf(m *model.Media) string {
	if m == nil {
		return "Movie"
	}
	if m.EpisodeNum > 0 || m.SeasonNum > 0 {
		return "Episode"
	}
	return "Movie"
}

// ─── 供 handler 层使用的远程 View 条目取值（导出薄封装） ──────────────────────

// RemoteItemIDString 提取远程 View 条目的 Id。
func RemoteItemIDString(item map[string]any) string { return remoteItemString(item, "Id") }

// RemoteItemNameString 提取远程 View 条目的 Name。
func RemoteItemNameString(item map[string]any) string { return remoteItemString(item, "Name") }

// RemoteItemCollectionType 提取远程 View 条目的 CollectionType。
func RemoteItemCollectionType(item map[string]any) string {
	return remoteItemString(item, "CollectionType")
}

// RemoteItemChildCount 提取远程 View 条目的 ChildCount。
func RemoteItemChildCount(item map[string]any) int { return remoteItemInt(item, "ChildCount") }

func parseEmbyRemoteDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.9999999Z",
		"2006-01-02T15:04:05.9999999",
		"2006-01-02T15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
