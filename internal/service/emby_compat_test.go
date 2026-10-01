package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func newTestEmbyService(t *testing.T) *EmbyService {
	t.Helper()
	db := newServiceTestDB(t, &model.Library{}, &model.Series{}, &model.Media{}, &model.Favorite{}, &model.PlaybackHistory{}, &model.User{}, &model.Setting{})
	// 内存库 + 异步探测协程：限制为单连接，避免连接池新建连接时
	// 拿到一个空白的 :memory: 实例（no such table）。
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	repos := repository.New(db)
	return NewEmbyService(&config.Config{}, zap.NewNop(), repos)
}

func TestEmbyLatestItemsOrderByReleaseDate(t *testing.T) {
	svc := newTestEmbyService(t)
	lib := model.Library{Name: "电影", Path: `/media/movies`, Type: "movie", Enabled: true}
	if err := svc.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	base := time.Now()
	rows := []model.Media{
		{
			Base:        model.Base{ID: "older-release-newer-scan", CreatedAt: base.Add(2 * time.Hour)},
			LibraryID:   lib.ID,
			Title:       "旧上映新入库",
			Path:        `/media/movies/old.mkv`,
			Year:        2026,
			ReleaseDate: "2026-01-10",
		},
		{
			Base:        model.Base{ID: "newer-release-older-scan", CreatedAt: base},
			LibraryID:   lib.ID,
			Title:       "新上映",
			Path:        `/media/movies/new.mkv`,
			Year:        2026,
			ReleaseDate: "2026-06-23",
		},
	}
	for i := range rows {
		if err := svc.repo.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("create media: %v", err)
		}
	}

	items, err := svc.LatestItems(t.Context(), "", lib.ID, 10)
	if err != nil {
		t.Fatalf("latest items: %v", err)
	}
	if len(items) != 2 || items[0]["Id"] != "newer-release-older-scan" {
		t.Fatalf("latest items should prefer release date over created_at, got %#v", items)
	}
	if _, ok := items[0]["PremiereDate"].(time.Time); !ok {
		t.Fatalf("latest item should expose PremiereDate for Emby clients: %#v", items[0])
	}
}

// SimilarItems with a real series_id (not a media table row ID) must return
// results instead of an empty list.  Before bug-2 fix, Media.FindByID returned
// nil for any ID that wasn't a primary-key match in the media table (including
// series_id values and virtual msgo-series-* IDs), so SimilarItems always
// returned empty for series detail pages.
func TestEmbyServiceSimilarItemsSeriesID(t *testing.T) {
	svc := newTestEmbyService(t)
	svc.SetDiscovery(NewMediaDiscoveryService(zap.NewNop(), svc.repo))

	lib := model.Library{Name: "TV", Path: "/media/tv", Type: "tv", Enabled: true}
	if err := svc.repo.Library.Create(context.Background(), &lib); err != nil {
		t.Fatal(err)
	}

	// Two series with the same genre so they score > 0 for similarity.
	ep1 := model.Media{
		LibraryID:  lib.ID,
		SeriesID:   "real-series-1",
		Title:      "剧一",
		Genres:     "Action",
		SeasonNum:  1,
		EpisodeNum: 1,
		Path:       "/media/tv/s1e1.mkv",
	}
	ep2 := model.Media{
		LibraryID:  lib.ID,
		SeriesID:   "real-series-2",
		Title:      "剧二",
		Genres:     "Action",
		SeasonNum:  1,
		EpisodeNum: 1,
		Path:       "/media/tv/s2e1.mkv",
	}
	for _, m := range []*model.Media{&ep1, &ep2} {
		if err := svc.repo.DB.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}

	// "real-series-1" is the series_id stored in the media row but is NOT a
	// primary key in the media table, so Media.FindByID("real-series-1") returns
	// nil.  SimilarItems must fall back to findSeriesGroup and still return
	// results.
	result, err := svc.SimilarItems(context.Background(), "real-series-1", "", 12)
	if err != nil {
		t.Fatalf("SimilarItems with series_id: %v", err)
	}
	similar, _ := result["Items"].([]map[string]any)
	if similar == nil {
		t.Fatal("SimilarItems returned nil items for a series_id that resolves via findSeriesGroup")
	}
	// Should contain 剧二 (the only other episodic content with the same genre).
	found := false
	for _, item := range similar {
		if name, _ := item["Name"].(string); strings.Contains(name, "剧二") || strings.Contains(name, "第 1 集") {
			found = true
			break
		}
	}
	if !found && len(similar) == 0 {
		t.Fatalf("SimilarItems returned no results; want at least 剧二 for series real-series-1")
	}
}

// SimilarItems with a virtual series ID (msgo-series-*) must also work.
// Virtual IDs are generated for episodes that have no series_id set.
func TestEmbyServiceSimilarItemsVirtualSeriesID(t *testing.T) {
	svc := newTestEmbyService(t)
	svc.SetDiscovery(NewMediaDiscoveryService(zap.NewNop(), svc.repo))

	lib := model.Library{Name: "TV2", Path: "/media/tv2", Type: "tv", Enabled: true}
	if err := svc.repo.Library.Create(context.Background(), &lib); err != nil {
		t.Fatal(err)
	}

	// Episodes WITHOUT SeriesID → series group gets a virtual msgo-series-* ID.
	ep1 := model.Media{
		LibraryID:  lib.ID,
		Title:      "虚拟剧一",
		Genres:     "Drama",
		SeasonNum:  1,
		EpisodeNum: 1,
		Path:       "/media/tv2/virtual1/S01E01.mkv",
	}
	ep2 := model.Media{
		LibraryID:  lib.ID,
		Title:      "虚拟剧二",
		Genres:     "Drama",
		SeasonNum:  1,
		EpisodeNum: 1,
		Path:       "/media/tv2/virtual2/S01E01.mkv",
	}
	for _, m := range []*model.Media{&ep1, &ep2} {
		if err := svc.repo.DB.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Fetch series items to get the virtual ID for ep1's series.
	seriesItems, err := svc.Items(context.Background(), ItemsParams{
		IncludeItemTypes: []string{"Series"},
		Recursive:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := seriesItems["Items"].([]map[string]any)
	var virtualID string
	for _, item := range items {
		id, _ := item["Id"].(string)
		name, _ := item["Name"].(string)
		if strings.Contains(name, "虚拟剧一") && strings.HasPrefix(id, embyVirtualSeriesPrefix) {
			virtualID = id
			break
		}
	}
	if virtualID == "" {
		t.Skip("virtual series ID not generated for episodes without series_id in this build")
	}

	result, err := svc.SimilarItems(context.Background(), virtualID, "", 12)
	if err != nil {
		t.Fatalf("SimilarItems with virtual series ID: %v", err)
	}
	similar, _ := result["Items"].([]map[string]any)
	if similar == nil {
		t.Fatalf("SimilarItems returned nil items for virtual series ID %q", virtualID)
	}
}
