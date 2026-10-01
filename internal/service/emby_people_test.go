package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
)

func TestResolveMediaPeopleUsesSharedStemBesideKeepExtStrm(t *testing.T) {
	svc := newTestEmbyService(t)
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "竞女01.mkv.strm")
	nfoPath := filepath.Join(dir, "竞女01.nfo")
	if err := os.WriteFile(mediaPath, []byte("http://example/play"), 0o644); err != nil {
		t.Fatal(err)
	}
	nfo := `<movie>
<title>竞女01</title>
<director>导演甲</director>
<actor><name>演员乙</name><role>主角</role></actor>
</movie>`
	if err := os.WriteFile(nfoPath, []byte(nfo), 0o644); err != nil {
		t.Fatal(err)
	}

	people := svc.resolveMediaPeople(t.Context(), &model.Media{Path: mediaPath, Title: "竞女01"})
	if len(people) != 2 {
		t.Fatalf("people=%d want 2: %#v", len(people), people)
	}
	got := map[string]string{}
	for _, p := range people {
		got[p["Name"].(string)] = p["Type"].(string)
	}
	if got["导演甲"] != "Director" || got["演员乙"] != "Actor" {
		t.Fatalf("unexpected people: %#v", people)
	}
}

func TestResolveMediaPeopleStillReadsLegacyKeepExtNFO(t *testing.T) {
	svc := newTestEmbyService(t)
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "竞女01.mkv.strm")
	legacyNFO := filepath.Join(dir, "竞女01.mkv.nfo")
	if err := os.WriteFile(mediaPath, []byte("http://example/play"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyNFO, []byte(`<movie><director>旧导演</director></movie>`), 0o644); err != nil {
		t.Fatal(err)
	}

	people := svc.resolveMediaPeople(t.Context(), &model.Media{Path: mediaPath})
	if len(people) != 1 || people[0]["Name"] != "旧导演" {
		t.Fatalf("expected legacy keep_ext nfo people, got %#v", people)
	}
}

func TestResolveMediaPeopleFetchesTMDbAndCaches(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/11/credits" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cast": []map[string]any{
				{"id": 101, "name": "演员甲", "character": "主角", "profile_path": "/actor.jpg", "order": 0},
			},
		})
	}))
	defer server.Close()

	svc := newTestEmbyService(t)
	svc.SetTMDbProvider(NewTMDbProvider(&config.Config{Secrets: config.SecretsConfig{
		TMDbAPIKey:     "test-key",
		TMDbAPIProxy:   server.URL,
		TMDbImageProxy: "https://image.example/t/p",
	}}, zap.NewNop(), nil))
	media := &model.Media{Base: model.Base{ID: "movie-people-1"}, Title: "测试电影", Path: "/media/movies/test.mkv", TMDbID: 11}

	people := svc.resolveMediaPeople(t.Context(), media)
	peopleAgain := svc.resolveMediaPeople(t.Context(), media)
	if len(people) != 1 || len(peopleAgain) != 1 {
		t.Fatalf("people=%#v again=%#v", people, peopleAgain)
	}
	if hits.Load() != 1 {
		t.Fatalf("TMDb credits hits=%d want 1", hits.Load())
	}
	if people[0]["Id"] != "person~tmdb~101" || people[0]["PrimaryImageTag"] != "tmdb:/actor.jpg?p2" {
		t.Fatalf("unexpected person: %#v", people[0])
	}
	raw, err := svc.PersonImageURL(t.Context(), "person~tmdb~101", "Primary", "tmdb:/actor.jpg?p2")
	if err != nil {
		t.Fatal(err)
	}
	if raw != "https://image.example/t/p/w300/actor.jpg" {
		t.Fatalf("person image URL=%q", raw)
	}
}
