package service

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/truewhile/MeBox/internal/model"
)

var episodicPathRE = regexp.MustCompile(`(?i)[\\/](?:电视剧|剧集|连续剧|短剧|国产剧|国剧|大陆剧|华语剧|国产电视剧|大陆电视剧|华语电视剧|欧美剧|欧美电视剧|美剧|英剧|日韩剧|日韩电视剧|日剧|韩剧|港剧|台剧|港台剧|泰剧|综艺|纪录片|儿童|动漫|番剧|国漫|日番|韩漫|美漫|欧美动漫|欧美动画|其他动漫|tv|series|shows?|season[\s._-]*\d|s\d{1,2}(?:[\s._-]|[\\/])|special[\s._-]*episodes?|specials?|sp|ovas?|oads?|ovds?|onas?|extras?|bonus(?:es)?|omake|picture[\s._-]*drama|ncop|nced|特别篇|特別篇|番外篇?|特典|外传|外傳|总集篇|總集篇|画像特典)[\\/]`)

var genericMovieTitleRE = regexp.MustCompile(`(?i)^(?:cd\s*\d+|part\s*\d+|disc\s*\d+|disk\s*\d+|dvd\s*\d+|movie|film|video|main|feature|track\s*\d+|preview|sample|trailer|\d{3,4}p|4k|2160p|1080p|720p)$`)

func isGenericMovieTitle(title string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return true
	}
	return genericMovieTitleRE.MatchString(title)
}

func mediaSeriesKey(media model.Media) string {
	return compactSeriesKey(mediaSeriesRawKey(media))
}

func mediaSeriesRawKey(media model.Media) string {
	fromPath := seriesTitleFromMediaPath(media.Path)
	if seriesTitleIsGenericContainer(fromPath, media) {
		fromPath = ""
	}
	if media.SeasonNum > 0 || media.EpisodeNum > 0 || episodicPathRE.MatchString(media.Path+" "+media.DisplayLibraryPath+" "+media.LibraryPath) {
		if fromPath != "" {
			return seriesFingerprint("library-path", mediaTargetLibraryID(media), fromPath)
		}
		if idKey := seriesExternalIDKeyFromPath(media.Path); idKey != "" {
			return seriesFingerprint("library-path-id", mediaTargetLibraryID(media), idKey)
		}
		if media.TMDbID > 0 {
			return seriesFingerprint("episodic-external", fmt.Sprintf("tmdb:%d", media.TMDbID))
		}
		if media.BangumiID > 0 {
			return seriesFingerprint("episodic-external", fmt.Sprintf("bgm:%d", media.BangumiID))
		}
		if strings.TrimSpace(media.DoubanID) != "" {
			return seriesFingerprint("episodic-external", "douban:"+strings.TrimSpace(media.DoubanID))
		}
		if strings.TrimSpace(media.TheTVDBID) != "" {
			return seriesFingerprint("episodic-external", "thetvdb:"+strings.TrimSpace(media.TheTVDBID))
		}
		if strings.TrimSpace(media.SeriesID) != "" {
			return "series:" + strings.TrimSpace(media.SeriesID)
		}
		return seriesFingerprint("library-title", mediaTargetLibraryID(media), normalizeSeriesTitle(seriesDisplayTitle(media)))
	}
	if strings.TrimSpace(media.SeriesID) != "" {
		return "series:" + strings.TrimSpace(media.SeriesID)
	}
	if media.TMDbID > 0 {
		return seriesFingerprint("movie-external", fmt.Sprintf("tmdb:%d", media.TMDbID))
	}
	if media.BangumiID > 0 {
		return seriesFingerprint("movie-external", fmt.Sprintf("bgm:%d", media.BangumiID))
	}
	if fromPath != "" && !mediaParentLooksLikeCollection(media.Path) {
		titleNorm := normalizeSeriesTitle(media.Title)
		fromPathNorm := normalizeSeriesTitle(fromPath)
		if titleNorm == "" || isGenericMovieTitle(titleNorm) || titleNorm == fromPathNorm {
			return seriesFingerprint("library-path", media.LibraryID, fromPath)
		}
	}
	return seriesFingerprint("library-title", media.LibraryID, normalizeSeriesTitle(media.Title))
}

