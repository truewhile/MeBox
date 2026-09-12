package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

// Item 单条目详情。
func (e *EmbyService) Item(ctx context.Context, mediaID, userID string) (map[string]any, error) {
	if e == nil {
		return nil, nil
	}
	// 远程 Emby 条目：不查本地库，直接向远程转发（保持远程最新元数据）。
	if e.remote != nil && IsEmbyRemoteID(mediaID) {
		mountID, remoteID, _ := DecodeEmbyRemoteID(mediaID)
		mount, acct, _ := e.remote.ResolveMount(ctx, mountID)
		if mount == nil || acct == nil {
			return nil, nil
		}
		if !EmbyMountLibraryAllowed(e.mediaVisibility(ctx, userID), mount) {
			return nil, nil
		}
		out, err := e.remote.RemoteItem(ctx, mount, acct, remoteID)
		if err != nil || out == nil {
			return out, err
		}
		if err := e.mergeRemoteUserData(ctx, userID, out); err != nil {
			return nil, err
		}
		if favorite, _ := IsUserFavorite(ctx, e.repo, userID, mediaID); favorite {
			userData, _ := out["UserData"].(map[string]any)
			if userData == nil {
				userData = map[string]any{}
				out["UserData"] = userData
			}
			userData["IsFavorite"] = true
		}
		return out, nil
	}
	if lib, err := e.repo.Library.FindByID(ctx, mediaID); err != nil {
		return nil, err
	} else if lib != nil {
		libs := FilterDisplayCloudLibraries(ctx, e.repo, []model.Library{*lib})
		if len(libs) == 0 {
			return nil, nil
		}
		visibility := e.mediaVisibility(ctx, userID)
		if !e.libraryVisibleFromCachedVisibility(libs[0], visibility) {
			return nil, nil
		}
		return e.libraryAsView(ctx, &libs[0]), nil
	}
	if strings.HasPrefix(mediaID, embyVirtualSeasonPrefix) {
		if season, ok, err := e.findSeasonGroup(ctx, mediaID, userID); err != nil {
			return nil, err
		} else if ok {
			return e.seasonPayload(season), nil
		}
	}
	if strings.HasPrefix(mediaID, embyVirtualSeriesPrefix) {
		if series, ok, err := e.findSeriesGroup(ctx, mediaID, userID); err != nil {
			return nil, err
		} else if ok {
			return e.seriesPayload(series), nil
		}
	}
	m, err := e.repo.Media.FindByID(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		if series, ok, err := e.findSeriesGroup(ctx, mediaID, userID); err != nil {
			return nil, err
		} else if ok {
			return e.seriesPayload(series), nil
		}
		return nil, nil
	}
	if !UserDefaultMediaVisibility(ctx, e.repo, userID).Allows(m) {
		return nil, nil
	}
	fav := false
	pos := int64(0)
	if userID != "" {
		var f model.Favorite
		ferr := e.repo.DB.WithContext(ctx).Where("user_id = ? AND media_id = ?", userID, mediaID).First(&f).Error
		if ferr == nil {
			fav = true
		}
		var h model.PlaybackHistory
		herr := e.repo.DB.WithContext(ctx).Where("user_id = ? AND media_id = ?", userID, mediaID).
			Order("watched_at desc").First(&h).Error
		if herr == nil {
			pos = h.PositionMs
		}
	}
	// 单条目 payload 内部对库类型/series 标题有多次查找，挂请求级缓存合并。
	return e.itemPayload(e.withPayloadCache(ctx), m, fav, pos), nil
}

