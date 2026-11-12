package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestMetaTubeProviderSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/movies/search" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query().Get("q")
		if q != "IPX-235" {
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer secret-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		results := struct {
			Data []MetaTubeSearchResult `json:"data"`
		}{
			Data: []MetaTubeSearchResult{
				{
					ID:          "123456",
					Number:      "IPX-235",
					Title:       "相沢みなみ 専属デビュー",
					Provider:    "javdb",
					Actors:      []string{"相沢みなみ"},
					CoverURL:    "https://example.com/cover.jpg",
					ReleaseDate: "2018-11-13",
					Score:       4.6,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(results)
	}))
	defer server.Close()

	provider := NewMetaTubeProvider(zap.NewNop())
	cfg := MetaTubeConfig{
		ServerURL: server.URL,
		Token:     "secret-token",
	}

	matches, err := provider.Search(context.Background(), cfg, "IPX-235")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	m := matches[0]
	if m.Provider != "metatube" {
		t.Errorf("expected provider metatube, got %q", m.Provider)
	}
	if m.OriginalName != "IPX-235" {
		t.Errorf("expected original name IPX-235, got %s", m.OriginalName)
	}
	if m.Year != 2018 {
		t.Errorf("expected year 2018, got %d", m.Year)
	}
	if !m.NSFW {
		t.Errorf("expected NSFW true")
	}
	if len(m.Genres) != 0 {
		t.Errorf("actor names must not be persisted as genres: %v", m.Genres)
	}
	if len(m.People) != 1 || m.People[0]["Name"] != "相沢みなみ" || m.People[0]["Type"] != "Actor" {
		t.Errorf("unexpected people: %#v", m.People)
	}
	if want := server.URL + "/v1/images/primary/javdb/123456?auto=false&pos=-1&quality=90&ratio=-1&url=https%3A%2F%2Fexample.com%2Fcover.jpg"; m.PosterURL != want {
		t.Errorf("poster URL = %q, want %q", m.PosterURL, want)
	}
	if want := server.URL + "/v1/images/backdrop/javdb/123456?quality=90"; m.BackdropURL != want {
		t.Errorf("backdrop URL = %q, want %q", m.BackdropURL, want)
	}
}

func TestMetaTubeProviderGetMovie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/movies/javdb/123456" {
			http.NotFound(w, r)
			return
		}

		info := struct {
			Data MetaTubeMovieInfo `json:"data"`
		}{
			Data: MetaTubeMovieInfo{
				ID:            "123456",
				Number:        "IPX-235",
				Title:         "相沢みなみ 専属デビュー",
				Summary:       "超绝美少女相沢みなみ出道作品！",
				Provider:      "javdb",
				Actors:        []string{"相沢みなみ"},
				Directors:     []string{"导演A"},
				Maker:         "IDEA POCKET",
				Label:         "Tissue",
				Genres:        []string{"单体作品", "美少女"},
				Score:         4.8,
				ReleaseDate:   "2018-11-13",
				CoverURL:      "https://example.com/cover.jpg",
				BigCoverURL:   "https://example.com/big_cover.jpg",
				PreviewImages: []string{"https://example.com/preview1.jpg"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info)
	}))
	defer server.Close()

	provider := NewMetaTubeProvider(zap.NewNop())
	cfg := MetaTubeConfig{
		ServerURL: server.URL,
		CropCover: true,
	}

	match, err := provider.GetMovie(context.Background(), cfg, "javdb", "123456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if match == nil {
		t.Fatal("expected non-nil match")
	}
	if match.Overview != "超绝美少女相沢みなみ出道作品！" {
		t.Errorf("unexpected overview: %s", match.Overview)
	}
	// IPX-235 是常规 DVD 番号：服务端不得开启人脸定位（auto=false），否则主簇会落到封底的剧照拼贴。
	if want := server.URL + "/v1/images/primary/javdb/123456?auto=false&pos=1&quality=90&ratio=-1&url=https%3A%2F%2Fexample.com%2Fbig_cover.jpg"; match.PosterURL != want {
		t.Errorf("poster URL = %q, want %q", match.PosterURL, want)
	}
	if want := server.URL + "/v1/images/backdrop/javdb/123456?quality=90"; match.BackdropURL != want {
		t.Errorf("backdrop URL = %q, want %q", match.BackdropURL, want)
	}
	if match.Year != 2018 {
		t.Errorf("expected year 2018, got %d", match.Year)
	}
}

func TestMetaTubeProviderTestConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/providers" {
			http.NotFound(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer valid-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		resp := MetaTubeProvidersResponse{}
		resp.Data.MovieProviders = map[string]string{
			"JavBus": "https://www.javbus.com/",
			"FANZA":  "https://www.dmm.co.jp/",
			"FC2":    "https://adult.contents.fc2.com/",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewMetaTubeProvider(zap.NewNop())

	// Test valid token
	res, err := provider.TestConnection(context.Background(), server.URL, "valid-token")
	if err != nil || !res.Success {
		t.Fatalf("expected success, got %v, err=%v", res, err)
	}
	if len(res.Providers) != 3 {
		t.Errorf("expected 3 providers, got %d (%v)", len(res.Providers), res.Providers)
	}

	// Test invalid token
	resFail, errFail := provider.TestConnection(context.Background(), server.URL, "wrong-token")
	if errFail == nil && resFail.Success {
		t.Fatalf("expected failure for wrong token, got %v", resFail)
	}
}

// MetaTube 代理的 auto 参数决定服务端是否用 Pigo 做人脸定位裁切。
// 常规 DVD 番号的封套封底是剧照拼贴、密布人脸，开启 auto 会裁出「半张封底」；
// 只有 FC2 / HEYZO / 素人 / 无码这类构图不固定的封面才应该开启。
func TestMetaTubeArtworkURLsFaceDetectionPolicy(t *testing.T) {
	cfg := MetaTubeConfig{ServerURL: "https://mt.example", CropCover: true}
	cases := []struct {
		code     string
		wantAuto string
	}{
		{"IPX-235", "false"},
		{"SSNI-677", "false"},
		{"NGOD-361", "false"},
		{"FC2-PPV-482896", "true"},
		{"HEYZO-1234", "true"},
		{"123456-789", "true"},
	}
	for _, c := range cases {
		poster, _ := metaTubeArtworkURLs(cfg, "javdb", "123456", "https://example.com/pl.jpg", c.code)
		if !strings.Contains(poster, "auto="+c.wantAuto) {
			t.Errorf("%s: poster URL = %q, want auto=%s", c.code, poster, c.wantAuto)
		}
		if !strings.Contains(poster, "pos=1") {
			t.Errorf("%s: poster URL = %q, want pos=1", c.code, poster)
		}
	}
}
