package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
)

type OpenSearchMediaBackend struct {
	baseURL  string
	index    string
	username string
	password string
	client   *http.Client
}

func NewOpenSearchMediaBackend(cfg config.SearchConfig) *OpenSearchMediaBackend {
	if strings.TrimSpace(cfg.Backend) != "opensearch" || strings.TrimSpace(cfg.OpenSearchURL) == "" {
		return nil
	}
	index := strings.TrimSpace(cfg.Index)
	if index == "" {
		index = "mebox_media"
	}
	return &OpenSearchMediaBackend{
		baseURL:  strings.TrimRight(strings.TrimSpace(cfg.OpenSearchURL), "/"),
		index:    index,
		username: strings.TrimSpace(cfg.Username),
		password: cfg.Password,
		client:   &http.Client{Timeout: 4 * time.Second},
	}
}

func (b *OpenSearchMediaBackend) SearchMediaIDs(ctx context.Context, query string, offset, limit int, filter MediaQueryFilter) ([]string, int64, error) {
	if b == nil || b.client == nil || b.baseURL == "" || b.index == "" {
		return nil, 0, fmt.Errorf("opensearch backend not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	must := []any{
		map[string]any{
			"multi_match": map[string]any{
				"query":     query,
				"fields":    []string{"title^4", "original_name^3", "genres^2", "path"},
				"type":      "best_fields",
				"operator":  "and",
				"fuzziness": "AUTO",
			},
		},
	}
	filters := []any{
		map[string]any{"term": map[string]any{"deleted": false}},
	}
	if !filter.IncludeNSFW {
		filters = append(filters, map[string]any{"term": map[string]any{"nsfw": false}})
	}
	if len(filter.AllowedLibraryIDs) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"library_id": filter.AllowedLibraryIDs}})
	}
	if len(filter.HiddenLibraryIDs) > 0 {
		filters = append(filters, map[string]any{"bool": map[string]any{
			"must_not": []any{map[string]any{"terms": map[string]any{"library_id": filter.HiddenLibraryIDs}}},
		}})
	}
	body := map[string]any{
		"from": offset,
		"size": limit,
		"_source": []string{
			"id",
		},
		"query": map[string]any{
			"bool": map[string]any{
				"must":   must,
				"filter": filters,
			},
		},
	}
	var resp struct {
		Hits struct {
			Total any `json:"total"`
			Hits  []struct {
				ID     string `json:"_id"`
				Source struct {
					ID string `json:"id"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := b.doJSON(ctx, http.MethodPost, "/"+url.PathEscape(b.index)+"/_search", body, &resp); err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		id := strings.TrimSpace(hit.Source.ID)
		if id == "" {
			id = strings.TrimSpace(hit.ID)
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, openSearchTotal(resp.Hits.Total), nil
}

func (b *OpenSearchMediaBackend) EnsureIndex(ctx context.Context) error {
	if err := b.do(ctx, http.MethodHead, "/"+url.PathEscape(b.index), nil, "", nil); err == nil {
		return nil
	}
	mapping := map[string]any{
		"mappings": map[string]any{
			"properties": map[string]any{
				"id":            map[string]any{"type": "keyword"},
				"library_id":    map[string]any{"type": "keyword"},
				"title":         map[string]any{"type": "text"},
				"original_name": map[string]any{"type": "text"},
				"path":          map[string]any{"type": "text"},
				"genres":        map[string]any{"type": "text"},
				"nsfw":          map[string]any{"type": "boolean"},
				"deleted":       map[string]any{"type": "boolean"},
				"created_at":    map[string]any{"type": "date"},
			},
		},
	}
	return b.doJSON(ctx, http.MethodPut, "/"+url.PathEscape(b.index), mapping, nil)
}

func (b *OpenSearchMediaBackend) IndexMedia(ctx context.Context, rows []model.Media) error {
	const batchSize = 500
	var failures []error
	for start := 0; start < len(rows); start += batchSize {
		if err := b.indexMediaBatch(ctx, rows[start:min(start+batchSize, len(rows))]); err != nil {
			failures = append(failures, err)
			if ctx.Err() != nil {
				break
			}
		}
	}
	return errors.Join(failures...)
}

type openSearchBulkResponse struct {
	Errors bool `json:"errors"`
	Items  []struct {
		Index struct {
			ID     string `json:"_id"`
			Status int    `json:"status"`
			Error  *struct {
				Type string `json:"type"`
			} `json:"error"`
		} `json:"index"`
	} `json:"items"`
}

func (b *OpenSearchMediaBackend) indexMediaBatch(ctx context.Context, rows []model.Media) error {
	if len(rows) == 0 {
		return nil
	}
	pending := rows
	var failures []error
	const attempts = 3
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(100*(1<<(attempt-1))) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errors.Join(append(failures, ctx.Err())...)
			case <-timer.C:
			}
		}
		response, err := b.submitMediaBulk(ctx, pending)
		if err != nil {
			if retryableOpenSearchError(err) && attempt < attempts-1 {
				continue
			}
			return errors.Join(append(failures, err)...)
		}
		if len(response.Items) != len(pending) {
			return errors.Join(append(failures, fmt.Errorf("opensearch bulk returned %d item results for %d documents", len(response.Items), len(pending)))...)
		}
		var retry []model.Media
		failed := false
		for i, item := range response.Items {
			result := item.Index
			if result.Status >= 200 && result.Status < 300 && result.Error == nil {
				continue
			}
			failed = true
			if retryableOpenSearchStatus(result.Status) && attempt < attempts-1 {
				retry = append(retry, pending[i])
				continue
			}
			errorType := "unknown"
			if result.Error != nil {
				errorType = result.Error.Type
			}
			failures = append(failures, fmt.Errorf("opensearch bulk document %q failed: status %d (%s)", pending[i].ID, result.Status, errorType))
		}
		if response.Errors && !failed {
			failures = append(failures, errors.New("opensearch bulk reported errors without failed item results"))
		}
		if len(retry) == 0 {
			break
		}
		pending = retry
	}
	return errors.Join(failures...)
}

func retryableOpenSearchStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func retryableOpenSearchError(err error) bool {
	var statusError *openSearchHTTPError
	if errors.As(err, &statusError) {
		return retryableOpenSearchStatus(statusError.status)
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func (b *OpenSearchMediaBackend) submitMediaBulk(ctx context.Context, rows []model.Media) (*openSearchBulkResponse, error) {
	if len(rows) == 0 {
		return &openSearchBulkResponse{}, nil
	}
	var bulk bytes.Buffer
	enc := json.NewEncoder(&bulk)
	for _, row := range rows {
		if err := enc.Encode(map[string]any{"index": map[string]any{"_index": b.index, "_id": row.ID}}); err != nil {
			return nil, err
		}
		if err := enc.Encode(map[string]any{
			"id":            row.ID,
			"library_id":    row.LibraryID,
			"title":         row.Title,
			"original_name": row.OriginalName,
			"path":          row.Path,
			"genres":        row.Genres,
			"nsfw":          row.NSFW,
			"deleted":       row.DeletedAt.Valid,
			"created_at":    row.CreatedAt,
		}); err != nil {
			return nil, err
		}
	}
	var response openSearchBulkResponse
	if err := b.do(ctx, http.MethodPost, "/_bulk", &bulk, "application/x-ndjson", &response); err != nil {
		return nil, err
	}
	return &response, nil
}

type openSearchHTTPError struct {
	method string
	path   string
	status int
}

func (e *openSearchHTTPError) Error() string {
	return fmt.Sprintf("opensearch %s %s returned %d", e.method, e.path, e.status)
}

func (b *OpenSearchMediaBackend) doJSON(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	return b.do(ctx, method, path, reader, "application/json", out)
}

func (b *OpenSearchMediaBackend) do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, b.baseURL+path, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if b.username != "" {
		req.SetBasicAuth(b.username, b.password)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return &openSearchHTTPError{method: method, path: path, status: resp.StatusCode}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func openSearchTotal(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case map[string]any:
		if n, ok := v["value"].(float64); ok {
			return int64(n)
		}
	}
	return 0
}
