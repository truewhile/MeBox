package service

import "github.com/truewhile/MeBox/internal/model"

func applyLocalMetadata(m *model.Media, local *LocalMetadata) {
	applyLocalIdentityMetadata(m, local)
	applyLocalArtworkMetadata(m, local)
	applyLocalExternalIDMetadata(m, local)
	applyLocalEpisodeMetadata(m, local)
	applyLocalTaxonomyMetadata(m, local)
	if local.NSFW {
		m.NSFW = true
	}
	if localMetadataMarksMatched(local) {
		m.ScrapeStatus = "matched"
	}
}

func applyLocalIdentityMetadata(m *model.Media, local *LocalMetadata) {
	if local.Title != "" {
		m.Title = local.Title
	}
	if local.OriginalName != "" {
		m.OriginalName = local.OriginalName
	}
	if local.AdultCode != "" {
		m.OriginalName = local.AdultCode
	}
	if local.Year > 0 {
		m.Year = local.Year
	}
	if local.ReleaseDate != "" {
		m.ReleaseDate = local.ReleaseDate
	}
	if local.Overview != "" {
		m.Overview = local.Overview
	}
	if local.Rating > 0 {
		m.Rating = local.Rating
	}
}

func applyLocalArtworkMetadata(m *model.Media, local *LocalMetadata) {
	if local.PosterURL != "" {
		m.PosterURL = local.PosterURL
	}
	if local.BackdropURL != "" {
		m.BackdropURL = local.BackdropURL
	}
}

func applyLocalExternalIDMetadata(m *model.Media, local *LocalMetadata) {
	if local.TMDbID > 0 {
		m.TMDbID = local.TMDbID
	}
	if local.BangumiID > 0 {
		m.BangumiID = local.BangumiID
	}
	if local.DoubanID != "" {
		m.DoubanID = local.DoubanID
	}
	if local.TheTVDBID != "" {
		m.TheTVDBID = local.TheTVDBID
	}
}

func applyLocalEpisodeMetadata(m *model.Media, local *LocalMetadata) {
	if local.EpisodeTitle != "" {
		m.EpisodeTitle = local.EpisodeTitle
	}
	// tvshow.nfo 里常带 scraper 写的 <season>-1</season> / <episode>-1</episode>
	// 哨兵（表示「整剧级、不适用」）。负季号不是有效季号：一旦让它覆盖，该集会被
	// 写成 -1，网页端按 seasonLabel(-1) 显示成「剧场版」。因此负值一律忽略，
	// 保留文件名解析出来的季号（S00Exx 会被解析成第 0 季 = 特别篇）。
	if local.SeasonNum >= 0 && (local.SeasonNum > 0 || local.EpisodeNum > 0) {
		m.SeasonNum = local.SeasonNum
	}
	if local.EpisodeNum > 0 {
		m.EpisodeNum = local.EpisodeNum
	}
}

func applyLocalTaxonomyMetadata(m *model.Media, local *LocalMetadata) {
	if local.Genres != "" {
		m.Genres = local.Genres
	}
	if local.Countries != "" {
		m.Countries = local.Countries
	}
	if local.Languages != "" {
		m.Languages = local.Languages
	}
}

func localMetadataMarksMatched(local *LocalMetadata) bool {
	return local != nil && (local.HasNFO || (!local.PathHint && localHasDescriptiveMetadata(local)))
}

func localHasDescriptiveMetadata(local *LocalMetadata) bool {
	if local == nil {
		return false
	}
	return local.Title != "" ||
		local.OriginalName != "" ||
		local.EpisodeTitle != "" ||
		local.AdultCode != "" ||
		local.Year > 0 ||
		local.ReleaseDate != "" ||
		local.Overview != "" ||
		local.Rating > 0 ||
		local.TMDbID > 0 ||
		local.BangumiID > 0 ||
		local.DoubanID != "" ||
		local.TheTVDBID != "" ||
		local.Genres != "" ||
		local.Countries != "" ||
		local.Languages != ""
}
