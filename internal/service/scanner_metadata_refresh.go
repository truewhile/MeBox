package service

import (
	"strings"

	"github.com/truewhile/MeBox/internal/model"
)

type scanDerivedMetadata struct {
	Title        string
	ScrapeStatus string
	Year         int
	ReleaseDate  string
	TMDbID       int
	BangumiID    int
	DoubanID     string
	TheTVDBID    string
	SeasonNum    int
	EpisodeNum   int
	// EpisodeFraction 是集号的小数部分（S01E11.5 → 0.5）：它变化时同样要刷新，
	// 否则已有行上的半集标记永远写不进去。
	EpisodeFraction float64
}

func localMetadataNeedsRefresh(existing existingLocalMedia, local *LocalMetadata) bool {
	if local == nil {
		return false
	}
	if localMetadataMarksMatched(local) && strings.TrimSpace(existing.ScrapeStatus) != "matched" {
		return true
	}
	if local.Title != "" && strings.TrimSpace(existing.Title) != strings.TrimSpace(local.Title) {
		return true
	}
	if local.OriginalName != "" && strings.TrimSpace(existing.OriginalName) != strings.TrimSpace(local.OriginalName) {
		return true
	}
	if local.EpisodeTitle != "" && strings.TrimSpace(existing.EpisodeTitle) != strings.TrimSpace(local.EpisodeTitle) {
		return true
	}
	if local.AdultCode != "" && !strings.EqualFold(strings.TrimSpace(existing.OriginalName), strings.TrimSpace(local.AdultCode)) {
		return true
	}
	if local.Year > 0 && existing.Year != local.Year {
		return true
	}
	if local.ReleaseDate != "" && strings.TrimSpace(existing.ReleaseDate) != strings.TrimSpace(local.ReleaseDate) {
		return true
	}
	if local.Overview != "" && strings.TrimSpace(existing.Overview) != strings.TrimSpace(local.Overview) {
		return true
	}
	if local.Rating > 0 && existing.Rating != local.Rating {
		return true
	}
	if local.PosterURL != "" && strings.TrimSpace(existing.PosterURL) != strings.TrimSpace(local.PosterURL) {
		return true
	}
	if local.BackdropURL != "" && strings.TrimSpace(existing.BackdropURL) != strings.TrimSpace(local.BackdropURL) {
		return true
	}
	if local.TMDbID > 0 && existing.TMDbID != local.TMDbID {
		return true
	}
	if local.BangumiID > 0 && existing.BangumiID != local.BangumiID {
		return true
	}
	if local.DoubanID != "" && strings.TrimSpace(existing.DoubanID) != strings.TrimSpace(local.DoubanID) {
		return true
	}
	if local.TheTVDBID != "" && strings.TrimSpace(existing.TheTVDBID) != strings.TrimSpace(local.TheTVDBID) {
		return true
	}
	if (local.SeasonNum > 0 || local.EpisodeNum > 0) && existing.SeasonNum != local.SeasonNum {
		return true
	}
	if local.EpisodeNum > 0 && existing.EpisodeNum != local.EpisodeNum {
		return true
	}
	if local.Genres != "" && strings.TrimSpace(existing.Genres) != strings.TrimSpace(local.Genres) {
		return true
	}
	if local.Countries != "" && strings.TrimSpace(existing.Countries) != strings.TrimSpace(local.Countries) {
		return true
	}
	if local.Languages != "" && strings.TrimSpace(existing.Languages) != strings.TrimSpace(local.Languages) {
		return true
	}
	return local.NSFW && !existing.NSFW
}