// LatestItems 最近添加，全库或指定库。远程媒体库(parentID 带前缀)直接透传远程。
func (e *EmbyService) LatestItems(ctx context.Context, userID, parentID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if e.remote != nil && IsEmbyRemoteID(parentID) {
		mountID, remoteParent, _ := DecodeEmbyRemoteID(parentID)
		mount, acct, _ := e.remote.ResolveMount(ctx, mountID)
		if mount == nil || acct == nil {
			return nil, nil
		}
		if !EmbyMountLibraryAllowed(e.mediaVisibility(ctx, userID), mount) {
			return nil, nil
		}
		out, err := e.remote.RemoteLatest(ctx, mount, acct, remoteParent, limit)
		if err != nil {
			return nil, err
		}
		if err := e.mergeRemoteUserData(ctx, userID, out); err != nil {
			return nil, err
		}
		return out, nil
	}
	cacheKey := e.embyLatestCacheKey(userID, parentID, limit)
	var cached embyLatestCacheValue
	if e.cache != nil && e.cache.GetJSON(ctx, cacheKey, &cached) {
		e.rememberArtworkRefs(cached.Artwork)
		return cached.Items, nil
	}
	q := e.repo.DB.WithContext(ctx).Model(&model.Media{}).Where("deleted_at IS NULL")
	q = e.applyUserMediaVisibility(ctx, q, userID)
	if parentID != "" {
		if episodic, err := e.libraryIsEpisodic(ctx, parentID); err == nil && episodic {
			out, artwork, err := e.latestSeriesItemsForLibrary(ctx, userID, parentID, limit)
			if err == nil && e.cache != nil {
				e.cache.SetJSON(ctx, cacheKey, embyLatestCacheValue{Items: out, Artwork: artwork}, time.Duration(e.embyLatestCacheTTLSeconds())*time.Second)
			}
			return out, err
		}
		q = q.Where("library_id IN ?", e.mergedLibraryIDs(ctx, parentID))
	}
	rowLimit := limit * 4
	if rowLimit < 100 {
		rowLimit = 100
	}
	if rowLimit > 500 {
		rowLimit = 500
	}
	var rows []model.Media
	if err := q.Order(mediaReleaseOrderSQL(true)).Limit(rowLimit).Find(&rows).Error; err != nil {
		return nil, err
	}
	rows = e.collapseMediaVersionRows(ctx, rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out, err := e.payloadsForMedia(ctx, rows, userID)
	if err != nil {
		return nil, err
	}
	if e.cache != nil {
		e.cache.SetJSON(ctx, cacheKey, embyLatestCacheValue{Items: out}, time.Duration(e.embyLatestCacheTTLSeconds())*time.Second)
	}
	return out, nil
}

func (e *EmbyService) latestSeriesItemsForLibrary(ctx context.Context, userID, libraryID string, limit int) ([]map[string]any, map[string]embyArtworkRef, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := e.repo.DB.WithContext(ctx).Model(&model.Media{}).
		Where("library_id IN ? AND (season_num > 0 OR episode_num > 0)", e.mergedLibraryIDs(ctx, libraryID))
	q = e.applyUserMediaVisibility(ctx, q, userID)
	var rows []model.Media
	if err := q.Order(mediaReleaseOrderSQL(true)).Limit(embySeriesGroupingLimit).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	groups := e.seriesGroupsFromMedia(ctx, rows)
	sortSeriesGroups(groups, ItemsParams{SortBy: "premieredate", SortOrder: "Descending"})
	if len(groups) > limit {
		groups = groups[:limit]
	}
	items := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		items = append(items, e.seriesPayload(group))
	}
	return items, e.artworkRefsForSeriesGroups(groups), nil
}

// ResumeItems 列出有未完成播放进度的媒体。
func (e *EmbyService) ResumeItems(ctx context.Context, userID string, limit int) (map[string]any, error) {
	return e.resumableItems(ctx, ItemsParams{UserID: userID, Limit: limit})
}

