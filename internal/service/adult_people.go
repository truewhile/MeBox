package service

import (
	"context"
	"strings"

	"github.com/truewhile/MeBox/internal/model"
)

// GetPeople resolves adult cast/crew on demand from MetaTube. The provider and
// remote movie ID are persisted in the existing external-ID columns; the
// returned people are intentionally not written to the database.
func (p *AdultProvider) GetPeople(ctx context.Context, media *model.Media) []map[string]any {
	if p == nil || media == nil || !media.NSFW {
		return nil
	}
	provider := strings.TrimSpace(media.TheTVDBID)
	movieID := strings.TrimSpace(media.DoubanID)
	if provider == "" || movieID == "" {
		return nil
	}
	match, err := p.GetMetaTubeCandidate(ctx, provider, movieID)
	if err != nil || match == nil {
		return nil
	}
	return match.People
}
