package service

import (
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

func TestGroupMediaSeriesItemsKeepsLibrariesSeparate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		media model.Media
	}{
		{"remote series with TMDb ID", model.Media{TMDbID: 61709}},
		{"remote series with Bangumi ID", model.Media{BangumiID: 61709}},
		{"flat episode with TMDb ID", model.Media{SeasonNum: 1, EpisodeNum: 1, TMDbID: 61709}},
		{"flat episode with Bangumi ID", model.Media{SeasonNum: 1, EpisodeNum: 1, BangumiID: 61709}},
		{"flat episode with Douban ID", model.Media{SeasonNum: 1, EpisodeNum: 1, DoubanID: "61709"}},
		{"flat episode with TVDB ID", model.Media{SeasonNum: 1, EpisodeNum: 1, TheTVDBID: "61709"}},
		{"shared series ID", model.Media{SeriesID: "same-series"}},
		{"same title and directory", model.Media{Path: "/media/anime/龙珠改/Season 1/ep1.mkv", SeasonNum: 1, EpisodeNum: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, second := tc.media, tc.media
			first.ID, first.LibraryID, first.LibraryName, first.Title = "first", "anime-a", "番剧库甲", "龙珠改"
			second.ID, second.LibraryID, second.LibraryName, second.Title = "second", "anime-b", "番剧库乙", "龙珠改"
			if strings.HasPrefix(tc.name, "remote") {
				first.ID = EncodeEmbyRemoteID("mount-a", "same-id")
				second.ID = EncodeEmbyRemoteID("mount-b", "same-id")
			}

			items := GroupMediaSeriesItems([]model.Media{first, second})
			if len(items) != 2 {
				t.Fatalf("got %d search results, want one per library: %#v", len(items), items)
			}
			seen := make(map[string]bool)
			for _, item := range items {
				seen[item.LibraryID] = true
				if item.Title != "龙珠改" || item.LibraryName == "" {
					t.Fatalf("search result lost title or library metadata: %#v", item)
				}
			}
			if !seen[first.LibraryID] || !seen[second.LibraryID] {
				t.Fatalf("search results lost a library: %#v", items)
			}
		})
	}
}

func TestGroupMediaSeriesItemsFoldsEpisodesWithinDisplayLibrary(t *testing.T) {
	items := []model.Media{
		{Base: model.Base{ID: "a-2"}, LibraryID: "source-a", DisplayLibraryID: "anime-a", Title: "龙珠改", Path: "/media/anime/龙珠改/Season 1/ep2.mkv", SeasonNum: 1, EpisodeNum: 2, TMDbID: 61709},
		{Base: model.Base{ID: "a-1"}, LibraryID: "source-b", DisplayLibraryID: "anime-a", Title: "龙珠改", Path: "/media/anime/龙珠改/Season 1/ep1.mkv", SeasonNum: 1, EpisodeNum: 1, TMDbID: 61709},
		{Base: model.Base{ID: "b-2"}, LibraryID: "source-a", DisplayLibraryID: "anime-b", Title: "龙珠改", Path: "/media/anime/龙珠改/Season 1/ep2.mkv", SeasonNum: 1, EpisodeNum: 2, TMDbID: 61709},
		{Base: model.Base{ID: "b-1"}, LibraryID: "source-a", DisplayLibraryID: "anime-b", Title: "龙珠改", Path: "/media/anime/龙珠改/Season 1/ep1.mkv", SeasonNum: 1, EpisodeNum: 1, TMDbID: 61709},
	}
	grouped := GroupMediaSeriesItems(items)
	if len(grouped) != 2 {
		t.Fatalf("got %d results, want one series per display library: %#v", len(grouped), grouped)
	}
	seen := make(map[string]bool)
	for _, item := range grouped {
		if item.EpisodeNum != 1 || seen[item.DisplayLibraryID] {
			t.Fatalf("expected the first episode from each display library: %#v", grouped)
		}
		seen[item.DisplayLibraryID] = true
	}
}