// A flat library such as /media/电视剧 may contain many unrelated episodes.
// Treating the category/root folder as the show title collapses all of them
// into one giant collection, so generic container names must not be used as a
// path identity.
func seriesTitleIsGenericContainer(title string, media model.Media) bool {
	title = normalizeSeriesTitle(title)
	if title == "" {
		return false
	}
	switch title {
	case "movie", "movies", "film", "films", "tv", "series", "show", "shows", "anime", "animation", "variety",
		"电视剧", "剧集", "连续剧", "短剧", "国产剧", "国剧", "欧美剧", "美剧", "英剧", "日韩剧", "日剧", "韩剧", "港剧", "台剧", "港台剧",
		"综艺", "纪录片", "儿童", "动漫", "番剧", "国漫", "日番", "韩漫", "美漫", "欧美动漫", "欧美动画", "其他动漫", "电影", "成人", "未分类":
		return true
	}
	for _, libraryPath := range []string{media.DisplayLibraryPath, media.LibraryPath} {
		libraryPath = strings.TrimSpace(libraryPath)
		if libraryPath == "" {
			continue
		}
		base := pathBaseSlash(libraryPath)
		if normalizeSeriesTitle(base) == title {
			return true
		}
	}
	return false
}

func seriesFingerprint(parts ...string) string {
	return strings.Join(parts, "\x1f")
}

func compactSeriesKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var hash uint32 = 2166136261
	for _, b := range []byte(raw) {
		hash ^= uint32(b)
		hash *= 16777619
	}
	return fmt.Sprintf("series:%08x", hash)
}

var (
	seriesYearRE        = regexp.MustCompile(`\s*\((?:19|20)\d{2}\)\s*`)
	seriesIDRE          = regexp.MustCompile(`(?i)\s*\[(?:tmdb|tmdbid)[=-]\d+\]\s*`)
	seriesBraceRE       = regexp.MustCompile(`(?i)\s*\{(?:tmdb|tmdbid|douban|bangumi|bgm|thetvdb|tvdb)[\s:=#-]*[a-z0-9_-]+\}\s*`)
	seriesSpacerRE      = regexp.MustCompile(`[\s._-]+`)
	seriesSeasonDirRE   = regexp.MustCompile(`(?i)^(?:s\d{1,2}|season[\s._-]*\d{1,2}|第\s*[0-9一二三四五六七八九十百零两]+\s*季|special[\s._-]*episodes?|specials?|sp|ovas?|oads?|ovds?|onas?|extras?|bonus(?:es)?|omake|picture[\s._-]*drama|ncop|nced|特别篇|特別篇|番外篇?|特典|外传|外傳|总集篇|總集篇|画像特典)$`)
	seriesSpecialCodeRE = regexp.MustCompile(`(?i)\s*[\[(（【]?\s*(?:s0+\s*e?\s*\d+|season\s*0+(?:\s*episode)?\s*\d*|special(?:\s*episode)?s?\s*\d*|sp\s*\d*|ovas?\s*\d*|oads?\s*\d*|ovds?\s*\d*|onas?\s*\d*|extras?\s*\d*|bonus(?:es)?\s*\d*|omake\s*\d*|picture[\s._-]*drama\s*\d*|ncop\s*\d*|nced\s*\d*)\s*[\])）】]?$`)
	seriesSpecialCJKRE  = regexp.MustCompile(`(?i)\s*[\[(（【]?\s*(?:特别篇|特別篇|番外篇?|特典|外传|外傳|总集篇|總集篇|画像特典)(?:\s*第?\s*[0-9一二三四五六七八九十百零两]+(?:[集话話期])?)?\s*[\])）】]?$`)
)

func normalizeSeriesTitle(value string) string {
	// 归组时每行都会调用（且同一部剧的每集标题相同），纯函数缓存能省掉
	// 4 条正则的重复执行。
	if normalized, ok := seriesTitleNormMemo.get(value); ok {
		return normalized
	}
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = seriesYearRE.ReplaceAllString(normalized, " ")
	normalized = seriesIDRE.ReplaceAllString(normalized, " ")
	normalized = seriesBraceRE.ReplaceAllString(normalized, " ")
	normalized = seriesSpacerRE.ReplaceAllString(normalized, " ")
	normalized = strings.TrimSpace(normalized)
	seriesTitleNormMemo.put(value, normalized)
	return normalized
}

func normalizeSeriesPathTitle(value string) string {
	// 整库归组时每个文件都会走到这里，而 CleanQuery 内部是十几条正则。
	// 同一部剧的几十集共享同一个目录名，按目录名缓存后，首页「最近添加」
	// 的分组阶段从 240ms 降到几十毫秒（7k 行实测）。
	if title, ok := seriesPathTitleMemo.get(value); ok {
		return title
	}
	title, _ := CleanQuery(value)
	if title == "" {
		title = normalizeSeriesTitle(value)
	} else {
		title = normalizeSeriesTitle(title)
	}
	stripped := stripSeriesSpecialSuffix(title)
	if stripped != "" {
		title = stripped
	}
	seriesPathTitleMemo.put(value, title)
	return title
}

