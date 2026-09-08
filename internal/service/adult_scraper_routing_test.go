package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestAdultProviderRouting(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	_ = db.AutoMigrate(&model.Setting{}, &model.APIConfig{})

	repos := repository.New(db)

	mtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/movies/search":
			q := r.URL.Query().Get("q")
			if q == "SSIS-001" {
				results := struct {
					Data []MetaTubeSearchResult `json:"data"`
				}{
					Data: []MetaTubeSearchResult{
						{
							ID:          "999",
							Number:      "SSIS-001",
							Title:       "河北彩花 専属デビュー",
							Provider:    "javdb",
							Actors:      []string{"河北彩花"},
							ReleaseDate: "2021-06-19",
							Score:       4.9,
						},
					},
				}
				_ = json.NewEncoder(w).Encode(results)
				return
			}
		case "/v1/movies/javdb/999":
			_ = json.NewEncoder(w).Encode(struct {
				Data MetaTubeMovieInfo `json:"data"`
			}{
				Data: MetaTubeMovieInfo{
					ID:            "999",
					Number:        "SSIS-001",
					Title:         "河北彩花 専属デビュー",
					Provider:      "javdb",
					CoverURL:      "https://example.com/poster.jpg",
					PreviewImages: []string{"https://example.com/backdrop.jpg"},
					ReleaseDate:   "2021-06-19",
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer mtServer.Close()

	// 1. Configure metatube settings
	_ = repos.Setting.Set(context.Background(), "adult.scraper.engine", "metatube")
	_ = repos.Setting.Set(context.Background(), "adult.scraper.metatube_server", mtServer.URL)

	adultProvider := NewAdultProvider(zap.NewNop(), nil, repos)

	// Test Search
	match, err := adultProvider.Search(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if match == nil {
		t.Fatal("expected match, got nil")
	}
	if match.OriginalName != "SSIS-001" {
		t.Errorf("expected SSIS-001, got %s", match.OriginalName)
	}
	if match.Year != 2021 {
		t.Errorf("expected 2021, got %d", match.Year)
	}

	// Test SearchCandidates
	candidates, err := adultProvider.SearchCandidates(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatalf("unexpected error on candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	wantPoster := mtServer.URL + "/v1/images/primary/javdb/999?auto=true&pos=1&quality=90&ratio=-1&url=https%3A%2F%2Fexample.com%2Fposter.jpg"
	wantBackdrop := mtServer.URL + "/v1/images/backdrop/javdb/999?quality=90"
	if candidates[0].PosterURL != wantPoster || candidates[0].BackdropURL != wantBackdrop {
		t.Fatalf("candidate artwork was not enriched: %#v", candidates[0])
	}

	// 2. Test auto mode with failing metatube query
	_ = repos.Setting.Set(context.Background(), "adult.scraper.engine", "auto")
	// Search for something not on mock metatube server; will attempt fallback
	mUnknown, _ := adultProvider.Search(context.Background(), "NONEXISTENT-999")
	if mUnknown != nil {
		t.Errorf("expected nil for nonexistent in auto mode when sources unavailable")
	}
}

func TestBuiltinAdultScrapeUsesMetaTubeFaceAwareArtwork(t *testing.T) {
	builtinServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			_, _ = w.Write([]byte(`<a class="box" href="/v/local"><strong>SSIS-001 本地候选</strong></a>`))
		case "/v/local":
			_, _ = w.Write([]byte(`<h2>SSIS-001 本地标题</h2><img class="video-cover" src="/wide-cover.jpg">`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer builtinServer.Close()

	metaTubeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/movies/search" || r.URL.Query().Get("q") != "SSIS-001" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(struct {
			Data []MetaTubeSearchResult `json:"data"`
		}{
			Data: []MetaTubeSearchResult{{
				ID:       "999",
				Number:   "SSIS-001",
				Title:    "MetaTube candidate",
				Provider: "AVE",
				CoverURL: "https://example.com/wide-cover.jpg",
			}},
		})
	}))
	defer metaTubeServer.Close()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Setting{}, &model.APIConfig{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	for key, value := range map[string]string{
		"adult.scraper.engine":            "builtin",
		"adult.scraper.builtin_javdb_url": builtinServer.URL,
		"adult.scraper.metatube_server":   metaTubeServer.URL,
		"adult.scraper.crop_cover":        "true",
	} {
		if err := repos.Setting.Set(t.Context(), key, value); err != nil {
			t.Fatal(err)
		}
	}

	provider := NewAdultProvider(zap.NewNop(), nil, repos)
	match, err := provider.Search(t.Context(), "SSIS-001")
	if err != nil {
		t.Fatal(err)
	}
	if match == nil {
		t.Fatal("expected built-in match")
	}
	wantPoster := metaTubeServer.URL + "/v1/images/primary/AVE/999?auto=true&pos=1&quality=90&ratio=-1&url=https%3A%2F%2Fexample.com%2Fwide-cover.jpg"
	if match.PosterURL != wantPoster {
		t.Fatalf("built-in poster = %q, want face-aware URL %q", match.PosterURL, wantPoster)
	}
}
