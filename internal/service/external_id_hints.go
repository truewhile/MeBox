package service

import (
	"path/filepath"
	"regexp"
	"strings"
)

type mediaExternalIDHints struct {
	TMDbID    int
	BangumiID int
	DoubanID  string
	TheTVDBID string
}

var externalIDHintPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"tmdb", regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:tmdb|tmdbid)[\s_:=#-]*(\d{2,})`)},
	{"bangumi", regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:bangumi|bgm)[\s_:=#-]*(\d{2,})`)},
	{"douban", regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:douban|db)[\s_:=#-]*(\d{2,})`)},
	{"thetvdb", regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:thetvdb|tvdb)[\s_:=#-]*(\d{2,})`)},
}

func externalIDHintsFromText(raw string) mediaExternalIDHints {
	var hints mediaExternalIDHints
	for _, item := range externalIDHintPatterns {
		m := item.re.FindStringSubmatch(raw)
		if len(m) < 2 {
			continue
		}
		value := strings.TrimSpace(m[1])
		switch item.name {
		case "tmdb":
			hints.TMDbID = mustAtoi(value)
		case "bangumi":
			hints.BangumiID = mustAtoi(value)
		case "douban":
			hints.DoubanID = value
		case "thetvdb":
			hints.TheTVDBID = value
		}
	}
	return hints
}

func (h mediaExternalIDHints) useful() bool {
	return h.TMDbID > 0 || h.BangumiID > 0 || strings.TrimSpace(h.DoubanID) != "" || strings.TrimSpace(h.TheTVDBID) != ""
}

func (h mediaExternalIDHints) applyToLocalMetadata(meta *LocalMetadata) *LocalMetadata {
	if !h.useful() {
		return meta
	}
	if meta == nil {
		meta = &LocalMetadata{}
	}
	if h.TMDbID > 0 && meta.TMDbID <= 0 {
		meta.TMDbID = h.TMDbID
	}
	if h.BangumiID > 0 && meta.BangumiID <= 0 {
		meta.BangumiID = h.BangumiID
	}
	if strings.TrimSpace(h.DoubanID) != "" && meta.DoubanID == "" {
		meta.DoubanID = strings.TrimSpace(h.DoubanID)
	}
	if strings.TrimSpace(h.TheTVDBID) != "" && meta.TheTVDBID == "" {
		meta.TheTVDBID = strings.TrimSpace(h.TheTVDBID)
	}
	meta.PathHint = true
	return meta
}

func pathHintMetadata(raw string, seriesLike bool) (*LocalMetadata, mediaExternalIDHints) {
	source := pathHintSourceText(raw, seriesLike)
	hints := externalIDHintsFromText(source)
	title, year := "", 0
	if seriesLike {
		title, year = CleanQuery(source)
	} else if patTheatricalTitle.MatchString(raw) || patTheatricalFolder.MatchString(raw) {
		base := pathBaseSlash(raw)
		stem := mediaFileStem(base)
		if stem == "" {
			stem = strings.TrimSuffix(base, filepath.Ext(base))
		}
		title, year = CleanQuery(stem)
	} else {
		title, year = cloudSeriesTitleFromMediaPath(source)
		if title == "" {
			title, year = CleanQuery(source)
		}
	}
	if !hints.useful() && title == "" && year <= 0 {
		return nil, hints
	}
	meta := (&mediaExternalIDHints{
		TMDbID:    hints.TMDbID,
		BangumiID: hints.BangumiID,
		DoubanID:  hints.DoubanID,
		TheTVDBID: hints.TheTVDBID,
	}).applyToLocalMetadata(&LocalMetadata{PathHint: true})
	if title != "" {
		meta.Title = title
	}
	if year > 0 {
		meta.Year = year
	}
	return meta, hints
}

func pathHintSourceText(raw string, seriesLike bool) string {
	raw = strings.TrimSpace(raw)
	if !seriesLike || raw == "" {
		return raw
	}
	base := pathBaseSlash(raw)
	ext := strings.ToLower(filepath.Ext(base))
	if _, ok := videoExtensions[ext]; !ok {
		return raw
	}
	if showDir := showDirFromEpisodePath(raw); showDir != "" {
		return showDir
	}
	return raw
}
