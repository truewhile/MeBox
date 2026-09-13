package service

import (
	"sort"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

// MediaSortSpec is the normalized sort request shared by media and series endpoints.
type MediaSortSpec struct {
	Field string
	Order string
}

// NormalizeMediaSort validates the request against the same fields the web UI
// exposes. Unknown values fall back to the historical release-date ordering.
func NormalizeMediaSort(field, order string) MediaSortSpec {
	field = strings.ToLower(strings.TrimSpace(field))
	switch field {
	case "title", "release_date", "year", "created_at", "updated_at", "rating", "duration", "bitrate", "last_played", "random":
	case "imdb_rating":
		field = "rating"
	default:
		field = "release_date"
	}

	order = strings.ToLower(strings.TrimSpace(order))
	if order != "asc" && order != "desc" {
		if field == "title" {
			order = "asc"
		} else {
			order = "desc"
		}
	}
	if field == "random" {
		// The web client shuffles the complete result so pagination stays stable.
		field = "release_date"
		order = "desc"
	}
	return MediaSortSpec{Field: field, Order: order}
}

// SortMediaItems returns a sorted copy of the grouped media list. The input is
// often backed by an immutable runtime cache, so callers must not rely on the
// returned slice aliasing it.
func SortMediaItems(items []MediaItem, field, order string, history map[string]time.Time) []MediaItem {
	spec := NormalizeMediaSort(field, order)
	out := append([]MediaItem(nil), items...)
	if len(out) < 2 {
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		cmp := compareMediaByField(out[i], out[j], spec, history)
		if cmp == 0 {
			cmp = compareID(out[i].ID, out[j].ID)
		}
		return cmp < 0
	})
	return out
}

// SortSeriesCards returns a sorted copy of series cards.
func SortSeriesCards(cards []SeriesCard, field, order string, history map[string]time.Time) []SeriesCard {
	spec := NormalizeMediaSort(field, order)
	out := append([]SeriesCard(nil), cards...)
	if len(out) < 2 {
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		cmp := compareSeriesByField(out[i], out[j], spec, history)
		if cmp == 0 {
			cmp = compareID(out[i].Key, out[j].Key)
		}
		return cmp < 0
	})
	return out
}

func compareMediaByField(a, b MediaItem, spec MediaSortSpec, history map[string]time.Time) int {
	switch spec.Field {
	case "title":
		ta, tb := mediaSortTitle(a.Media), mediaSortTitle(b.Media)
		return compareOrdered(strings.Compare(strings.ToLower(strings.TrimSpace(ta)), strings.ToLower(strings.TrimSpace(tb))), strings.TrimSpace(ta) != "", strings.TrimSpace(tb) != "", spec.Order)
	case "release_date":
		ta, oka := mediaReleaseTime(a.Media)
		tb, okb := mediaReleaseTime(b.Media)
		return compareOrdered(compareTime(ta, tb), oka, okb, spec.Order)
	case "year":
		return compareOrdered(compareInt(a.Year, b.Year), true, true, spec.Order)
	case "created_at":
		return compareOrdered(compareTime(a.CreatedAt, b.CreatedAt), !a.CreatedAt.IsZero(), !b.CreatedAt.IsZero(), spec.Order)
	case "updated_at":
		ta, oka := mediaUpdatedTime(a.Media)
		tb, okb := mediaUpdatedTime(b.Media)
		return compareOrdered(compareTime(ta, tb), oka, okb, spec.Order)
	case "rating":
		return compareOrdered(compareFloat(float64(a.Rating), float64(b.Rating)), true, true, spec.Order)
	case "duration":
		return compareOrdered(compareInt(a.DurationSec, b.DurationSec), true, true, spec.Order)
	case "bitrate":
		return compareOrdered(compareInt64(a.SizeBytes, b.SizeBytes), true, true, spec.Order)
	case "last_played":
		ta, oka := mediaItemLastPlayedTime(a, history)
		tb, okb := mediaItemLastPlayedTime(b, history)
		return compareOrdered(compareTime(ta, tb), oka, okb, spec.Order)
	default:
		return 0
	}
}
func compareSeriesByField(a, b SeriesCard, spec MediaSortSpec, history map[string]time.Time) int {
	switch spec.Field {
	case "title":
		return compareOrdered(strings.Compare(strings.ToLower(strings.TrimSpace(mediaSortTitle(a.Rep))), strings.ToLower(strings.TrimSpace(mediaSortTitle(b.Rep)))), strings.TrimSpace(mediaSortTitle(a.Rep)) != "", strings.TrimSpace(mediaSortTitle(b.Rep)) != "", spec.Order)
	case "release_date":
		ta, oka := mediaReleaseTime(a.Rep)
		tb, okb := mediaReleaseTime(b.Rep)
		return compareOrdered(compareTime(ta, tb), oka, okb, spec.Order)
	case "year":
		return compareOrdered(compareInt(a.Rep.Year, b.Rep.Year), true, true, spec.Order)
	case "created_at":
		return compareOrdered(compareTime(a.Rep.CreatedAt, b.Rep.CreatedAt), !a.Rep.CreatedAt.IsZero(), !b.Rep.CreatedAt.IsZero(), spec.Order)
	case "updated_at":
		ta, oka := seriesUpdatedTime(a)
		tb, okb := seriesUpdatedTime(b)
		return compareOrdered(compareTime(ta, tb), oka, okb, spec.Order)
	case "rating":
		return compareOrdered(compareFloat(float64(a.Rep.Rating), float64(b.Rep.Rating)), true, true, spec.Order)
	case "duration":
		return compareOrdered(compareInt(a.Rep.DurationSec, b.Rep.DurationSec), true, true, spec.Order)
	case "bitrate":
		return compareOrdered(compareInt64(a.Rep.SizeBytes, b.Rep.SizeBytes), true, true, spec.Order)
	case "last_played":
		ta, oka := seriesLastPlayed(a, history)
		tb, okb := seriesLastPlayed(b, history)
		return compareOrdered(compareTime(ta, tb), oka, okb, spec.Order)
	default:
		return 0
	}
}

