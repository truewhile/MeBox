package service

import (
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// seasonIndexFixture builds one series covering every season-numbering class a
// client can ask for: two regular seasons, a generic specials bucket (season 0)
// and an OVA folder (negative season).
func seasonIndexFixture(t *testing.T) (*EmbyService, string) {
	t.Helper()
	svc := newTestEmbyService(t)
	lib := model.Library{Name: "剧集", Path: `F:\media\剧集`, Type: "tv", Enabled: true}
	if err := svc.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	rows := []model.Media{
		{
			Base:       model.Base{ID: "s1e1"},
			LibraryID:  lib.ID,
			Title:      "權力的遊戲",
			Path:       `F:\media\剧集\權力的遊戲 (2011)\Season 01\權力的遊戲 - S01E01.mkv`,
			SeasonNum:  1,
			EpisodeNum: 1,
		},
		{
			Base:       model.Base{ID: "s1e2"},
			LibraryID:  lib.ID,
			Title:      "權力的遊戲",
			Path:       `F:\media\剧集\權力的遊戲 (2011)\Season 01\權力的遊戲 - S01E02.mkv`,
			SeasonNum:  1,
			EpisodeNum: 2,
		},
		{
			Base:       model.Base{ID: "s2e1"},
			LibraryID:  lib.ID,
			Title:      "權力的遊戲",
			Path:       `F:\media\剧集\權力的遊戲 (2011)\Season 02\權力的遊戲 - S02E01.mkv`,
			SeasonNum:  2,
			EpisodeNum: 1,
		},
		{
			Base:       model.Base{ID: "spec1"},
			LibraryID:  lib.ID,
			Title:      "權力的遊戲",
			Path:       `F:\media\剧集\權力的遊戲 (2011)\Specials\權力的遊戲 - S00E01.mkv`,
			SeasonNum:  0,
			EpisodeNum: 1,
		},
		{
			Base:       model.Base{ID: "ova1"},
			LibraryID:  lib.ID,
			Title:      "權力的遊戲",
			Path:       `F:\media\剧集\權力的遊戲 (2011)\OVA\權力的遊戲 - OVA.mkv`,
			SeasonNum:  0,
			EpisodeNum: 1,
		},
	}
	for i := range rows {
		if err := svc.repo.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("create media: %v", err)
		}
	}

	items, err := svc.Items(t.Context(), ItemsParams{
		ParentID:         lib.ID,
		IncludeItemTypes: []string{"Series"},
		Limit:            10,
	})
	if err != nil {
		t.Fatalf("series items: %v", err)
	}
	cards := items["Items"].([]map[string]any)
	if len(cards) != 1 {
		t.Fatalf("want one series card, got %#v", cards)
	}
	return svc, cards[0]["Id"].(string)
}

func episodeIDs(t *testing.T, out map[string]any) []string {
	t.Helper()
	items, ok := out["Items"].([]map[string]any)
	if !ok {
		t.Fatalf("Items has unexpected type: %#v", out["Items"])
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item["Id"].(string))
	}
	return ids
}

// itemsTotal reads TotalRecordCount, which is int on the episode path and int64
// on the empty-envelope path.
func itemsTotal(t *testing.T, out map[string]any) int {
	t.Helper()
	switch v := out["TotalRecordCount"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	default:
		t.Fatalf("TotalRecordCount has unexpected type: %#v", out["TotalRecordCount"])
		return 0
	}
}

func assertEpisodeIDs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("episode ids = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("episode ids = %#v, want %#v", got, want)
		}
	}
}

