package service

import (
	"fmt"
	"strings"

	"github.com/truewhile/MeBox/internal/model"
)

type mediaSeriesKeyResolver struct {
	pathCounts     map[string]int
	externalCounts map[string]int
	titleCounts    map[string]int
	pathTitles     map[string]string
}

// mediaSeriesKeyInputs 保存单条媒体解析剧集 key 所需的派生字段。剧集卡片、
// 剧集索引和分页分组需要反复读取同一批行；提前计算一次可以避免对每行执行
// 多轮正则匹配、路径解析和标题规范化。
type mediaSeriesKeyInputs struct {
	episodic    bool
	pathKey     string
	externalKey string
	titleKey    string
}

func analyzeMediaSeriesKey(item model.Media) mediaSeriesKeyInputs {
	if !mediaLooksEpisodicForGrouping(item) {
		return mediaSeriesKeyInputs{}
	}
	return mediaSeriesKeyInputs{
		episodic:    true,
		pathKey:     mediaSeriesRawKey(item),
		externalKey: repeatedSeriesExternalKey(item),
		titleKey:    repeatedSeriesTitleKey(item),
	}
}

func analyzeMediaSeriesKeys(items []model.Media) []mediaSeriesKeyInputs {
	inputs := make([]mediaSeriesKeyInputs, len(items))
	for i := range items {
		inputs[i] = analyzeMediaSeriesKey(items[i])
	}
	return inputs
}

func newMediaSeriesKeyResolver(items []model.Media) mediaSeriesKeyResolver {
	return newMediaSeriesKeyResolverFromInputs(analyzeMediaSeriesKeys(items))
}

func newMediaSeriesKeyResolverFromInputs(inputs []mediaSeriesKeyInputs) mediaSeriesKeyResolver {
	resolver := mediaSeriesKeyResolver{
		pathCounts:     make(map[string]int),
		externalCounts: make(map[string]int),
		titleCounts:    make(map[string]int),
		pathTitles:     make(map[string]string),
	}
	pathTitleCandidates := make(map[string]map[string]struct{})
	for _, input := range inputs {
		if !input.episodic {
			continue
		}
		if strings.HasPrefix(input.pathKey, "library-path") {
			resolver.pathCounts[input.pathKey]++
			if input.titleKey != "" {
				if pathTitleCandidates[input.pathKey] == nil {
					pathTitleCandidates[input.pathKey] = make(map[string]struct{})
				}
				pathTitleCandidates[input.pathKey][input.titleKey] = struct{}{}
			}
		}
		if input.externalKey != "" {
			resolver.externalCounts[input.externalKey]++
		}
		if input.titleKey != "" {
			resolver.titleCounts[input.titleKey]++
		}
	}
	// A scraper can normalize the same show to one title while the source
	// release folders still contain different tags (1080p/2160p, uploader
	// names, etc.). Remember an unambiguous title alias for each path group so
	// those folders are bridged instead of rendered as separate collections.
	for pathKey, candidates := range pathTitleCandidates {
		repeatedTitle := ""
		repeatedCount := 0
		for titleKey := range candidates {
			if resolver.titleCounts[titleKey] < 2 {
				continue
			}
			repeatedTitle = titleKey
			repeatedCount++
		}
		if repeatedCount == 1 {
			resolver.pathTitles[pathKey] = repeatedTitle
		}
	}
	return resolver
}

// resolveMediaSeriesKeys 对输入只做一轮派生字段计算，返回可供后续 O(1) 解析的
// resolver 以及每条记录对应的 key。这是整库剧集加载的快速路径。
func resolveMediaSeriesKeys(items []model.Media) (mediaSeriesKeyResolver, []string) {
	inputs := analyzeMediaSeriesKeys(items)
	resolver := newMediaSeriesKeyResolverFromInputs(inputs)
	keys := make([]string, len(items))
	for i := range items {
		keys[i] = resolver.keyFromInputs(items[i], inputs[i])
	}
	return resolver, keys
}

func (r mediaSeriesKeyResolver) key(media model.Media) string {
	return r.keyFromInputs(media, analyzeMediaSeriesKey(media))
}

func (r mediaSeriesKeyResolver) keyFromInputs(media model.Media, input mediaSeriesKeyInputs) string {
	if input.episodic {
		if strings.HasPrefix(input.pathKey, "library-path") {
			if titleKey := r.pathTitles[input.pathKey]; titleKey != "" {
				return compactSeriesKey(titleKey)
			}
			// A series directory is the strongest identity for mixed rows:
			// main episodes and specials (CM/NCOP/PV/OVA) may be scraped to
			// slightly different titles, but they still belong to one show.
			if r.pathCounts[input.pathKey] > 1 {
				return compactSeriesKey(input.pathKey)
			}
		}
		if input.titleKey != "" && r.titleCounts[input.titleKey] > 1 {
			return compactSeriesKey(input.titleKey)
		}
		if input.externalKey != "" && r.externalCounts[input.externalKey] > 1 {
			return compactSeriesKey(input.externalKey)
		}
	}
	return mediaSeriesKey(media)
}

func mediaLooksEpisodicForGrouping(media model.Media) bool {
	return media.SeasonNum > 0 || media.EpisodeNum > 0 ||
		episodicPathRE.MatchString(media.Path+" "+media.DisplayLibraryPath+" "+media.LibraryPath)
}

func repeatedSeriesExternalKey(media model.Media) string {
	identity := ""
	switch {
	case media.TMDbID > 0:
		identity = fmt.Sprintf("tmdb:%d", media.TMDbID)
	case media.BangumiID > 0:
		identity = fmt.Sprintf("bgm:%d", media.BangumiID)
	case strings.TrimSpace(media.DoubanID) != "":
		identity = "douban:" + strings.TrimSpace(media.DoubanID)
	case strings.TrimSpace(media.TheTVDBID) != "":
		identity = "thetvdb:" + strings.TrimSpace(media.TheTVDBID)
	}
	if identity == "" {
		return ""
	}
	return seriesFingerprint("library-external", mediaTargetLibraryID(media), identity)
}

func repeatedSeriesTitleKey(media model.Media) string {
	if !strings.EqualFold(strings.TrimSpace(media.ScrapeStatus), "matched") {
		return ""
	}
	title := strings.TrimSpace(firstNonEmpty(media.Title, media.OriginalName))
	if title == "" || unsafeAutomaticEpisodeQuery(title) || organizeMediaTitleLooksLikeRelease(title) {
		return ""
	}
	title = normalizeSeriesTitle(title)
	if title == "" {
		return ""
	}
	return seriesFingerprint("library-title-year", mediaTargetLibraryID(media), title, fmt.Sprint(media.Year))
}
