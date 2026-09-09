package service

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/truewhile/MeBox/internal/model"
)

type MediaItem struct {
	model.Media
	Versions []model.Media `json:"versions,omitempty"`
}

func normalizeGroupedMediaPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > maxMediaSearchPageSize {
		pageSize = maxMediaSearchPageSize
	}
	return page, pageSize
}

func paginateMediaItems(items []MediaItem, page, pageSize int) []MediaItem {
	page, pageSize = normalizeGroupedMediaPage(page, pageSize)
	if len(items) == 0 {
		// 返回非 nil 空切片：nil 会被 JSON 序列化成 "items": null，
		// 前端 concat(null) 会得到 [null] 并在渲染期崩溃（空库进入白屏）。
		return []MediaItem{}
	}
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []MediaItem{}
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

// PaginateMediaItems 导出分页辅助函数。
func PaginateMediaItems(items []MediaItem, page, pageSize int) []MediaItem {
	return paginateMediaItems(items, page, pageSize)
}

func firstMediaItems(items []MediaItem, limit int) []MediaItem {
	if len(items) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > maxMediaSearchLimit {
		limit = maxMediaSearchLimit
	}
	if limit > len(items) {
		limit = len(items)
	}
	return items[:limit]
}

// FirstMediaItems 导出截取前 N 项辅助函数。
func FirstMediaItems(items []MediaItem, limit int) []MediaItem {
	return firstMediaItems(items, limit)
}

func groupMediaVersions(items []model.Media) []MediaItem {
	if len(items) == 0 {
		return nil
	}
	type group struct {
		key     string
		primary model.Media
		rows    []model.Media
	}
	groups := make([]group, 0, len(items))
	byKey := make(map[string]int, len(items))
	for _, item := range items {
		key := mediaVersionGroupKey(item)
		if key == "" {
			groups = append(groups, group{primary: item, rows: []model.Media{item}})
			continue
		}
		if idx, ok := byKey[key]; ok {
			groups[idx].rows = append(groups[idx].rows, item)
			if betterMediaVersion(item, groups[idx].primary) {
				groups[idx].primary = item
			}
			continue
		}
		byKey[key] = len(groups)
		groups = append(groups, group{key: key, primary: item, rows: []model.Media{item}})
	}
	out := make([]MediaItem, 0, len(groups))
	for _, g := range groups {
		sort.SliceStable(g.rows, func(i, j int) bool {
			return betterMediaVersion(g.rows[i], g.rows[j])
		})
		item := MediaItem{Media: g.primary}
		if len(g.rows) > 1 {
			item.Versions = g.rows
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// GroupMediaVersions 导出多版本分组函数。
func GroupMediaVersions(items []model.Media) []MediaItem {
	return groupMediaVersions(items)
}

// GroupEpisodeVersionsForDisplay folds encoding/container variants into one
// episode while preserving season/episode ordering for episode-list APIs.
func GroupEpisodeVersionsForDisplay(items []model.Media) []MediaItem {
	grouped := groupMediaVersions(items)
	if grouped == nil {
		return []MediaItem{}
	}
	sort.SliceStable(grouped, func(i, j int) bool {
		if grouped[i].SeasonNum != grouped[j].SeasonNum {
			return grouped[i].SeasonNum < grouped[j].SeasonNum
		}
		if grouped[i].EpisodeNum != grouped[j].EpisodeNum {
			return grouped[i].EpisodeNum < grouped[j].EpisodeNum
		}
		return grouped[i].CreatedAt.Before(grouped[j].CreatedAt)
	})
	return grouped
}

func mediaVersionGroupKey(m model.Media) string {
	// 远程 Emby 挂载条目保持独立，不与其它远程条目或本地条目折叠合并。
	if IsEmbyRemoteID(m.ID) {
		return fmt.Sprintf("embyremote:%s", m.ID)
	}

	libKey := strings.ToLower(strings.TrimSpace(m.LibraryID))
	if libKey == "" {
		libKey = strings.ToLower(strings.TrimSpace(m.DisplayLibraryID))
	}
	specialKind := mediaSpecialKind(m.Path)
	season, episode := m.SeasonNum, m.EpisodeNum
	if specialKind != "" && specialKind != mediaSpecialTheatrical && episode <= 0 {
		if parsedSeason, parsedEpisode := ParseEpisode(m.Path); parsedEpisode > 0 {
			season, episode = parsedSeason, parsedEpisode
		}
	}

	if season > 0 || episode > 0 {
		kind := specialKind
		if kind == "" {
			kind = "episode"
		}
		switch {
		case m.TMDbID > 0:
			return fmt.Sprintf("episode:%s:tmdb:%d:%d:%d", kind, m.TMDbID, season, episode)
		case m.BangumiID > 0:
			return fmt.Sprintf("episode:%s:bangumi:%d:%d:%d", kind, m.BangumiID, season, episode)
		case strings.TrimSpace(m.DoubanID) != "":
			return fmt.Sprintf("episode:%s:douban:%s:%d:%d", kind, strings.ToLower(strings.TrimSpace(m.DoubanID)), season, episode)
		case strings.TrimSpace(m.TheTVDBID) != "":
			return fmt.Sprintf("episode:%s:thetvdb:%s:%d:%d", kind, strings.ToLower(strings.TrimSpace(m.TheTVDBID)), season, episode)
		}
		title := firstNonEmpty(m.OriginalName, m.Title)
		if title == "" {
			title, _ = CleanQuery(m.Path)
		}
		title, _ = mediaVersionTitleKey(title)
		if title == "" {
			return ""
		}
		return strings.Join([]string{
			"episode",
			kind,
			libKey,
			title,
			fmt.Sprintf("%d:%d", season, episode),
		}, "|")
	}

	if specialKind != "" && specialKind != mediaSpecialTheatrical {
		return mediaVersionStemGroupKey(m, libKey)
	}

	switch {
	case m.TMDbID > 0:
		if libKey != "" {
			if specialKind != "" {
				return fmt.Sprintf("movie:%s:%s:tmdb:%d", specialKind, libKey, m.TMDbID)
			}
			return fmt.Sprintf("movie:%s:tmdb:%d", libKey, m.TMDbID)
		}
		if specialKind != "" {
			return fmt.Sprintf("movie:%s:tmdb:%d", specialKind, m.TMDbID)
		}
		return fmt.Sprintf("tmdb:%d", m.TMDbID)
	case m.BangumiID > 0:
		if libKey != "" {
			if specialKind != "" {
				return fmt.Sprintf("movie:%s:%s:bangumi:%d", specialKind, libKey, m.BangumiID)
			}
			return fmt.Sprintf("movie:%s:bangumi:%d", libKey, m.BangumiID)
		}
		if specialKind != "" {
			return fmt.Sprintf("movie:%s:bangumi:%d", specialKind, m.BangumiID)
		}
		return fmt.Sprintf("bangumi:%d", m.BangumiID)
	case strings.TrimSpace(m.DoubanID) != "":
		if libKey != "" {
			if specialKind != "" {
				return fmt.Sprintf("movie:%s:%s:douban:%s", specialKind, libKey, strings.ToLower(strings.TrimSpace(m.DoubanID)))
			}
			return fmt.Sprintf("movie:%s:douban:%s", libKey, strings.ToLower(strings.TrimSpace(m.DoubanID)))
		}
		if specialKind != "" {
			return "movie:" + specialKind + ":douban:" + strings.ToLower(strings.TrimSpace(m.DoubanID))
		}
		return "douban:" + strings.ToLower(strings.TrimSpace(m.DoubanID))
	case strings.TrimSpace(m.TheTVDBID) != "":
		if libKey != "" {
			if specialKind != "" {
				return fmt.Sprintf("movie:%s:%s:thetvdb:%s", specialKind, libKey, strings.ToLower(strings.TrimSpace(m.TheTVDBID)))
			}
			return fmt.Sprintf("movie:%s:thetvdb:%s", libKey, strings.ToLower(strings.TrimSpace(m.TheTVDBID)))
		}
		if specialKind != "" {
			return "movie:" + specialKind + ":thetvdb:" + strings.ToLower(strings.TrimSpace(m.TheTVDBID))
		}
		return "thetvdb:" + strings.ToLower(strings.TrimSpace(m.TheTVDBID))
	}

	title := firstNonEmpty(m.OriginalName, m.Title)
	titleYear := 0
	if title == "" {
		title, _ = CleanQuery(m.Path)
	} else {
		title, titleYear = mediaVersionTitleKey(title)
	}
	if title == "" {
		// 无标题时退回同目录词干（覆盖 keep_ext 的 name.mkv.strm / name.mp4.strm）
		if stemKey := mediaVersionStemGroupKey(m, libKey); stemKey != "" {
			return stemKey
		}
		return ""
	}
	year := m.Year
	if year <= 0 {
		year = titleYear
	}
	if year <= 0 {
		_, year = CleanQuery(m.Path)
	}
	if libKey != "" {
		return fmt.Sprintf("movie:%s:%s:%d", libKey, title, year)
	}
	return fmt.Sprintf("movie:%s:%d", title, year)
}

// mediaVersionStemGroupKey 按「库 + 父目录 + 文件词干」折叠多版本
// （如 竞女01.mkv.strm 与 竞女01.mp4.strm）。
func mediaVersionStemGroupKey(m model.Media, libKey string) string {
	path := strings.ReplaceAll(strings.TrimSpace(m.Path), "\\", "/")
	if path == "" || strings.HasPrefix(strings.ToLower(path), "cloud://") {
		return ""
	}
	dir := ""
	base := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		dir = path[:idx]
		base = path[idx+1:]
	}
	stem := mediaVersionFileStem(base)
	if stem == "" {
		return ""
	}
	stem = normalizeMediaVersionText(stem)
	if stem == "" {
		return ""
	}
	if libKey == "" {
		libKey = "_"
	}
	return fmt.Sprintf("stem:%s:%s:%s", libKey, strings.ToLower(dir), stem)
}

// mediaVersionFileStem 去掉最终扩展名；若为 .strm 且前一层是视频扩展，再剥一层。
func mediaVersionFileStem(name string) string {
	return mediaFileStem(name)
}

// MediaVersionLabel 生成版本切换展示名（分辨率 / 容器 / 编码 / 体积 / 文件名）。
func MediaVersionLabel(m model.Media) string {
	parts := make([]string, 0, 4)
	if m.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dp", m.Height))
	} else if m.Width > 0 {
		parts = append(parts, fmt.Sprintf("%dw", m.Width))
	}
	container := strings.Trim(strings.ToLower(strings.TrimSpace(m.Container)), ". ")
	if container == "" || container == "strm" {
		container = mediaVersionContainerFromPath(m.Path, m.STRMURL)
	}
	if container != "" && container != "strm" {
		parts = append(parts, strings.ToUpper(container))
	}
	if codec := strings.TrimSpace(m.VideoCodec); codec != "" {
		parts = append(parts, strings.ToUpper(codec))
	}
	if m.SizeBytes > 0 {
		parts = append(parts, formatMediaSize(m.SizeBytes))
	}
	if len(parts) > 0 {
		return strings.Join(parts, " · ")
	}
	base := filepath.Base(strings.ReplaceAll(strings.TrimSpace(m.Path), "\\", "/"))
	if base == "" || base == "." {
		return firstNonEmpty(m.Title, m.OriginalName, m.ID)
	}
	return base
}

func mediaVersionContainerFromPath(path, strmURL string) string {
	base := filepath.Base(strings.ReplaceAll(strings.TrimSpace(path), "\\", "/"))
	ext := strings.ToLower(filepath.Ext(base))
	name := strings.TrimSuffix(base, ext)
	if ext == ".strm" {
		if second := strings.ToLower(filepath.Ext(name)); second != "" {
			if _, ok := videoExtensions[second]; ok {
				return strings.TrimPrefix(second, ".")
			}
		}
		// 从 strm 播放 URL 的 /video.mkv 推断
		u := strings.ToLower(strmURL)
		if idx := strings.LastIndex(u, "/video."); idx >= 0 {
			rest := u[idx+len("/video."):]
			if end := strings.IndexAny(rest, "?#&/"); end >= 0 {
				rest = rest[:end]
			}
			rest = strings.Trim(rest, ".")
			if rest != "" {
				return rest
			}
		}
		return "strm"
	}
	if ext != "" {
		if _, ok := videoExtensions[ext]; ok {
			return strings.TrimPrefix(ext, ".")
		}
	}
	return strings.TrimPrefix(ext, ".")
}

func formatMediaSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	const unit = 1024
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func mediaVersionTitleKey(value string) (string, int) {
	cleaned, year := CleanQuery(value)
	if strings.TrimSpace(cleaned) == "" {
		cleaned = value
	}
	return normalizeMediaVersionText(cleaned), year
}

func normalizeMediaVersionText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		switch r {
		case '.', '_', '-', ' ', '\t', '/', '\\', '[', ']', '(', ')', '（', '）', '【', '】':
			return true
		default:
			return false
		}
	})
	out := fields[:0]
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, noise := noiseTokenSet[field]; noise {
			continue
		}
		// 去掉视频容器词干残留（keep_ext / 旧标题「竞女01 mkv」）
		if _, ok := videoExtensions["."+field]; ok {
			continue
		}
		out = append(out, field)
	}
	return strings.Join(out, " ")
}

func betterMediaVersion(candidate, current model.Media) bool {
	candidateCloud := isCloudMediaVersion(candidate)
	currentCloud := isCloudMediaVersion(current)
	if candidateCloud != currentCloud {
		return !candidateCloud
	}
	candidatePixels := candidate.Width * candidate.Height
	currentPixels := current.Width * current.Height
	if candidatePixels != currentPixels {
		return candidatePixels > currentPixels
	}
	if candidate.SizeBytes != current.SizeBytes {
		return candidate.SizeBytes > current.SizeBytes
	}
	return candidate.CreatedAt.After(current.CreatedAt)
}

func isCloudMediaVersion(media model.Media) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(media.Path)), "cloud://") ||
		strings.Contains(strings.ToLower(strings.TrimSpace(media.STRMURL)), "/api/cloud/play/")
}
