package service

import (
	"sort"
	"strings"
	"time"
)

type embySeriesCacheEntry struct {
	group     embySeriesGroup
	expiresAt time.Time
}

type embySeasonCacheEntry struct {
	season    embySeasonGroup
	expiresAt time.Time
}

type embyArtworkCacheEntry struct {
	primary   string
	backdrop  string
	expiresAt time.Time
}

func (e *EmbyService) rememberSeriesGroup(group embySeriesGroup) {
	if e == nil || strings.TrimSpace(group.ID) == "" {
		return
	}
	expiresAt := time.Now().Add(embyVirtualCacheTTL)
	e.virtualMu.Lock()
	defer e.virtualMu.Unlock()
	if e.virtualSeries == nil {
		e.virtualSeries = make(map[string]embySeriesCacheEntry)
	}
	if e.virtualSeasons == nil {
		e.virtualSeasons = make(map[string]embySeasonCacheEntry)
	}
	if e.virtualArtwork == nil {
		e.virtualArtwork = make(map[string]embyArtworkCacheEntry)
	}
	e.trimVirtualCachesLocked(time.Now())
	e.virtualSeries[group.ID] = embySeriesCacheEntry{group: group, expiresAt: expiresAt}
	e.virtualArtwork[group.ID] = embyArtworkCacheEntry{primary: group.PosterURL, backdrop: group.BackdropURL, expiresAt: expiresAt}
	e.virtualArtwork[group.ID+"-bd"] = embyArtworkCacheEntry{primary: group.PosterURL, backdrop: group.BackdropURL, expiresAt: expiresAt}
	for _, season := range e.seasonsForSeries(group) {
		e.virtualSeasons[season.ID] = embySeasonCacheEntry{season: season, expiresAt: expiresAt}
		e.virtualArtwork[season.ID] = embyArtworkCacheEntry{primary: season.Series.PosterURL, backdrop: season.Series.BackdropURL, expiresAt: expiresAt}
		e.virtualArtwork[season.ID+"-bd"] = embyArtworkCacheEntry{primary: season.Series.PosterURL, backdrop: season.Series.BackdropURL, expiresAt: expiresAt}
	}
}

func (e *EmbyService) rememberSeasonGroup(season embySeasonGroup) {
	if e == nil || strings.TrimSpace(season.ID) == "" {
		return
	}
	expiresAt := time.Now().Add(embyVirtualCacheTTL)
	e.virtualMu.Lock()
	defer e.virtualMu.Unlock()
	if e.virtualSeasons == nil {
		e.virtualSeasons = make(map[string]embySeasonCacheEntry)
	}
	if e.virtualArtwork == nil {
		e.virtualArtwork = make(map[string]embyArtworkCacheEntry)
	}
	e.trimVirtualCachesLocked(time.Now())
	e.virtualSeasons[season.ID] = embySeasonCacheEntry{season: season, expiresAt: expiresAt}
	e.virtualArtwork[season.ID] = embyArtworkCacheEntry{primary: season.Series.PosterURL, backdrop: season.Series.BackdropURL, expiresAt: expiresAt}
	e.virtualArtwork[season.ID+"-bd"] = embyArtworkCacheEntry{primary: season.Series.PosterURL, backdrop: season.Series.BackdropURL, expiresAt: expiresAt}
}

func (e *EmbyService) cachedSeriesGroup(id string) (embySeriesGroup, bool) {
	if e == nil || strings.TrimSpace(id) == "" {
		return embySeriesGroup{}, false
	}
	now := time.Now()
	e.virtualMu.RLock()
	entry, ok := e.virtualSeries[id]
	e.virtualMu.RUnlock()
	if !ok || now.After(entry.expiresAt) {
		if ok {
			e.virtualMu.Lock()
			delete(e.virtualSeries, id)
			e.virtualMu.Unlock()
		}
		return embySeriesGroup{}, false
	}
	return entry.group, true
}

func (e *EmbyService) cachedSeasonGroup(id string) (embySeasonGroup, bool) {
	if e == nil || strings.TrimSpace(id) == "" {
		return embySeasonGroup{}, false
	}
	now := time.Now()
	e.virtualMu.RLock()
	entry, ok := e.virtualSeasons[id]
	e.virtualMu.RUnlock()
	if !ok || now.After(entry.expiresAt) {
		if ok {
			e.virtualMu.Lock()
			delete(e.virtualSeasons, id)
			e.virtualMu.Unlock()
		}
		return embySeasonGroup{}, false
	}
	return entry.season, true
}

