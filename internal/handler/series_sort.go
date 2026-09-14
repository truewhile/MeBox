package handler

import (
	"slices"
	"time"

	"github.com/truewhile/MeBox/internal/service"
)

// sortRemoteSeriesCards keeps the DateLastContentAdded order returned by the
// remote Emby server when LastAddedAt is not exposed in the Series DTO.
func sortRemoteSeriesCards(cards []service.SeriesCard, spec service.MediaSortSpec, history map[string]time.Time) []service.SeriesCard {
	if spec.Field != "updated_at" {
		return service.SortSeriesCards(cards, spec.Field, spec.Order, history)
	}

	out := append([]service.SeriesCard(nil), cards...)
	// RemoteSeriesCards always fetches DateLastContentAdded in descending order.
	if spec.Order == "asc" {
		slices.Reverse(out)
	}
	return service.SortSeriesCards(out, spec.Field, spec.Order, history)
}