// TestEmbyEpisodeItemsFilterBySeasonIndex is the regression guard for clients
// that scope episodes by season number (Season=N) instead of the virtual season
// id: without the filter every season was returned for any requested season.
func TestEmbyEpisodeItemsFilterBySeasonIndex(t *testing.T) {
	svc, seriesID := seasonIndexFixture(t)

	cases := []struct {
		name      string
		season    *int
		wantIDs   []string
		wantTotal int
	}{
		{
			name:   "unfiltered returns every episode",
			season: nil,
			// episodeItems sorts by the stored season/episode numbers, so the two
			// specials (both stored as season 0) come first in creation order.
			wantIDs:   []string{"spec1", "ova1", "s1e1", "s1e2", "s2e1"},
			wantTotal: 5,
		},
		{name: "season 1", season: intPtr(1), wantIDs: []string{"s1e1", "s1e2"}, wantTotal: 2},
		{name: "season 2", season: intPtr(2), wantIDs: []string{"s2e1"}, wantTotal: 1},
		{name: "season 0 is the specials bucket", season: intPtr(0), wantIDs: []string{"spec1"}, wantTotal: 1},
		{name: "OVA season", season: intPtr(embySeasonOVA), wantIDs: []string{"ova1"}, wantTotal: 1},
		{name: "unknown season is empty", season: intPtr(9), wantIDs: []string{}, wantTotal: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := svc.Items(t.Context(), ItemsParams{
				ParentID:         seriesID,
				IncludeItemTypes: []string{"Episode"},
				Recursive:        true,
				Limit:            50,
				SeasonIndex:      tc.season,
			})
			if err != nil {
				t.Fatalf("items: %v", err)
			}
			assertEpisodeIDs(t, episodeIDs(t, out), tc.wantIDs)
			if total := itemsTotal(t, out); total != tc.wantTotal {
				t.Fatalf("TotalRecordCount = %d, want %d", total, tc.wantTotal)
			}
		})
	}
}

// TestEmbySeasonIndexFilterKeepsSeasonIdsWorking pins the pre-existing
// SeasonId behaviour: resolving through the virtual season id must still return
// exactly that season.
func TestEmbySeasonIndexFilterKeepsSeasonIdsWorking(t *testing.T) {
	svc, seriesID := seasonIndexFixture(t)

	items, err := svc.Items(t.Context(), ItemsParams{ParentID: seriesID, Limit: 50})
	if err != nil {
		t.Fatalf("seasons: %v", err)
	}
	seasons := items["Items"].([]map[string]any)
	var seasonTwoID string
	for _, season := range seasons {
		if season["IndexNumber"] == 2 {
			seasonTwoID = season["Id"].(string)
		}
	}
	if seasonTwoID == "" {
		t.Fatalf("season 2 missing from payload: %#v", seasons)
	}

	out, err := svc.Items(t.Context(), ItemsParams{
		ParentID:         seasonTwoID,
		IncludeItemTypes: []string{"Episode"},
		Recursive:        true,
		Limit:            50,
	})
	if err != nil {
		t.Fatalf("season episodes: %v", err)
	}
	got := episodeIDs(t, out)
	assertEpisodeIDs(t, got, []string{"s2e1"})
}

// TestEmbyItemsCacheKeySeparatesSeasonFilters guards against a cached unfiltered
// response being reused for a season-scoped request (and vice versa).
func TestEmbyItemsCacheKeySeparatesSeasonFilters(t *testing.T) {
	svc := newTestEmbyService(t)
	base := ItemsParams{ParentID: "series-1", IncludeItemTypes: []string{"Episode"}, Limit: 50}

	unfiltered := svc.embyItemsCacheKey("items", base)
	seasonZero := svc.embyItemsCacheKey("items", withSeasonIndex(base, 0))
	seasonTwo := svc.embyItemsCacheKey("items", withSeasonIndex(base, 2))

	if unfiltered == seasonZero {
		t.Fatal("unfiltered and Season=0 keys must differ")
	}
	if unfiltered == seasonTwo {
		t.Fatal("unfiltered and Season=2 keys must differ")
	}
	if seasonZero == seasonTwo {
		t.Fatal("Season=0 and Season=2 keys must differ")
	}
	if again := svc.embyItemsCacheKey("items", withSeasonIndex(base, 2)); again != seasonTwo {
		t.Fatal("cache key must be stable for the same season filter")
	}
}

func withSeasonIndex(p ItemsParams, season int) ItemsParams {
	p.SeasonIndex = intPtr(season)
	return p
}

func intPtr(v int) *int { return &v }
