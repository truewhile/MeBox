package service

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// A TV library scrapes one media row per episode, and every row in the same
// show computes the same query candidate (the series folder title). Without a
// cache each episode re-issues the identical TMDb/Douban/Bangumi/TheTVDB
// search, so a 100-episode season costs 100x the network calls it needs.
//
// The cache lives on the ScraperService instance (not a package global) so that
// two services with different provider configuration never share results. TTL
// bounds staleness for out-of-band edits; explicit invalidation is unnecessary
// because the key includes the effective media kind, theatrical flag, query
// and year. Negative entries use a shorter TTL so a failed query is retried
// sooner without manual intervention.
const (
	scrapeLookupCacheTTL         = 10 * time.Minute
	scrapeLookupNegativeCacheTTL = 2 * time.Minute
	scrapeLookupCacheMaxItems    = 1024
)

type scrapeLookupCache struct {
	mu      sync.Mutex
	entries map[string]scrapeLookupCacheEntry
}

type scrapeLookupCacheEntry struct {
	match     *Match
	expiresAt time.Time
}

func newScrapeLookupCache() *scrapeLookupCache {
	return &scrapeLookupCache{entries: map[string]scrapeLookupCacheEntry{}}
}

func scrapeLookupCacheKey(kind, query string, year int, isTheatrical bool) string {
	theatrical := "0"
	if isTheatrical {
		theatrical = "1"
	}
	return strings.ToLower(strings.TrimSpace(kind)) + "|" +
		theatrical + "|" +
		strconv.Itoa(year) + "|" +
		strings.ToLower(strings.TrimSpace(query))
}

// get reports whether the key is cached. A hit may carry a nil match, which
// means the provider chain already ran and found nothing (negative cache).
// Use clearNegative before a user-triggered retry so stale negative entries
// do not suppress the fresh provider round-trip.
func (c *scrapeLookupCache) get(key string) (*Match, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if now.After(item.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return cloneMatch(item.match), true
}

func (c *scrapeLookupCache) set(key string, match *Match) {
	if c == nil || key == "" {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= scrapeLookupCacheMaxItems {
		for k, item := range c.entries {
			if now.After(item.expiresAt) || len(c.entries) >= scrapeLookupCacheMaxItems {
				delete(c.entries, k)
			}
			if len(c.entries) < scrapeLookupCacheMaxItems {
				break
			}
		}
	}
	ttl := scrapeLookupCacheTTL
	if match == nil {
		ttl = scrapeLookupNegativeCacheTTL
	}
	c.entries[key] = scrapeLookupCacheEntry{match: cloneMatch(match), expiresAt: now.Add(ttl)}
}

// clearNegatives drops cached misses so a user-triggered retry gets a fresh
// provider round-trip instead of reusing a stale negative entry.
func (c *scrapeLookupCache) clearNegatives() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, item := range c.entries {
		if item.match == nil {
			delete(c.entries, k)
		}
	}
}

// cloneMatch returns an independent copy so callers that mutate the result
// (localized title preference, local metadata merge, fanart artwork) cannot
// corrupt the cached entry or leak state between media rows.
func cloneMatch(m *Match) *Match {
	if m == nil {
		return nil
	}
	out := *m
	out.Languages = append([]string(nil), m.Languages...)
	out.Countries = append([]string(nil), m.Countries...)
	out.Genres = append([]string(nil), m.Genres...)
	out.Aliases = append([]string(nil), m.Aliases...)
	return &out
}
