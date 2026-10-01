package service

import (
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// Emby/Jellyfin 对「不适用」的字段不下发（JSON 里没有该键），而不是下发空串。
// 小幻影视（RodelPlayer）2.2610 会把电影条目的 "SeasonId": "" 原样传进播放准备
// 逻辑并抛 ArgumentException（Parameter 'seasonId'），导致整个起播失败。
func TestEmbyMovieItemOmitsEmptySeasonAndSeriesFields(t *testing.T) {
	svc := newTestEmbyService(t)
	lib := model.Library{Name: "小姐姐", Path: `/media/小姐姐`, Type: "movie", Enabled: true}
	if err := svc.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	movie := model.Media{
		Base:      model.Base{ID: "movie-strm"},
		LibraryID: lib.ID,
		Title:     "SNIS-919",
		Path:      `/media/小姐姐/三上悠亚/SNIS-919/SNIS-919.strm`,
		Container: "strm",
		STRMURL:   "/api/strm/play/cloud115/video.mkv?acct=a1&pickcode=pc1",
	}
	if err := svc.repo.DB.Create(&movie).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}

	item := svc.itemPayload(t.Context(), &movie, false, 0, false)
	if item["Type"] != "Movie" {
		t.Fatalf("Type = %#v, want Movie", item["Type"])
	}
	for _, key := range []string{"SeasonId", "SeriesId", "SeasonName", "SeriesName"} {
		if value, ok := item[key]; ok {
			t.Fatalf("%s must be omitted for a movie, got %#v", key, value)
		}
	}
}

// 剧集的季/剧归属是真实存在的，不能被一起删掉。
func TestEmbyEpisodeItemKeepsSeasonAndSeriesFields(t *testing.T) {
	svc := newTestEmbyService(t)
	lib := model.Library{Name: "剧集", Path: `/media/tv`, Type: "tv", Enabled: true}
	if err := svc.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	episode := model.Media{
		Base:       model.Base{ID: "ep-1"},
		LibraryID:  lib.ID,
		Title:      "命运石之门",
		Path:       `/media/tv/命运石之门/Season 01/命运石之门 - S01E01.mkv`,
		SeasonNum:  1,
		EpisodeNum: 1,
	}
	if err := svc.repo.DB.Create(&episode).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}

	item := svc.itemPayload(t.Context(), &episode, false, 0, false)
	if item["Type"] != "Episode" {
		t.Fatalf("Type = %#v, want Episode", item["Type"])
	}
	for _, key := range []string{"SeasonId", "SeriesId", "SeasonName", "SeriesName"} {
		value, _ := item[key].(string)
		if value == "" {
			t.Fatalf("%s = %#v, want a non-empty value for an episode", key, item[key])
		}
	}
}

func TestDropEmptyStringFields(t *testing.T) {
	item := map[string]any{
		"SeasonId":  "",
		"SeriesId":  "   ",
		"SeriesName": "命运石之门",
		"IndexNumber": 0,
	}
	dropEmptyStringFields(item, "SeasonId", "SeriesId", "SeriesName")
	if _, ok := item["SeasonId"]; ok {
		t.Fatalf("empty SeasonId must be removed")
	}
	if _, ok := item["SeriesId"]; ok {
		t.Fatalf("whitespace-only SeriesId must be removed")
	}
	if item["SeriesName"] != "命运石之门" {
		t.Fatalf("non-empty SeriesName must be kept, got %#v", item["SeriesName"])
	}
	if item["IndexNumber"] != 0 {
		t.Fatalf("non-string fields must be left alone, got %#v", item["IndexNumber"])
	}
}
