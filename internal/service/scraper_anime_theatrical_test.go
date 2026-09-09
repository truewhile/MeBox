package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
)

func TestTheatricalTitleVariants(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{
			input: "名侦探柯南 剧场版01 引爆摩天楼",
			want:  []string{"名侦探柯南 引爆摩天楼", "名侦探柯南 剧场版01 引爆摩天楼"},
		},
		{
			input: "名侦探柯南 剧场版26 黑铁的鱼影",
			want:  []string{"名侦探柯南 黑铁的鱼影", "名侦探柯南 剧场版26 黑铁的鱼影"},
		},
		{
			input: "鬼灭之刃 剧场版 无限列车篇",
			want:  []string{"鬼灭之刃 无限列车篇", "鬼灭之刃 剧场版 无限列车篇"},
		},
		{
			input: "航海王 The Movie 黄金之城",
			want:  []string{"航海王 The Movie 黄金之城"},
		},
		{
			input: "普通动漫 第01集",
			want:  []string{"普通动漫 第01集"},
		},
	}

	for _, tc := range tests {
		got := theatricalTitleVariants(tc.input)
		if len(got) != len(tc.want) {
			t.Fatalf("theatricalTitleVariants(%q) = %v, want %v", tc.input, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("theatricalTitleVariants(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

func TestMetadataMatchCompatibilityForTheatricalFeatures(t *testing.T) {
	animeMatch := &Match{MediaType: "anime"}

	if metadataMatchCompatibleWithType("movie", animeMatch) {
		t.Fatal("ordinary movie lookups must not accept anime matches")
	}
	if !metadataMatchCompatibleWithTheatrical("movie", true, animeMatch) {
		t.Fatal("theatrical movie lookup should accept an anime provider match")
	}
	if metadataMatchCompatibleWithTheatrical("movie", false, animeMatch) {
		t.Fatal("non-theatrical movie lookup must not accept an anime provider match")
	}
}

func TestScrapeQueryCandidatesForAnimeTheatricalMix(t *testing.T) {
	lib := &model.Library{
		Path: `/media/anime`,
		Type: "anime",
	}

	// 1. 同目录下包含剧场版01
	theatricalMedia := &model.Media{
		Title: "名侦探柯南 剧场版01 引爆摩天楼",
		Path:  `/media/anime/名侦探柯南/名侦探柯南 剧场版01 引爆摩天楼.mkv`,
	}
	candidates := scrapeQueryCandidates(theatricalMedia, lib)
	if len(candidates) == 0 {
		t.Fatal("scrapeQueryCandidates returned no candidates for theatrical media")
	}
	if candidates[0] != "名侦探柯南 引爆摩天楼" {
		t.Fatalf("first candidate for theatrical media = %q, want cleaned movie title '名侦探柯南 引爆摩天楼'; all=%v", candidates[0], candidates)
	}

	// 2. 剧场版放在以剧场版命名的子目录
	subfolderTheatricalMedia := &model.Media{
		Title: "无限列车篇",
		Path:  `/media/anime/鬼灭之刃/剧场版/无限列车篇.mkv`,
	}
	subCandidates := scrapeQueryCandidates(subfolderTheatricalMedia, lib)
	if len(subCandidates) == 0 {
		t.Fatal("scrapeQueryCandidates returned no candidates for subfolder theatrical media")
	}
	foundCombined := false
	for _, c := range subCandidates {
		if c == "鬼灭之刃 无限列车篇" {
			foundCombined = true
			break
		}
	}
	if !foundCombined {
		t.Fatalf("candidates for subfolder theatrical did not contain '鬼灭之刃 无限列车篇': %v", subCandidates)
	}

	// 3. 混合放置的普通剧集分集不应受剧场版影响
	episodeMedia := &model.Media{
		Title:      "名侦探柯南 - S01E01",
		Path:       `/media/anime/名侦探柯南/名侦探柯南 - S01E01.mp4`,
		SeasonNum:  1,
		EpisodeNum: 1,
	}
	epCandidates := scrapeQueryCandidates(episodeMedia, lib)
	if len(epCandidates) == 0 {
		t.Fatal("scrapeQueryCandidates returned no candidates for episode media")
	}
	if epCandidates[0] != "名侦探柯南" {
		t.Fatalf("first candidate for tv episode = %q, want series folder title '名侦探柯南'", epCandidates[0])
	}
}

func TestEnrichOneAnimeMixedEpisodesAndTheatrical(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/tv":
			q := r.URL.Query().Get("query")
			if q == "鬼灭之刃" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"results": []map[string]any{{
						"id":             85937,
						"name":           "鬼灭之刃",
						"original_name":  "鬼滅の刃",
						"overview":       "大正时期、日本...",
						"first_air_date": "2019-04-06",
					}},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
		case "/search/movie":
			q := r.URL.Query().Get("query")
			if q == "鬼灭之刃 无限列车篇" || q == "鬼灭之刃 剧场版 无限列车篇" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"results": []map[string]any{{
						"id":             635302,
						"title":          "鬼灭之刃 剧场版 无限列车篇",
						"original_title": "劇場版「鬼滅の刃」無限列車編",
						"overview":       "在结束了蝴蝶屋的修业之后...",
						"release_date":   "2020-10-16",
					}},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
		case "/tv/85937":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":             85937,
				"name":           "鬼灭之刃",
				"original_name":  "鬼滅の刃",
				"first_air_date": "2019-04-06",
			})
		case "/movie/635302":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":             635302,
				"title":          "鬼灭之刃 剧场版 无限列车篇",
				"original_title": "劇場版「鬼滅の刃」無限列車編",
				"release_date":   "2020-10-16",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	repos := newOrganizerTestRepo(t)
	cfg := &config.Config{}
	cfg.Secrets.TMDbAPIKey = "test-key"
	cfg.Secrets.TMDbAPIProxy = upstream.URL
	log := zap.NewNop()
	scraper := NewScraperService(cfg, log, repos, NewTMDbProvider(cfg, log, nil), nil, nil, nil, NewHub(log))

	lib := model.Library{Name: "动漫", Path: `/media/anime`, Type: "anime", Enabled: true}
	if err := repos.DB.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}

	// 1. 同一目录下的 TV 剧集
	tvEpisode := model.Media{
		LibraryID:    lib.ID,
		Title:        "鬼灭之刃 S01E01",
		Path:         `/media/anime/鬼灭之刃/Season 01/鬼灭之刃 - S01E01.mkv`,
		SeasonNum:    1,
		EpisodeNum:   1,
		ScrapeStatus: "pending",
	}
	if err := repos.DB.Create(&tvEpisode).Error; err != nil {
		t.Fatal(err)
	}

	// 2. 同一目录下的剧场版文件
	movieMedia := model.Media{
		LibraryID:    lib.ID,
		Title:        "鬼灭之刃 剧场版 无限列车篇",
		Path:         `/media/anime/鬼灭之刃/鬼灭之刃 剧场版 无限列车篇.mkv`,
		ScrapeStatus: "pending",
	}
	if err := repos.DB.Create(&movieMedia).Error; err != nil {
		t.Fatal(err)
	}

	// 刮削剧集
	if err := scraper.EnrichOne(t.Context(), &tvEpisode); err != nil {
		t.Fatalf("enrich tv episode: %v", err)
	}
	gotTV, err := repos.Media.FindByID(t.Context(), tvEpisode.ID)
	if err != nil || gotTV == nil {
		t.Fatalf("load tv episode: %v", err)
	}
	if gotTV.ScrapeStatus != "matched" || gotTV.TMDbID != 85937 {
		t.Fatalf("tv episode scrape mismatch: status=%s, tmdb_id=%d", gotTV.ScrapeStatus, gotTV.TMDbID)
	}

	// 刮削剧场版
	if err := scraper.EnrichOne(t.Context(), &movieMedia); err != nil {
		t.Fatalf("enrich movie: %v", err)
	}
	gotMovie, err := repos.Media.FindByID(t.Context(), movieMedia.ID)
	if err != nil || gotMovie == nil {
		t.Fatalf("load movie: %v", err)
	}
	if gotMovie.ScrapeStatus != "matched" || gotMovie.TMDbID != 635302 {
		t.Fatalf("theatrical movie scrape mismatch: status=%s, tmdb_id=%d, want 635302", gotMovie.ScrapeStatus, gotMovie.TMDbID)
	}
	if gotMovie.Title != "鬼灭之刃 剧场版 无限列车篇" {
		t.Fatalf("theatrical movie title = %q, want '鬼灭之刃 剧场版 无限列车篇'", gotMovie.Title)
	}
}

func TestClassifyAnimeTheatricalMovie(t *testing.T) {
	categories := map[string]string{
		"animation_movie": "动画电影",
		"jp_anime":        "日番",
		"chinese_movie":   "华语电影",
	}
	tests := []struct {
		title     string
		mediaType string
		want      string
	}{
		{
			title:     "名侦探柯南 剧场版01 引爆摩天楼",
			mediaType: "movie",
			want:      "动画电影",
		},
		{
			title:     "鬼灭之刃 剧场版 无限列车篇",
			mediaType: "movie",
			want:      "动画电影",
		},
		{
			title:     "鬼灭之刃 S01E01",
			mediaType: "tv",
			want:      "日番",
		},
	}

	for _, tc := range tests {
		input := mediaClassifyInput{
			MediaType: tc.mediaType,
			Title:     tc.title,
			Languages: []string{"JA"},
			Countries: []string{"JP"},
			Genres:    []string{"Animation"},
		}
		got := classifyMediaCategory(input, categories)
		if got != tc.want {
			t.Fatalf("classifyMediaCategory(%q, %q) = %q, want %q", tc.title, tc.mediaType, got, tc.want)
		}
	}
}
