package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

func TestTMDbGetPeopleAndProfileImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/11/credits" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cast": []map[string]any{
				{"id": 101, "name": "演员甲", "character": "主角", "profile_path": "/actor.jpg", "order": 0},
			},
			"crew": []map[string]any{
				{"id": 201, "name": "导演甲", "job": "Director", "department": "Directing", "profile_path": "/director.jpg"},
				{"id": 202, "name": "编剧甲", "job": "Writer", "department": "Writing", "profile_path": "/writer.jpg"},
			},
		})
	}))
	defer server.Close()

	cfg := &config.Config{Secrets: config.SecretsConfig{
		TMDbAPIKey:     "test-key",
		TMDbAPIProxy:   server.URL,
		TMDbImageProxy: "https://image.example/t/p",
	}}
	provider := NewTMDbProvider(cfg, zap.NewNop(), nil)
	people, err := provider.GetPeople(context.Background(), 11, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 3 {
		t.Fatalf("people=%d want 3: %#v", len(people), people)
	}
	if people[0]["Id"] != "person~tmdb~101" || people[0]["Role"] != "主角" {
		t.Fatalf("unexpected actor: %#v", people[0])
	}
	if people[0]["PrimaryImageTag"] != "tmdb:/actor.jpg" {
		t.Fatalf("unexpected actor image tag: %#v", people[0])
	}
	if got := provider.ProfileImageURL("/actor.jpg"); got != "https://image.example/t/p/w300/actor.jpg" {
		t.Fatalf("profile image URL=%q", got)
	}
}

func TestTMDbGetPeopleUsesAggregateCreditsForTV(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tv/22/aggregate_credits" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cast": []map[string]any{
				{"id": 301, "name": "演员乙", "roles": []map[string]any{{"character": "角色乙"}}, "profile_path": "/tv-actor.jpg"},
			},
			"crew": []map[string]any{},
		})
	}))
	defer server.Close()

	cfg := &config.Config{Secrets: config.SecretsConfig{TMDbAPIKey: "test-key", TMDbAPIProxy: server.URL}}
	provider := NewTMDbProvider(cfg, zap.NewNop(), nil)
	people, err := provider.GetPeople(context.Background(), 22, "tv")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 1 || people[0]["Role"] != "角色乙" {
		t.Fatalf("unexpected TV people: %#v", people)
	}
}