func localDerivedMetadataNeedsRefresh(existing existingLocalMedia, incoming *model.Media) bool {
	if incoming == nil {
		return false
	}
	if incoming.LibraryRootID != "" && incoming.LibraryRootID != existing.LibraryRootID {
		return true
	}
	if incoming.RelativePath != "" && incoming.RelativePath != existing.RelativePath {
		return true
	}
	return scanDerivedMetadataNeedsRefresh(scanDerivedMetadata{
		Title:           existing.Title,
		ScrapeStatus:    existing.ScrapeStatus,
		Year:            existing.Year,
		ReleaseDate:     existing.ReleaseDate,
		TMDbID:          existing.TMDbID,
		BangumiID:       existing.BangumiID,
		DoubanID:        existing.DoubanID,
		TheTVDBID:       existing.TheTVDBID,
		SeasonNum:       existing.SeasonNum,
		EpisodeNum:      existing.EpisodeNum,
		EpisodeFraction: existing.EpisodeFraction,
	}, incoming)
}

func scanDerivedMetadataNeedsRefresh(existing scanDerivedMetadata, incoming *model.Media) bool {
	status := strings.TrimSpace(existing.ScrapeStatus)
	enrichable := status == "" || status == "pending" || status == "no_match"
	if enrichable && strings.TrimSpace(incoming.Title) != "" && !strings.EqualFold(strings.TrimSpace(existing.Title), strings.TrimSpace(incoming.Title)) {
		return true
	}
	if enrichable && incoming.Year > 0 && existing.Year != incoming.Year {
		return true
	}
	if enrichable && incoming.ReleaseDate != "" && strings.TrimSpace(existing.ReleaseDate) != strings.TrimSpace(incoming.ReleaseDate) {
		return true
	}
	if (incoming.SeasonNum > 0 || incoming.EpisodeNum > 0) && existing.SeasonNum != incoming.SeasonNum {
		return true
	}
	if incoming.EpisodeNum > 0 && existing.EpisodeNum != incoming.EpisodeNum {
		return true
	}
	if (incoming.SeasonNum > 0 || incoming.EpisodeNum > 0) && existing.EpisodeFraction != incoming.EpisodeFraction {
		return true
	}
	if incoming.TMDbID > 0 && existing.TMDbID != incoming.TMDbID {
		return true
	}
	if incoming.BangumiID > 0 && existing.BangumiID != incoming.BangumiID {
		return true
	}
	if strings.TrimSpace(incoming.DoubanID) != "" && strings.TrimSpace(existing.DoubanID) != strings.TrimSpace(incoming.DoubanID) {
		return true
	}
	return strings.TrimSpace(incoming.TheTVDBID) != "" && strings.TrimSpace(existing.TheTVDBID) != strings.TrimSpace(incoming.TheTVDBID)
}

func cloudSeriesTitleFromMediaPath(mediaPath string) (string, int) {
	displayPath := strings.TrimSpace(mediaPath)
	if strings.HasPrefix(strings.ToLower(displayPath), "cloud://") {
		rest := strings.TrimPrefix(displayPath, "cloud://")
		if idx := strings.Index(rest, "/"); idx >= 0 {
			displayPath = rest[idx+1:]
		} else {
			return "", 0
		}
	}
	displayPath = strings.Trim(strings.ReplaceAll(displayPath, "\\", "/"), "/")
	if displayPath == "" {
		return "", 0
	}
	parts := strings.Split(displayPath, "/")
	if len(parts) < 2 {
		return "", 0
	}
	dirs := parts[:len(parts)-1]
	if len(dirs) == 0 {
		return "", 0
	}
	base := strings.TrimSpace(dirs[len(dirs)-1])
	usedSeasonFolder := false
	if _, ok := seasonFromDir(base); ok {
		usedSeasonFolder = true
		dirs = dirs[:len(dirs)-1]
		if len(dirs) == 0 {
			return "", 0
		}
		base = strings.TrimSpace(dirs[len(dirs)-1])
	}
	if base == "" || (!usedSeasonFolder && len(dirs) < 2) {
		return "", 0
	}
	title, year := CleanQuery(base)
	if title == "" {
		title = base
	}
	return strings.TrimSpace(title), year
}
