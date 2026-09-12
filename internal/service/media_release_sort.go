package service

import (
	"fmt"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

func mediaReleaseSortTime(media model.Media) time.Time {
	if value := normalizeReleaseDate(media.ReleaseDate); value != "" {
		if t, err := time.Parse("2006-01-02", value); err == nil {
			return t
		}
	}
	if media.Year > 0 {
		if t, err := time.Parse("2006-01-02", fmt.Sprintf("%04d-12-31", media.Year)); err == nil {
			return t
		}
	}
	if !media.UpdatedAt.IsZero() {
		return media.UpdatedAt
	}
	return media.CreatedAt
}

func mediaReleaseOrderSQL(desc bool) string {
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	return fmt.Sprintf("media.release_date %s, media.year %s, media.created_at %s, media.id %s", dir, dir, dir, dir)
}

// embyMediaReleaseSortTime matches the original payload-based ordering used by
// the Emby movie-library merge: release date, then year, then created_at.
func embyMediaReleaseSortTime(media model.Media) time.Time {
	if t, ok := embyPremiereDate(media.ReleaseDate); ok {
		return t
	}
	if media.Year > 0 {
		return time.Date(media.Year, time.December, 31, 0, 0, 0, 0, time.UTC)
	}
	return media.CreatedAt
}

func embyPremiereDate(value string) (time.Time, bool) {
	value = normalizeReleaseDate(value)
	if value == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", value)
	return t, err == nil
}
