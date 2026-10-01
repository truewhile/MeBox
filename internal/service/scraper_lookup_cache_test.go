package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// Every episode of a show produces the same query candidate (the series folder
// title). Without the lookup cache each episode re-issues the identical search,
// so a whole season costs one request per episode. This asserts the provider is
// hit once and subsequent episodes reuse the cached match.
func TestEnrichLibraryReusesLookupCacheAcrossEpisodes(t *testing.T) {
	var searchCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/tv":
			if r.URL.Query().Get("query") != "折腰" {
				_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
				return
			}
			atomic.AddInt32(&searchCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{{
					"id":             296753,
					"name":           "折腰",
					"overview":       "正确的剧集条目",
					"poster_path":    "/zheyao.jpg",
					"first_air_date": "2025-05-13",
					"origin_country": []string{"CN"},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Series{}, &model.Media{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	cfg := &config.Config{}
	cfg.Secrets.TMDbAPIKey = "test-key"
	cfg.Secrets.TMDbAPIProxy = upstream.URL
	cfg.Secrets.TMDbImageProxy = upstream.URL + "/images"
	log := zap.NewNop()
	scraper := NewScraperService(cfg, log, repos, NewTMDbProvider(cfg, log, nil), nil, nil, nil, NewHub(log))

	lib := model.Library{Name: "OpenList · 刮削缓存测试库", Path: "cloud://openlist/scrape-lookup-cache", Type: "tv", Enabled: true}
	if err := repos.DB.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	const episodeCount = 4
	for episode := 1; episode <= episodeCount; episode++ {
		media := model.Media{
			LibraryID:    lib.ID,
			Title:        "折腰",
			Path:         "cloud://openlist/scrape-lookup-cache/折腰 (2025)/Season 1/折腰.S01E0" + string(rune('0'+episode)) + ".mkv",
			SeasonNum:    1,
			EpisodeNum:   episode,
			ScrapeStatus: "pending",
		}
		if err := repos.DB.Create(&media).Error; err != nil {
			t.Fatal(err)
		}
	}

	result, err := scraper.EnrichLibraryDetailedWithOptions(t.Context(), lib.ID, ScrapeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched != episodeCount {
		t.Fatalf("matched = %d, want %d", result.Matched, episodeCount)
	}
	if calls := atomic.LoadInt32(&searchCalls); calls != 1 {
		t.Fatalf("tmdb /search/tv called %d times for %d episodes; want 1 (cache should dedupe identical queries)", calls, episodeCount)
	}
}

// A cached match must be handed out as an independent copy: mutating one row's
// result (localized title preference, local metadata merge) must not bleed into
// the next row.
func TestScrapeLookupCacheReturnsIndependentCopies(t *testing.T) {
	cache := newScrapeLookupCache()
	original := &Match{
		Title:     "折腰",
		TMDbID:    296753,
		Genres:    []string{"剧情"},
		Countries: []string{"CN"},
		Aliases:   []string{"Zhe Yao"},
	}
	key := scrapeLookupCacheKey("tv", "折腰", 2025, false)
	cache.set(key, original)

	first, ok := cache.get(key)
	if !ok || first == nil {
		t.Fatal("expected a cache hit")
	}
	first.Title = "mutated"
	first.Genres[0] = "mutated"
	first.Aliases = append(first.Aliases, "extra")

	second, ok := cache.get(key)
	if !ok || second == nil {
		t.Fatal("expected a second cache hit")
	}
	if second.Title != "折腰" || second.Genres[0] != "剧情" || len(second.Aliases) != 1 {
		t.Fatalf("cached match was mutated through a returned copy: %+v", second)
	}
}

// Negative results are cached too: a query that matched nothing must not be
// re-issued for every remaining episode of the same show.
func TestScrapeLookupCacheStoresNegativeResults(t *testing.T) {
	cache := newScrapeLookupCache()
	key := scrapeLookupCacheKey("tv", "no-such-show", 0, false)
	cache.set(key, nil)
	if _, ok := cache.get(key); !ok {
		t.Fatal("negative result should be cached to avoid repeated provider calls")
	}
}

// The theatrical flag is part of the key: a theatrical feature gets a tv
// fallback lookup, so its result must not be shared with (or returned for)
// the same query issued for a non-theatrical media row.
func TestScrapeLookupCacheKeySeparatesTheatrical(t *testing.T) {
	plain := scrapeLookupCacheKey("movie", "query", 2024, false)
	theatrical := scrapeLookupCacheKey("movie", "query", 2024, true)
	if plain == theatrical {
		t.Fatalf("theatrical flag must be part of the cache key: %q", plain)
	}
}

// A manual "retry no match" run clears stale negative entries so the provider
// chain is actually re-queried instead of short-circuiting on the cached miss.
func TestScrapeLookupCacheClearNegativesKeepsPositives(t *testing.T) {
	cache := newScrapeLookupCache()
	negKey := scrapeLookupCacheKey("tv", "missing", 0, false)
	posKey := scrapeLookupCacheKey("tv", "found", 0, false)
	cache.set(negKey, nil)
	cache.set(posKey, &Match{Title: "found"})
	cache.clearNegatives()
	if _, ok := cache.get(negKey); ok {
		t.Fatal("negative entry should be cleared before a manual retry")
	}
	if got, ok := cache.get(posKey); !ok || got == nil || got.Title != "found" {
		t.Fatalf("positive entry should survive clearNegatives: %+v", got)
	}
}
