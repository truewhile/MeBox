package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/repository"
)

type metadataProbeFixture struct {
	provider string
	path     string
	key      string
	body     string
}

var metadataProbeFixtures = []metadataProbeFixture{
	{"bangumi", "/v0/subjects/253", "", `{"id":253,"name":"カウボーイビバップ","type":2}`},
	{"thetvdb", "/login", "tvdb-test-secret", `{"status":"success","data":{"token":"test-jwt"}}`},
	{"fanart", "/movies/550", "fanart+test&secret", `{"name":"Fight Club","tmdb_id":"550","imdb_id":"tt0137523","movieposter":[]}`},
	{"douban", "/j/subject_suggest", "bid=test-cookie", `[{"id":"3541415","title":"盗梦空间","year":"2010","type":"movie"}]`},
}

func newMetadataConnectionTestService(t *testing.T) *ApiConfigService {
	t.Helper()
	log := zap.NewNop()
	return NewApiConfigService(&config.Config{}, log, repository.New(newServiceTestDB(t)), NewCryptoService("metadata-test-secret", log))
}

func saveMetadataProbeConfig(t *testing.T, svc *ApiConfigService, provider, baseURL, key string) {
	t.Helper()
	if _, err := svc.Upsert(t.Context(), provider, key, baseURL, "", true); err != nil {
		t.Fatal(err)
	}
}

// Removing a dispatch branch, ignoring the saved URL/key, or using the wrong
// endpoint must fail this integration test (real database and HTTP server).
func TestMetadataConnectionUsesSavedConfig(t *testing.T) {
	svc := newMetadataConnectionTestService(t)
	for _, fixture := range metadataProbeFixtures {
		t.Run(fixture.provider, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/proxy"+fixture.path {
					t.Errorf("request path = %q", r.URL.Path)
				}
				if r.Header.Get("User-Agent") == "" {
					t.Error("missing User-Agent")
				}
				if fixture.provider == "thetvdb" {
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if r.Method != http.MethodPost || body["apikey"] != fixture.key || r.Header.Get("Content-Type") != "application/json" {
						t.Error("TheTVDB login must POST the saved, decrypted API key as JSON")
					}
				} else if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				if fixture.provider == "fanart" && r.URL.Query().Get("api_key") != fixture.key {
					t.Error("Fanart API key was not escaped/passed correctly")
				}
				if fixture.provider == "douban" {
					if r.Header.Get("Cookie") != fixture.key || r.Header.Get("Referer") != "https://movie.douban.com/" || r.URL.Query().Get("q") == "" {
						t.Error("Douban search must carry the configured cookie, referer and query")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(fixture.body))
			}))
			defer server.Close()
			saveMetadataProbeConfig(t, svc, fixture.provider, "  "+server.URL+"/proxy/  ", fixture.key)
			result, err := svc.TestConnection(t.Context(), fixture.provider)
			if err != nil || result != "success" {
				t.Fatalf("TestConnection = %q, %v, want success", result, err)
			}
			if calls != 1 {
				t.Errorf("HTTP calls = %d, want 1", calls)
			}
		})
	}
}

func TestMetadataConnectionRejectsBadResponses(t *testing.T) {
	svc := newMetadataConnectionTestService(t)
	for _, fixture := range metadataProbeFixtures {
		for _, response := range []struct {
			name   string
			status int
			body   string
			result string
		}{
			{"unauthorized", 401, `{"error":"denied"}`, "invalid"},
			{"forbidden", 403, `<html>Access denied</html>`, "error"},
			{"rate limited", 429, `{}`, "error"},
			{"upstream error", 503, `{}`, "error"},
			{"challenge page", 200, `<html>Please log in or complete verification</html>`, "error"},
			{"empty body", 200, ``, "error"},
			{"wrong schema", 200, `{"error":"intercepted"}`, "error"},
			{"null", 200, `null`, "error"},
			{"trailing garbage", 200, fixture.body + `<html>blocked</html>`, "error"},
		} {
			t.Run(fixture.provider+"/"+response.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(response.status)
					_, _ = w.Write([]byte(response.body))
				}))
				defer server.Close()
				saveMetadataProbeConfig(t, svc, fixture.provider, server.URL, fixture.key)
				result, err := svc.TestConnection(t.Context(), fixture.provider)
				if err == nil || result != response.result {
					t.Fatalf("TestConnection = %q, %v, want %q and error", result, err, response.result)
				}
				if fixture.key != "" && strings.Contains(err.Error(), fixture.key) {
					t.Error("error exposes configured credentials")
				}
			})
		}
	}
}

