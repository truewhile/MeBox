package service

import (
	"fmt"
	"math"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestSortedMediaPageIncludesRowsBeyondSearchLimit(t *testing.T) {
	db := newServiceTestDB(t)
	repos := repository.New(db)
	lib := model.Library{Name: "Large", Path: "/media/large", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	// This must cross the historical 50,000-row boundary: a smaller fixture
	// cannot detect truncation before grouping, counting, and sorting.
	now := time.Now()
	for start := 0; start <= maxMediaSearchLimit; start += 500 {
		var rows []model.Media
		for i := start; i < min(start+500, maxMediaSearchLimit+1); i++ {
			rows = append(rows, model.Media{
				Base:      model.Base{ID: fmt.Sprintf("large-%05d", i), CreatedAt: now, UpdatedAt: now},
				LibraryID: lib.ID, Path: fmt.Sprintf("/media/large/%05d.mkv", i),
				Title: fmt.Sprintf("Movie %05d", i), TMDbID: i + 1,
			})
		}
		if err := db.CreateInBatches(&rows, 100).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos)
	items, total, err := svc.ListMediaVisibleSortedPage(t.Context(), lib.ID, maxMediaSearchLimit/100+1, 100,
		MediaVisibility{IncludeNSFW: true}, MediaListFilters{}, MediaSortSpec{Field: "title", Order: "asc"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if total != int64(maxMediaSearchLimit+1) || len(items) != 1 || items[0].ID != "large-50000" {
		t.Fatalf("total=%d, page length=%d, want 50001 rows and final item large-50000", total, len(items))
	}
}

func TestSortedMediaPagesReuseIndicesAndRefreshAfterInvalidation(t *testing.T) {
	db := newServiceTestDB(t)
	repos := repository.New(db)
	lib := model.Library{Name: "Movies", Path: "/media/movies", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	rows := []model.Media{
		{Base: model.Base{ID: "alpha"}, LibraryID: lib.ID, Path: "/media/movies/a.mkv", Title: "Alpha", TMDbID: 1, SizeBytes: 100},
		{Base: model.Base{ID: "alpha-alt"}, LibraryID: lib.ID, Path: "/media/movies/a-alt.mkv", Title: "Alpha", TMDbID: 1, SizeBytes: 50},
		{Base: model.Base{ID: "beta"}, LibraryID: lib.ID, Path: "/media/movies/b.mkv", Title: "Beta", TMDbID: 2},
		{Base: model.Base{ID: "hidden"}, LibraryID: lib.ID, Path: "/media/movies/hidden.mkv", Title: "Hidden", TMDbID: 3, NSFW: true},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos).
		SetRuntimeCache(NewRuntimeCacheService(&config.Config{}, zap.NewNop()))
	visibility := MediaVisibility{IncludeNSFW: false}
	spec := MediaSortSpec{Field: "title", Order: "asc"}
	first, total, err := svc.ListMediaVisibleSortedPage(t.Context(), lib.ID, 1, 1, visibility, MediaListFilters{}, spec, nil)
	if err != nil || total != 2 || len(first) != 1 || first[0].ID != "alpha" || len(first[0].Versions) != 2 {
		t.Fatalf("first=%#v, total=%d, error=%v", first, total, err)
	}
	grouped, err := svc.groupedMediaVisibleFiltered(t.Context(), lib.ID, visibility, MediaListFilters{})
	if err != nil {
		t.Fatal(err)
	}
	index := svc.sortedMediaIndices(grouped, spec, nil)
	second, _, err := svc.ListMediaVisibleSortedPage(t.Context(), lib.ID, 2, 1, visibility, MediaListFilters{}, spec, nil)
	if err != nil || len(second) != 1 || second[0].ID != "beta" {
		t.Fatalf("second=%#v, error=%v", second, err)
	}
	cachedIndex := svc.sortedMediaIndices(grouped, spec, nil)
	if &index[0] != &cachedIndex[0] {
		t.Fatal("pagination rebuilt the sorted position index")
	}
	// The returned page is independently writable without corrupting cached rows.
	first[0].Title = "Modified by caller"
	if err := db.Model(&model.Media{}).Where("id = ?", "beta").Update("title", "Aardvark").Error; err != nil {
		t.Fatal(err)
	}
	svc.invalidateMediaCache(t.Context())
	fresh, _, err := svc.ListMediaVisibleSortedPage(t.Context(), lib.ID, 1, 1, visibility, MediaListFilters{}, spec, nil)
	if err != nil || len(fresh) != 1 || fresh[0].ID != "beta" {
		t.Fatalf("updated page=%#v, error=%v", fresh, err)
	}
	empty, _, err := svc.ListMediaVisibleSortedPage(t.Context(), lib.ID, math.MaxInt, 100, visibility, MediaListFilters{}, spec, nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("out-of-range page=%#v, error=%v", empty, err)
	}
}

func TestMediaSortIndexDoesNotCachePersonalPlaybackHistory(t *testing.T) {
	svc := NewMediaService(&config.Config{}, zap.NewNop(), nil).
		SetRuntimeCache(NewRuntimeCacheService(&config.Config{}, zap.NewNop()))
	grouped := &groupedMediaCacheValue{Revision: "history-test", Items: []MediaItem{
		{Media: model.Media{Base: model.Base{ID: "a"}}},
		{Media: model.Media{Base: model.Base{ID: "b"}}},
	}}
	spec := MediaSortSpec{Field: "last_played", Order: "desc"}
	first := svc.sortedMediaIndices(grouped, spec, map[string]time.Time{"a": time.Now()})
	second := svc.sortedMediaIndices(grouped, spec, map[string]time.Time{"b": time.Now()})
	if first[0] != 0 || second[0] != 1 {
		t.Fatalf("history sorts = %v, %v", first, second)
	}
}

func BenchmarkMediaSortIndex(b *testing.B) {
	items := make([]MediaItem, 10000)
	for i := range items {
		items[i].ID = fmt.Sprint(i)
		items[i].Title = fmt.Sprintf("Movie %05d", len(items)-i)
	}
	spec := MediaSortSpec{Field: "title", Order: "asc"}
	b.Run("sort-every-page", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			SortMediaItems(items, spec.Field, spec.Order, nil)
		}
	})
	b.Run("cached-position-index", func(b *testing.B) {
		svc := NewMediaService(&config.Config{}, zap.NewNop(), nil).
			SetRuntimeCache(NewRuntimeCacheService(&config.Config{}, zap.NewNop()))
		grouped := &groupedMediaCacheValue{Items: items, Revision: "benchmark"}
		svc.sortedMediaIndices(grouped, spec, nil)
		b.ReportAllocs()
		for b.Loop() {
			svc.sortedMediaIndices(grouped, spec, nil)
		}
	})
}