// boundedStringMemo 是纯字符串函数的定长缓存。
//
// 只用于「入参唯一决定出参、不读配置/数据库」的函数：剧集标题规范化、
// 目录名清洗等。整库归组会把这些函数在每行上重复调用几十次，缓存后首页
// 「最近添加」的 CPU 从 ~240ms 降到 ~40ms（7k 行实测）。上限到达后整体
// 清空，避免长跑进程无限增长（不同目录名/标题的数量远小于集数）。
type boundedStringMemo struct {
	mu      sync.RWMutex
	max     int
	entries map[string]string
}

func newBoundedStringMemo(max int) *boundedStringMemo {
	if max <= 0 {
		max = 1024
	}
	return &boundedStringMemo{max: max, entries: make(map[string]string, max/8)}
}

func (m *boundedStringMemo) get(key string) (string, bool) {
	m.mu.RLock()
	value, ok := m.entries[key]
	m.mu.RUnlock()
	return value, ok
}

func (m *boundedStringMemo) put(key, value string) {
	m.mu.Lock()
	if len(m.entries) >= m.max {
		m.entries = make(map[string]string, m.max/4)
	}
	m.entries[key] = value
	m.mu.Unlock()
}

var (
	seriesPathTitleMemo = newBoundedStringMemo(4096)
	seriesTitleNormMemo = newBoundedStringMemo(4096)
)

func stripSeriesSpecialSuffix(title string) string {
	for _, re := range []*regexp.Regexp{seriesSpecialCodeRE, seriesSpecialCJKRE} {
		stripped := strings.TrimSpace(re.ReplaceAllString(title, ""))
		if stripped != "" && stripped != title {
			return stripped
		}
	}
	return title
}

func seriesTitleFromMediaPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) < 2 {
		return ""
	}
	dirIndex := len(parts) - 2
	if last := parts[len(parts)-1]; !seriesPathPartLooksLikeFile(last) && !seriesSeasonDirRE.MatchString(filepath.Base(last)) {
		dirIndex = len(parts) - 1
	}
	for dirIndex >= 0 && (seriesSeasonDirRE.MatchString(filepath.Base(parts[dirIndex])) || isTheatricalFolder(parts[dirIndex])) {
		dirIndex--
	}
	if dirIndex < 0 {
		return ""
	}
	title := normalizeSeriesPathTitle(parts[dirIndex])
	if unsafeAutomaticEpisodeQuery(title) {
		return ""
	}
	return title
}

func seriesPathPartLooksLikeFile(part string) bool {
	ext := strings.ToLower(filepath.Ext(part))
	if _, ok := videoExtensions[ext]; ok {
		return true
	}
	switch ext {
	case ".iso", ".nfo", ".srt", ".ass", ".ssa", ".vtt", ".sub", ".idx", ".jpg", ".jpeg", ".png", ".webp":
		return true
	default:
		return false
	}
}

func seriesDisplayTitle(media model.Media) string {
	if fromPath := seriesTitleFromMediaPath(media.Path); fromPath != "" && !seriesTitleIsGenericContainer(fromPath, media) {
		return fromPath
	}
	if media.Title != "" {
		return media.Title
	}
	if media.OriginalName != "" {
		return media.OriginalName
	}
	if fromFile, _ := CleanQuery(media.Path); strings.TrimSpace(fromFile) != "" && !unsafeAutomaticEpisodeQuery(fromFile) {
		return fromFile
	}
	return "未命名节目"
}

func seriesExternalIDKeyFromPath(path string) string {
	_, hints := pathHintMetadata(path, true)
	switch {
	case hints.TMDbID > 0:
		return fmt.Sprintf("tmdb:%d", hints.TMDbID)
	case hints.BangumiID > 0:
		return fmt.Sprintf("bgm:%d", hints.BangumiID)
	case strings.TrimSpace(hints.DoubanID) != "":
		return "douban:" + strings.TrimSpace(hints.DoubanID)
	case strings.TrimSpace(hints.TheTVDBID) != "":
		return "thetvdb:" + strings.TrimSpace(hints.TheTVDBID)
	default:
		return ""
	}
}

func mediaTargetLibraryID(media model.Media) string {
	if strings.TrimSpace(media.DisplayLibraryID) != "" {
		return media.DisplayLibraryID
	}
	return media.LibraryID
}