// favoriteItems returns favourited media for Emby clients, including mounted
// remote items stored only in the local favourites table.
func (e *EmbyService) favoriteItems(ctx context.Context, p ItemsParams) (map[string]any, error) {
	if p.Limit <= 0 || p.Limit > 500 {
		p.Limit = 50
	}
	if p.StartIndex < 0 {
		p.StartIndex = 0
	}
	if strings.TrimSpace(p.UserID) == "" {
		return map[string]any{"Items": []any{}, "TotalRecordCount": int64(0), "StartIndex": p.StartIndex}, nil
	}

	var favs []model.Favorite
	if err := e.repo.DB.WithContext(ctx).
		Where("user_id = ?", p.UserID).
		Order("created_at desc").
		Find(&favs).Error; err != nil {
		return nil, err
	}
	if len(favs) == 0 {
		return map[string]any{"Items": []any{}, "TotalRecordCount": int64(0), "StartIndex": p.StartIndex}, nil
	}

	localIDs := make([]string, 0, len(favs))
	for _, fav := range favs {
		if !IsEmbyRemoteID(fav.MediaID) {
			localIDs = append(localIDs, fav.MediaID)
		}
	}
	byID := map[string]*model.Media{}
	if len(localIDs) > 0 {
		var medias []model.Media
		q := e.repo.DB.WithContext(ctx).Where("id IN ?", localIDs)
		q = e.applyUserMediaVisibility(ctx, q, p.UserID)
		if err := q.Find(&medias).Error; err != nil {
			return nil, err
		}
		for i := range medias {
			byID[medias[i].ID] = &medias[i]
		}
	}

	items := make([]map[string]any, 0, len(favs))
	for _, fav := range favs {
		if m, ok := byID[fav.MediaID]; ok {
			if !favoriteMatchesParent(ctx, e, p.ParentID, fav.MediaID, m.LibraryID, m.SeriesID, nil) {
				continue
			}
			if p.SearchTerm != "" {
				needle := strings.ToLower(p.SearchTerm)
				if !strings.Contains(strings.ToLower(m.Title), needle) &&
					!strings.Contains(strings.ToLower(m.OriginalName), needle) {
					continue
				}
			}
			items = append(items, e.itemPayload(ctx, m, true, 0))
			continue
		}
		if e.remote == nil || !IsEmbyRemoteID(fav.MediaID) {
			continue
		}
		mountID, remoteID, _ := DecodeEmbyRemoteID(fav.MediaID)
		mount, acct, err := e.remote.ResolveMount(ctx, mountID)
		if err != nil || mount == nil || acct == nil {
			continue
		}
		item, err := e.remote.RemoteItem(ctx, mount, acct, remoteID)
		if err != nil || item == nil {
			continue
		}
		if !favoriteMatchesParent(ctx, e, p.ParentID, fav.MediaID, "", "", item) {
			continue
		}
		if p.SearchTerm != "" {
			needle := strings.ToLower(p.SearchTerm)
			name, _ := item["Name"].(string)
			orig, _ := item["OriginalTitle"].(string)
			if !strings.Contains(strings.ToLower(name), needle) &&
				!strings.Contains(strings.ToLower(orig), needle) {
				continue
			}
		}
		userData, _ := item["UserData"].(map[string]any)
		if userData == nil {
			userData = map[string]any{}
			item["UserData"] = userData
		}
		userData["IsFavorite"] = true
		items = append(items, item)
	}

	total := int64(len(items))
	if p.StartIndex >= len(items) {
		return map[string]any{"Items": []map[string]any{}, "TotalRecordCount": total, "StartIndex": p.StartIndex}, nil
	}
	end := minInt(p.StartIndex+p.Limit, len(items))
	return map[string]any{"Items": items[p.StartIndex:end], "TotalRecordCount": total, "StartIndex": p.StartIndex}, nil
}

func favoriteMatchesParent(ctx context.Context, e *EmbyService, parentID, mediaID, libraryID, seriesID string, remoteItem map[string]any) bool {
	if parentID == "" {
		return true
	}
	if libraryID != "" {
		if libraryID == parentID || seriesID == parentID {
			return true
		}
		for _, id := range e.mergedLibraryIDs(ctx, parentID) {
			if id == libraryID {
				return true
			}
		}
		return false
	}
	if remoteItem == nil {
		return false
	}
	itemParent, _ := remoteItem["ParentId"].(string)
	itemSeries, _ := remoteItem["SeriesId"].(string)
	if itemParent == parentID || itemSeries == parentID || mediaID == parentID {
		return true
	}
	if !IsEmbyRemoteID(parentID) {
		return false
	}
	wantMountID, _, _ := DecodeEmbyRemoteID(parentID)
	gotMountID, _, _ := DecodeEmbyRemoteID(mediaID)
	return wantMountID != "" && gotMountID == wantMountID
}