func mediaSortTitle(m model.Media) string {
	if strings.TrimSpace(m.Title) != "" {
		return m.Title
	}
	return m.OriginalName
}

func mediaReleaseTime(m model.Media) (time.Time, bool) {
	if raw := strings.TrimSpace(m.ReleaseDate); raw != "" {
		if parsed, err := time.Parse("2006-01-02", raw); err == nil {
			return parsed, true
		}
	}
	if m.Year > 0 {
		return time.Date(m.Year, time.January, 1, 0, 0, 0, 0, time.UTC), true
	}
	return time.Time{}, false
}

func mediaUpdatedTime(m model.Media) (time.Time, bool) {
	if !m.UpdatedAt.IsZero() {
		return m.UpdatedAt, true
	}
	if !m.CreatedAt.IsZero() {
		return m.CreatedAt, true
	}
	return time.Time{}, false
}

func seriesUpdatedTime(card SeriesCard) (time.Time, bool) {
	if card.LastAddedAt != nil && !card.LastAddedAt.IsZero() {
		return *card.LastAddedAt, true
	}
	return mediaUpdatedTime(card.Rep)
}

func seriesLastPlayed(card SeriesCard, history map[string]time.Time) (time.Time, bool) {
	for _, id := range []string{card.Rep.ID, card.LinkMedia.ID} {
		if played, ok := history[id]; ok {
			return played, true
		}
	}
	return time.Time{}, false
}

func mediaItemLastPlayedTime(item MediaItem, history map[string]time.Time) (time.Time, bool) {
	best, found := history[item.ID]
	for _, version := range item.Versions {
		if played, ok := history[version.ID]; ok && (!found || played.After(best)) {
			best = played
			found = true
		}
	}
	return best, found
}
func compareOrdered(cmp int, aValid, bValid bool, order string) int {
	if !aValid && !bValid {
		return 0
	}
	if !aValid {
		return 1
	}
	if !bValid {
		return -1
	}
	if order == "desc" {
		return -cmp
	}
	return cmp
}

func compareTime(a, b time.Time) int {
	if a.Before(b) {
		return -1
	}
	if a.After(b) {
		return 1
	}
	return 0
}

func compareInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func compareInt64(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func compareFloat(a, b float64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func compareID(a, b string) int {
	return strings.Compare(a, b)
}
