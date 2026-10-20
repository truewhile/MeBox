package repository

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
)

func TestOpenSearchMediaBackendSearchesIDs(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["from"].(float64) != 5 || body["size"].(float64) != 10 {
			t.Fatalf("paging body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": map[string]any{
				"total": map[string]any{"value": 2},
				"hits": []any{
					map[string]any{"_id": "m-1", "_source": map[string]any{"id": "m-1"}},
					map[string]any{"_id": "m-2", "_source": map[string]any{"id": "m-2"}},
				},
			},
		})
	}))
	defer upstream.Close()

	backend := NewOpenSearchMediaBackend(config.SearchConfig{
		Backend:       "opensearch",
		OpenSearchURL: upstream.URL,
		Index:         "media-test",
	})
	ids, total, err := backend.SearchMediaIDs(t.Context(), "流浪地球", 5, 10, MediaQueryFilter{
		IncludeNSFW:       false,
		AllowedLibraryIDs: []string{"lib-1"},
		HiddenLibraryIDs:  []string{"adult"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/media-test/_search" {
		t.Fatalf("path = %q", gotPath)
	}
	if total != 2 || len(ids) != 2 || ids[0] != "m-1" || ids[1] != "m-2" {
		t.Fatalf("ids=%#v total=%d", ids, total)
	}
}

func bulkRequestIDs(r *http.Request) []string {
	var ids []string
	scanner := bufio.NewScanner(r.Body)
	for scanner.Scan() {
		var action struct {
			Index struct {
				ID string `json:"_id"`
			} `json:"index"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &action)
		ids = append(ids, action.Index.ID)
		if !scanner.Scan() {
			break
		}
	}
	return ids
}

func writeBulkResults(w http.ResponseWriter, ids []string, statuses map[string]int) {
	items := make([]any, 0, len(ids))
	failed := false
	for _, id := range ids {
		status := statuses[id]
		if status == 0 {
			status = http.StatusCreated
		}
		result := map[string]any{"_id": id, "status": status}
		if status >= 400 {
			failed = true
			result["error"] = map[string]any{"type": "test_failure"}
		}
		items = append(items, map[string]any{"index": result})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": failed, "items": items})
}

func TestOpenSearchBulkRetriesOnlyTransientFailedDocuments(t *testing.T) {
	var requests [][]string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := bulkRequestIDs(r)
		requests = append(requests, ids)
		statuses := map[string]int{}
		if len(requests) == 1 {
			statuses["retry"] = http.StatusTooManyRequests
			statuses["invalid"] = http.StatusBadRequest
		}
		writeBulkResults(w, ids, statuses)
	}))
	defer upstream.Close()
	backend := NewOpenSearchMediaBackend(config.SearchConfig{Backend: "opensearch", OpenSearchURL: upstream.URL})
	rows := []model.Media{{Base: model.Base{ID: "ok"}}, {Base: model.Base{ID: "retry"}}, {Base: model.Base{ID: "invalid"}}}
	err := backend.IndexMedia(t.Context(), rows)
	if err == nil || !strings.Contains(err.Error(), `"invalid"`) {
		t.Fatalf("error = %v, want permanent document failure", err)
	}
	if len(requests) != 2 || strings.Join(requests[1], ",") != "retry" {
		t.Fatalf("requests = %#v; successful/permanent documents must not be retried", requests)
	}
}

func TestOpenSearchBulkBoundsRetriesAndRejectsMalformedResponses(t *testing.T) {
	for _, scenario := range []string{"transient-item", "transient-http", "invalid-http", "missing-results"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch scenario {
				case "transient-item":
					writeBulkResults(w, bulkRequestIDs(r), map[string]int{"m1": http.StatusServiceUnavailable})
				case "transient-http":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "invalid-http":
					w.WriteHeader(http.StatusBadRequest)
				case "missing-results":
					fmt.Fprint(w, `{"errors":false,"items":[]}`)
				}
			}))
			defer upstream.Close()
			backend := NewOpenSearchMediaBackend(config.SearchConfig{Backend: "opensearch", OpenSearchURL: upstream.URL})
			if err := backend.IndexMedia(t.Context(), []model.Media{{Base: model.Base{ID: "m1"}}}); err == nil {
				t.Fatal("failed indexing was reported as successful")
			}
			want := 1
			if strings.HasPrefix(scenario, "transient") {
				want = 3
			}
			if calls != want {
				t.Fatalf("calls = %d, want %d", calls, want)
			}
		})
	}
}

func TestOpenSearchBulkHonorsCancellationDuringRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeBulkResults(w, bulkRequestIDs(r), map[string]int{"m1": http.StatusTooManyRequests})
		cancel()
	}))
	defer upstream.Close()
	backend := NewOpenSearchMediaBackend(config.SearchConfig{Backend: "opensearch", OpenSearchURL: upstream.URL})
	err := backend.IndexMedia(ctx, []model.Media{{Base: model.Base{ID: "m1"}}})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
}

func TestMediaUpsertBatchIndexesCommittedRowsTogether(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.Close()
	if err := db.AutoMigrate(&model.Media{}); err != nil {
		t.Fatal(err)
	}
	repos := New(db)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		ids := bulkRequestIDs(r)
		if len(ids) != 5 {
			t.Errorf("bulk contains %d documents, want 5", len(ids))
		}
		// With only one DB connection this also detects network work inside the
		// transaction: a separate read could not observe the committed batch.
		var count int64
		if err := db.WithContext(ctx).Model(&model.Media{}).Count(&count).Error; err != nil || count != 5 {
			t.Errorf("committed rows = %d, error = %v", count, err)
		}
		writeBulkResults(w, ids, nil)
	}))
	defer upstream.Close()
	repos.Media.SetSearchBackend(NewOpenSearchMediaBackend(config.SearchConfig{Backend: "opensearch", OpenSearchURL: upstream.URL}))
	var rows []*model.Media
	for i := 0; i < 5; i++ {
		rows = append(rows, &model.Media{LibraryID: "lib", Path: fmt.Sprintf("/media/%d.mkv", i)})
	}
	if err := repos.Media.UpsertBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("index requests = %d, want one bulk", calls)
	}
}