func (e *EmbyService) cachedArtworkURL(id, imageType string) (string, bool) {
	if e == nil || strings.TrimSpace(id) == "" {
		return "", false
	}
	now := time.Now()
	e.virtualMu.RLock()
	entry, ok := e.virtualArtwork[id]
	e.virtualMu.RUnlock()
	if !ok || now.After(entry.expiresAt) {
		if ok {
			e.virtualMu.Lock()
			delete(e.virtualArtwork, id)
			e.virtualMu.Unlock()
		}
		return "", false
	}
	switch strings.ToLower(imageType) {
	case "backdrop", "art":
		if entry.backdrop != "" {
			return entry.backdrop, true
		}
	}
	if entry.primary != "" {
		return entry.primary, true
	}
	return entry.backdrop, entry.backdrop != ""
}

// embyArtworkRef is the poster/backdrop pair stored next to a Latest payload
// so a JSON-cache hit can refill the in-memory artwork map before the client
// asks for the image.
type embyArtworkRef struct {
	Primary  string `json:"primary,omitempty"`
	Backdrop string `json:"backdrop,omitempty"`
}

func (e *EmbyService) artworkRefsForSeriesGroups(groups []embySeriesGroup) map[string]embyArtworkRef {
	if e == nil || len(groups) == 0 {
		return nil
	}
	refs := make(map[string]embyArtworkRef, len(groups)*2)
	for _, group := range groups {
		if group.PosterURL == "" && group.BackdropURL == "" {
			continue
		}
		ref := embyArtworkRef{Primary: group.PosterURL, Backdrop: group.BackdropURL}
		refs[group.ID] = ref
		for _, season := range e.seasonsForSeries(group) {
			refs[season.ID] = ref
		}
	}
	if len(refs) == 0 {
		return nil
	}
	return refs
}

func (e *EmbyService) rememberArtworkRefs(refs map[string]embyArtworkRef) {
	if e == nil || len(refs) == 0 {
		return
	}
	expiresAt := time.Now().Add(embyVirtualCacheTTL)
	e.virtualMu.Lock()
	defer e.virtualMu.Unlock()
	if e.virtualArtwork == nil {
		e.virtualArtwork = make(map[string]embyArtworkCacheEntry, len(refs))
	}
	e.trimVirtualArtworkLocked(time.Now())
	for id, ref := range refs {
		if strings.TrimSpace(id) == "" || (ref.Primary == "" && ref.Backdrop == "") {
			continue
		}
		e.virtualArtwork[id] = embyArtworkCacheEntry{primary: ref.Primary, backdrop: ref.Backdrop, expiresAt: expiresAt}
	}
}

func (e *EmbyService) trimVirtualCachesLocked(now time.Time) {
	e.trimVirtualSeriesLocked(now)
	e.trimVirtualSeasonsLocked(now)
	e.trimVirtualArtworkLocked(now)
}

func (e *EmbyService) trimVirtualSeriesLocked(now time.Time) {
	for id, entry := range e.virtualSeries {
		if now.After(entry.expiresAt) {
			delete(e.virtualSeries, id)
		}
	}
	evictOldest(e.virtualSeries, embyVirtualSeriesCap, func(entry embySeriesCacheEntry) time.Time {
		return entry.expiresAt
	})
}

func (e *EmbyService) trimVirtualSeasonsLocked(now time.Time) {
	for id, entry := range e.virtualSeasons {
		if now.After(entry.expiresAt) {
			delete(e.virtualSeasons, id)
		}
	}
	evictOldest(e.virtualSeasons, embyVirtualSeasonCap, func(entry embySeasonCacheEntry) time.Time {
		return entry.expiresAt
	})
}

func (e *EmbyService) trimVirtualArtworkLocked(now time.Time) {
	for id, entry := range e.virtualArtwork {
		if now.After(entry.expiresAt) {
			delete(e.virtualArtwork, id)
		}
	}
	evictOldest(e.virtualArtwork, embyVirtualArtworkCap, func(entry embyArtworkCacheEntry) time.Time {
		return entry.expiresAt
	})
}

// evictOldest drops the soonest-expiring entries until the map is under cap.
// It must not replace the map: a homepage refresh remembers many series at
// once, and wiping the whole cache made the just-advertised backdrops 404
// into a 1x1 placeholder.
func evictOldest[T any](items map[string]T, cap int, expiresAt func(T) time.Time) {
	excess := len(items) - cap
	if cap <= 0 || excess <= 0 {
		return
	}
	type pair struct {
		id string
		at time.Time
	}
	ordered := make([]pair, 0, len(items))
	for id, entry := range items {
		ordered = append(ordered, pair{id: id, at: expiresAt(entry)})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].at.Before(ordered[j].at) })
	if excess > len(ordered) {
		excess = len(ordered)
	}
	for i := 0; i < excess; i++ {
		delete(items, ordered[i].id)
	}
}

func embyVirtualImageTag(id, suffix string) string {
	if strings.HasPrefix(id, embyVirtualSeriesPrefix) || strings.HasPrefix(id, embyVirtualSeasonPrefix) {
		return id + suffix
	}
	return id
}
