package handler

import (
	"testing"

	"github.com/truewhile/MeBox/internal/service"
)

func TestSortRemoteSeriesCardsUpdatedAtUsesRemoteOrder(t *testing.T) {
	cards := []service.SeriesCard{{Key: "newest"}, {Key: "middle"}, {Key: "oldest"}}

	desc := sortRemoteSeriesCards(cards, service.MediaSortSpec{Field: "updated_at", Order: "desc"}, nil)
	if desc[0].Key != "newest" || desc[1].Key != "middle" || desc[2].Key != "oldest" {
		t.Fatalf("descending order = %q, %q, %q", desc[0].Key, desc[1].Key, desc[2].Key)
	}

	asc := sortRemoteSeriesCards(cards, service.MediaSortSpec{Field: "updated_at", Order: "asc"}, nil)
	if asc[0].Key != "oldest" || asc[1].Key != "middle" || asc[2].Key != "newest" {
		t.Fatalf("ascending order = %q, %q, %q", asc[0].Key, asc[1].Key, asc[2].Key)
	}
}
