package service

import (
	"testing"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

func TestSortMediaItemsTitleAscEmptyLast(t *testing.T) {
	items := []MediaItem{
		{Media: model.Media{Base: model.Base{ID: "b"}, Title: "Beta"}},
		{Media: model.Media{Base: model.Base{ID: "empty"}}},
		{Media: model.Media{Base: model.Base{ID: "a"}, Title: "alpha"}},
	}
	got := SortMediaItems(items, "title", "asc", nil)
	if got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "empty" {
		t.Fatalf("title order = %q, %q, %q", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestSortMediaItemsReleaseDescEmptyLast(t *testing.T) {
	items := []MediaItem{
		{Media: model.Media{Base: model.Base{ID: "old"}, ReleaseDate: "2020-01-01"}},
		{Media: model.Media{Base: model.Base{ID: "empty"}}},
		{Media: model.Media{Base: model.Base{ID: "new"}, ReleaseDate: "2024-01-01"}},
	}
	got := SortMediaItems(items, "release_date", "desc", nil)
	if got[0].ID != "new" || got[1].ID != "old" || got[2].ID != "empty" {
		t.Fatalf("release order = %q, %q, %q", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestSortSeriesCardsTitleAsc(t *testing.T) {
	cards := []SeriesCard{
		{Key: "b", Rep: model.Media{Base: model.Base{ID: "b"}, Title: "Beta"}},
		{Key: "a", Rep: model.Media{Base: model.Base{ID: "a"}, Title: "alpha"}},
	}
	got := SortSeriesCards(cards, "title", "asc", nil)
	if got[0].Key != "a" || got[1].Key != "b" {
		t.Fatalf("series order = %q, %q", got[0].Key, got[1].Key)
	}
}

func TestSortSeriesCardsUpdatedAtDoesNotFallbackToMediaDate(t *testing.T) {
	olderMediaDate := time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC)
	newerMediaDate := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	cards := []SeriesCard{
		{
			Key: "remote-first",
			Rep: model.Media{Base: model.Base{
				ID:        "remote-first",
				CreatedAt: olderMediaDate,
				UpdatedAt: olderMediaDate,
			}},
		},
		{
			Key: "remote-second",
			Rep: model.Media{Base: model.Base{
				ID:        "remote-second",
				CreatedAt: newerMediaDate,
				UpdatedAt: newerMediaDate,
			}},
		},
	}

	got := SortSeriesCards(cards, "updated_at", "desc", nil)
	if got[0].Key != "remote-first" || got[1].Key != "remote-second" {
		t.Fatalf("series order = %q, %q, want remote-provided order", got[0].Key, got[1].Key)
	}
}

func TestSortSeriesCardsUpdatedAtSortsKnownLastEpisodeDates(t *testing.T) {
	older := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	cards := []SeriesCard{
		{Key: "older", LastAddedAt: &older},
		{Key: "newer", LastAddedAt: &newer},
	}

	got := SortSeriesCards(cards, "updated_at", "desc", nil)
	if got[0].Key != "newer" || got[1].Key != "older" {
		t.Fatalf("series order = %q, %q", got[0].Key, got[1].Key)
	}
}
