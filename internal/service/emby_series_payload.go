package service

func (e *EmbyService) seriesPayload(group embySeriesGroup) map[string]any {
	e.rememberSeriesGroup(group)
	imageTags := map[string]string{}
	backdropTags := []string{}
	if group.PosterURL != "" {
		imageTags["Primary"] = embyVirtualImageTag(group.ID, embyVirtualPrimaryTagSuffix)
	}
	if group.BackdropURL != "" || group.PosterURL != "" {
		backdropTags = append(backdropTags, embyVirtualImageTag(group.ID, embyVirtualBackdropTagSuffix))
	}
	lastMediaAdded := group.DateLastMediaAdded
	if lastMediaAdded.IsZero() {
		lastMediaAdded = group.CreatedAt
	}
	item := map[string]any{
		"Id":                 group.ID,
		"Name":               group.Name,
		"ServerId":           embyServerID,
		"Type":               "Series",
		"MediaType":          "Video",
		"IsFolder":           true,
		"ParentId":           group.LibraryID,
		"ProductionYear":     group.Year,
		"Overview":           group.Overview,
		"CommunityRating":    group.Rating,
		"RecursiveItemCount": len(group.Episodes),
		"ChildCount":         len(e.seasonsForSeries(group)),
		"DateCreated":        group.CreatedAt,
		"DateLastMediaAdded": lastMediaAdded,
		"ImageTags":          imageTags,
		"BackdropImageTags":  backdropTags,
		"People":             []map[string]any{},
		"ProviderIds": map[string]string{
			"Tmdb":    intToStr(group.TMDbID),
			"Bangumi": intToStr(group.BangumiID),
		},
		"UserData": emptyUserData(),
	}
	if group.PosterURL != "" {
		item["PrimaryImageTag"] = embyVirtualImageTag(group.ID, embyVirtualPrimaryTagSuffix)
	}
	if premiered, ok := embyPremiereDate(group.ReleaseDate); ok {
		item["PremiereDate"] = premiered
	}
	return item
}

func (e *EmbyService) seasonPayload(season embySeasonGroup) map[string]any {
	e.rememberSeasonGroup(season)
	imageTags := map[string]string{}
	backdropTags := []string{}
	if season.Series.PosterURL != "" {
		imageTags["Primary"] = embyVirtualImageTag(season.ID, embyVirtualPrimaryTagSuffix)
	}
	if season.Series.BackdropURL != "" || season.Series.PosterURL != "" {
		backdropTags = append(backdropTags, embyVirtualImageTag(season.ID, embyVirtualBackdropTagSuffix))
	}
	item := map[string]any{
		"Id":                season.ID,
		"Name":              season.Name,
		"ServerId":          embyServerID,
		"Type":              "Season",
		"MediaType":         "Video",
		"IsFolder":          true,
		"ParentId":          season.SeriesID,
		"SeriesId":          season.SeriesID,
		"SeriesName":        season.Series.Name,
		"IndexNumber":       season.SeasonNum,
		"ChildCount":        len(season.Episodes),
		"ImageTags":         imageTags,
		"BackdropImageTags": backdropTags,
		"People":            []map[string]any{},
		"UserData":          emptyUserData(),
	}
	if season.Series.PosterURL != "" {
		item["PrimaryImageTag"] = embyVirtualImageTag(season.ID, embyVirtualPrimaryTagSuffix)
		item["SeriesPrimaryImageTag"] = embyVirtualImageTag(season.Series.ID, embyVirtualPrimaryTagSuffix)
	}
	if season.Series.BackdropURL != "" || season.Series.PosterURL != "" {
		item["ParentBackdropItemId"] = season.Series.ID
		item["ParentBackdropImageTags"] = []string{embyVirtualImageTag(season.Series.ID, embyVirtualBackdropTagSuffix)}
	}
	return item
}