// resumableItems 返回未完成播放进度的媒体（包含本地媒体与挂载的远程媒体），支持分页。
func (e *EmbyService) resumableItems(ctx context.Context, p ItemsParams) (map[string]any, error) {
	if p.Limit <= 0 || p.Limit > 100 {
		p.Limit = 50
	}
	if p.StartIndex < 0 {
		p.StartIndex = 0
	}
	if strings.TrimSpace(p.UserID) == "" {
		return map[string]any{"Items": []any{}, "TotalRecordCount": int64(0), "StartIndex": p.StartIndex}, nil
	}

	// 历史记录限行：此前无上限全量加载，远程条目多时既拖慢 SQL 也放大
	// 下面的远程详情请求量。
	var hist []model.PlaybackHistory
	if err := e.repo.DB.WithContext(ctx).
		Where("user_id = ? AND completed = ? AND position_ms > 0", p.UserID, false).
		Order("watched_at desc").Limit(200).Find(&hist).Error; err != nil {
		return nil, err
	}
	if len(hist) == 0 {
		return map[string]any{"Items": []any{}, "TotalRecordCount": int64(0), "StartIndex": p.StartIndex}, nil
	}

	localIDs := make([]string, 0, len(hist))
	for _, h := range hist {
		if !IsEmbyRemoteID(h.MediaID) {
			localIDs = append(localIDs, h.MediaID)
		}
	}
	byID := map[string]*model.Media{}
	if len(localIDs) > 0 {
		var medias []model.Media
		q := e.repo.DB.WithContext(ctx).Where("id IN ?", localIDs)
		q = e.applyUserMediaVisibility(ctx, q, p.UserID)
		if err := q.Find(&medias).Error; err != nil {
			return nil, err
		}
		for i := range medias {
			byID[medias[i].ID] = &medias[i]
		}
	}

	// 分页前置：凑满 StartIndex+Limit 条即停，不再为「总数」逐条发远程
	// 详情 GET（此前每条远程记录一次串行 GET，远程慢时请求挂起数分钟）。
	// 总数用候选行数（本地过滤后 + 远程候选），对继续观看行的翻页语义
	// 足够准确。
	needed := p.StartIndex + p.Limit
	items := make([]map[string]any, 0, p.Limit)
	localTotal, remoteTotal := 0, 0
	for _, h := range hist {
		if m, ok := byID[h.MediaID]; ok {
			if p.ParentID != "" && m.LibraryID != p.ParentID && m.SeriesID != p.ParentID {
				continue
			}
			localTotal++
			if produced := len(items); produced < needed {
				items = append(items, e.itemPayload(ctx, m, false, h.PositionMs))
			}
			continue
		}
		if e.remote == nil || !IsEmbyRemoteID(h.MediaID) {
			continue
		}
		remoteTotal++
		if len(items) >= needed {
			continue
		}
		mountID, remoteID, _ := DecodeEmbyRemoteID(h.MediaID)
		mount, acct, err := e.remote.ResolveMount(ctx, mountID)
		if err != nil || mount == nil || acct == nil {
			continue
		}
		item, err := e.remote.RemoteItem(ctx, mount, acct, remoteID)
		if err != nil || item == nil {
			continue
		}
		if p.ParentID != "" {
			parentID, _ := item["ParentId"].(string)
			seriesID, _ := item["SeriesId"].(string)
			if parentID != p.ParentID && seriesID != p.ParentID && mountID != p.ParentID {
				continue
			}
		}
		item["UserData"] = mergedRemoteUserData(item["UserData"], &h)
		items = append(items, item)
	}

	total := int64(localTotal + remoteTotal)
	if p.StartIndex >= len(items) {
		return map[string]any{"Items": []map[string]any{}, "TotalRecordCount": total, "StartIndex": p.StartIndex}, nil
	}
	end := minInt(p.StartIndex+p.Limit, len(items))
	return map[string]any{"Items": items[p.StartIndex:end], "TotalRecordCount": total, "StartIndex": p.StartIndex}, nil
}