func TestMetadataConnectionRequiresAPIKeys(t *testing.T) {
	svc := newMetadataConnectionTestService(t)
	for _, provider := range []string{"thetvdb", "fanart"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("must reject missing key before making a request")
			}))
			defer server.Close()
			saveMetadataProbeConfig(t, svc, provider, server.URL, "  ")
			result, err := svc.TestConnection(t.Context(), provider)
			if result != "error" || err == nil || !strings.Contains(err.Error(), "API key") {
				t.Fatalf("TestConnection = %q, %v, want missing API key error", result, err)
			}
		})
	}
}

func TestMetadataConnectionBangumiValidatesConfiguredToken(t *testing.T) {
	svc := newMetadataConnectionTestService(t)
	for _, valid := range []bool{true, false} {
		name := "valid"
		if !valid {
			name = "invalid"
		}
		t.Run(name, func(t *testing.T) {
			meCalls, subjectCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer bangumi-test-token" {
					t.Error("missing saved Bangumi bearer token")
				}
				switch r.URL.Path {
				case "/v0/me":
					meCalls++
					if !valid {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					_, _ = w.Write([]byte(`{"id":42,"username":"test-user"}`))
				case "/v0/subjects/253":
					subjectCalls++
					_, _ = w.Write([]byte(`{"id":253,"name":"カウボーイビバップ"}`))
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			saveMetadataProbeConfig(t, svc, "bangumi", server.URL, "bangumi-test-token")
			result, err := svc.TestConnection(t.Context(), "bangumi")
			if valid && (result != "success" || err != nil || subjectCalls != 1) {
				t.Fatalf("valid token: result=%q err=%v subjectCalls=%d", result, err, subjectCalls)
			}
			if !valid && (result != "invalid" || err == nil || subjectCalls != 0) {
				t.Fatalf("invalid token: result=%q err=%v subjectCalls=%d", result, err, subjectCalls)
			}
			if meCalls != 1 {
				t.Errorf("identity requests = %d, want 1", meCalls)
			}
		})
	}
}

func TestMetadataConnectionDoubanDoesNotRequireCookie(t *testing.T) {
	svc := newMetadataConnectionTestService(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("unexpected Cookie for public search")
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	saveMetadataProbeConfig(t, svc, "douban", server.URL, "")
	result, err := svc.TestConnection(t.Context(), "douban")
	if result != "success" || err != nil {
		t.Fatalf("public search with empty results = %q, %v", result, err)
	}
}

func TestMetadataConnectionHonorsContext(t *testing.T) {
	svc := newMetadataConnectionTestService(t)
	for _, fixture := range metadataProbeFixtures {
		t.Run(fixture.provider, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			saveMetadataProbeConfig(t, svc, fixture.provider, server.URL, fixture.key)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			go func() {
				select {
				case <-started:
					cancel()
				case <-ctx.Done():
				}
			}()
			result, err := svc.TestConnection(ctx, fixture.provider)
			if result != "error" || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled request = %q, %v, want context.Canceled", result, err)
			}
		})
	}
}

func TestMetadataConnectionTransportErrorsHideKeys(t *testing.T) {
	svc := newMetadataConnectionTestService(t)
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	key := "sensitive-fanart-key"
	saveMetadataProbeConfig(t, svc, "fanart", server.URL, key)
	result, err := svc.TestConnection(t.Context(), "fanart")
	if result != "error" || err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("transport failure = %q, %v, want error without credentials", result, err)
	}
}