func (e *EmbyService) itemPayload(ctx context.Context, m *model.Media, fav bool, posMs int64) map[string]any {
	itemType := "Movie"
	name := m.Title
	parentID := m.LibraryID
	seriesID := m.SeriesID
	seriesName := ""
	seasonID := ""
	if e.mediaShouldBeEpisode(ctx, m) {
		itemType = "Episode"
		seriesID = e.seriesIDForMedia(ctx, m)
		seriesName = e.seriesNameForMedia(ctx, m)
		seasonID = e.seasonIDForMedia(ctx, m)
		parentID = seasonID
		episodeTitle := strings.TrimSpace(m.EpisodeTitle)
		if episodeTitle != "" {
			name = episodeTitle
		} else if m.EpisodeNum > 0 {
			name = fmt.Sprintf("第 %d 集", m.EpisodeNum)
		}
	}
	imageTags := map[string]string{}
	backdropTags := []string{}
	primaryArtwork := e.mediaPrimaryArtwork(ctx, m)
	backdropArtwork := e.mediaBackdropArtwork(ctx, m)
	if primaryArtwork != "" {
		imageTags["Primary"] = m.ID
	}
	if backdropArtwork != "" {
		backdropTags = append(backdropTags, m.ID+"-bd")
	}

	runTimeTicks := int64(m.DurationSec) * 10_000_000
	durationMs := int64(m.DurationSec) * 1000
	if durationMs <= 0 && posMs > 0 {
		durationMs = posMs * 2
		if durationMs < 30*60*1000 {
			durationMs = 30 * 60 * 1000
		}
		runTimeTicks = durationMs * 10_000
	}
	played := posMs > 0 && durationMs > 0 && posMs >= durationMs*9/10
	pct := 0.0
	if durationMs > 0 {
		pct = float64(posMs) / float64(durationMs) * 100
	}

	item := map[string]any{
		"Id":                m.ID,
		"Name":              name,
		"OriginalTitle":     m.OriginalName,
		"ServerId":          embyServerID,
		"Type":              itemType,
		"MediaType":         "Video",
		"IsFolder":          false,
		"ProductionYear":    m.Year,
		"ParentIndexNumber": m.SeasonNum,
		"IndexNumber":       m.EpisodeNum,
		"Overview":          m.Overview,
		"RunTimeTicks":      runTimeTicks,
		"CommunityRating":   m.Rating,
		"Container":         m.Container,
		"Width":             m.Width,
		"Height":            m.Height,
		"DateCreated":       m.CreatedAt,
		"Path":              m.Path,
		"ParentId":          parentID,
		"SeasonId":          seasonID,
		"SeasonName":        seasonName(m.SeasonNum),
		"SeriesId":          seriesID,
		"SeriesName":        seriesName,
		"ImageTags":         imageTags,
		"BackdropImageTags": backdropTags,
		"Genres":            splitCSV(m.Genres),
		"People":            e.resolveMediaPeople(ctx, m),
		"ProviderIds": map[string]string{
			"Tmdb":    intToStr(m.TMDbID),
			"Bangumi": intToStr(m.BangumiID),
		},
		"UserData": map[string]any{
			"PlaybackPositionTicks": posMs * 10_000,
			"PlayCount":             0,
			"IsFavorite":            fav,
			"Played":                played,
			"PlayedPercentage":      pct,
		},
		"MediaSources": e.mediaSourcesForItem(ctx, m, true, false),
	}
	if primaryArtwork != "" {
		item["PrimaryImageTag"] = m.ID
	}
	if seriesID != "" {
		if sEntry, ok, _ := e.payloadSeriesEntry(ctx, seriesID); ok {
			if sEntry.posterURL != "" {
				item["SeriesPrimaryImageTag"] = embyVirtualImageTag(seriesID, embyVirtualPrimaryTagSuffix)
			}
			if len(backdropTags) == 0 && (sEntry.backdropURL != "" || sEntry.posterURL != "") {
				item["ParentBackdropItemId"] = seriesID
				item["ParentBackdropImageTags"] = []string{embyVirtualImageTag(seriesID, embyVirtualBackdropTagSuffix)}
			}
		}
	}
	if premiered, ok := embyPremiereDate(m.ReleaseDate); ok {
		item["PremiereDate"] = premiered
	}
	return item
}

func (e *EmbyService) resolveMediaPeople(ctx context.Context, m *model.Media) []map[string]any {
	if m == nil || strings.TrimSpace(m.Path) == "" {
		return []map[string]any{}
	}
	dir := filepath.Dir(m.Path)
	candidates := make([]string, 0, 6)
	seenPath := map[string]struct{}{}
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		key := strings.ToLower(filepath.Clean(path))
		if _, ok := seenPath[key]; ok {
			return
		}
		seenPath[key] = struct{}{}
		candidates = append(candidates, path)
	}
	// 共享词干优先（竞女01.mkv.strm → 竞女01.nfo），并兼容旧的单层剥扩展命名。
	add(nfoPath(m.Path))
	for _, base := range mediaSidecarBaseVariants(m.Path) {
		add(filepath.Join(dir, base+".nfo"))
	}
	add(filepath.Join(dir, "movie.nfo"))
	add(filepath.Join(dir, "tvshow.nfo"))

	people := make([]map[string]any, 0)
	seen := make(map[string]bool)

	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			doc, _, err := decodeNFOFile(p)
			if err == nil && doc != nil {
				for _, d := range doc.Directors {
					name := strings.TrimSpace(d)
					if name == "" {
						continue
					}
					personID := embyPersonID(name, "Director")
					if seen[personID] {
						continue
					}
					seen[personID] = true
					people = append(people, map[string]any{
						"Id":   personID,
						"Name": name,
						"Type": "Director",
						"Role": "Director",
					})
				}
				for _, a := range doc.Actors {
					name := strings.TrimSpace(a.Name)
					if name == "" {
						continue
					}
					personID := embyPersonID(name, "Actor")
					if seen[personID] {
						continue
					}
					seen[personID] = true
					role := strings.TrimSpace(a.Role)
					if role == "" {
						role = "Actor"
					}
					people = append(people, map[string]any{
						"Id":   personID,
						"Name": name,
						"Type": "Actor",
						"Role": role,
					})
				}
				break
			}
		}
	}
	return people
}

func embyPersonID(name, roleType string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(name)) + ":" + strings.ToLower(strings.TrimSpace(roleType))))
	return "person-" + hex.EncodeToString(sum[:8])
}
