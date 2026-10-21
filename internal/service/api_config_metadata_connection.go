package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

// Bangumi's public metadata endpoints work without a token. If one was saved,
// also check /v0/me: public endpoints alone may accept an expired token.
func (s *ApiConfigService) testBangumi(ctx context.Context, cfg *model.APIConfig) (string, error) {
	req, err := newMetadataProbeRequest(ctx, cfg, "https://api.bgm.tv", "/v0/subjects/253", http.MethodGet, nil)
	if err != nil {
		return "error", err
	}
	if token := strings.TrimSpace(cfg.APIKey); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		identityReq := req.Clone(ctx)
		identityURL := *req.URL
		identityURL.Path = strings.TrimSuffix(identityURL.Path, "/subjects/253") + "/me"
		identityReq.URL = &identityURL
		var identity struct {
			ID int `json:"id"`
		}
		if result, err := testMetadataProbeJSON(identityReq, "Bangumi", &identity); err != nil {
			return result, err
		}
		if identity.ID <= 0 {
			return "error", errors.New("Bangumi returned an invalid user response")
		}
	}
	var subject struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if result, err := testMetadataProbeJSON(req, "Bangumi", &subject); err != nil {
		return result, err
	}
	if subject.ID != 253 || strings.TrimSpace(subject.Name) == "" {
		return "error", errors.New("Bangumi returned an invalid subject response")
	}
	return "success", nil
}

func (s *ApiConfigService) testTheTVDB(ctx context.Context, cfg *model.APIConfig) (string, error) {
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		return "error", errors.New("TheTVDB API key is required")
	}
	body, err := json.Marshal(map[string]string{"apikey": key})
	if err != nil {
		return "error", err
	}
	req, err := newMetadataProbeRequest(ctx, cfg, "https://api4.thetvdb.com/v4", "/login", http.MethodPost, strings.NewReader(string(body)))
	if err != nil {
		return "error", err
	}
	req.Header.Set("Content-Type", "application/json")
	var response struct {
		Status string `json:"status"`
		Data   struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if result, err := testMetadataProbeJSON(req, "TheTVDB", &response); err != nil {
		return result, err
	}
	if response.Status != "success" || strings.TrimSpace(response.Data.Token) == "" {
		return "error", errors.New("TheTVDB login did not return a valid token")
	}
	return "success", nil
}

func (s *ApiConfigService) testFanart(ctx context.Context, cfg *model.APIConfig) (string, error) {
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		return "error", errors.New("Fanart API key is required")
	}
	req, err := newMetadataProbeRequest(ctx, cfg, "https://webservice.fanart.tv/v3", "/movies/550", http.MethodGet, nil)
	if err != nil {
		return "error", err
	}
	query := req.URL.Query()
	query.Set("api_key", key)
	req.URL.RawQuery = query.Encode()
	var response struct {
		TMDbID json.Number `json:"tmdb_id"`
		Name   string      `json:"name"`
	}
	if result, err := testMetadataProbeJSON(req, "Fanart", &response); err != nil {
		return result, err
	}
	if response.TMDbID.String() != "550" || strings.TrimSpace(response.Name) == "" {
		return "error", errors.New("Fanart returned an invalid movie response")
	}
	return "success", nil
}

func (s *ApiConfigService) testDouban(ctx context.Context, cfg *model.APIConfig) (string, error) {
	req, err := newMetadataProbeRequest(ctx, cfg, "https://movie.douban.com", "/j/subject_suggest", http.MethodGet, nil)
	if err != nil {
		return "error", err
	}
	query := req.URL.Query()
	query.Set("q", "Inception")
	req.URL.RawQuery = query.Encode()
	req.Header.Set("User-Agent", userAgents[0])
	req.Header.Set("Referer", "https://movie.douban.com/")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	if cookie := strings.TrimSpace(cfg.APIKey); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	var suggestions []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if result, err := testMetadataProbeJSON(req, "Douban", &suggestions); err != nil {
		return result, err
	}
	// An empty array is a valid search result; null or an error object is not.
	if suggestions == nil {
		return "error", errors.New("Douban returned an invalid search response (login or verification may be required)")
	}
	for _, suggestion := range suggestions {
		if suggestion.ID == "" || strings.TrimSpace(suggestion.Title) == "" {
			return "error", errors.New("Douban returned an invalid search suggestion")
		}
	}
	return "success", nil
}

func newMetadataProbeRequest(ctx context.Context, cfg *model.APIConfig, defaultBase, path, method string, body io.Reader) (*http.Request, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = defaultBase
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		// The saved base URL may contain credentials. Never echo it to the UI.
		return nil, fmt.Errorf("%s: invalid base URL", cfg.Provider)
	}
	req.Header.Set("User-Agent", "MeBox/0.1 (https://github.com/truewhile/MeBox)")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// Decode a bounded, complete JSON document rather than accepting any HTTP 200
// (reverse proxies and anti-bot/login pages often return 200 as well).
func testMetadataProbeJSON(req *http.Request, provider string, out any) (string, error) {
	client := NewExternalHTTPClient(10 * time.Second)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		// url.Error includes the full request URL, including Fanart's API key.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return "error", fmt.Errorf("%s connection failed: %w", provider, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "invalid", fmt.Errorf("%s authentication failed (HTTP 401): check configured credentials", provider)
	}
	if resp.StatusCode == http.StatusForbidden {
		return "error", fmt.Errorf("%s access denied (HTTP 403): check credentials, permissions or anti-bot verification", provider)
	}
	if resp.StatusCode != http.StatusOK {
		return "error", fmt.Errorf("%s API returned status %d", provider, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return "error", fmt.Errorf("%s response read failed: %w", provider, err)
	}
	if len(body) > 1<<20 {
		return "error", fmt.Errorf("%s response exceeds 1 MiB", provider)
	}
	if err := json.Unmarshal(body, out); err != nil {
		// Do not include the response body: it may echo credentials.
		return "error", fmt.Errorf("%s returned invalid JSON (login or verification page may have intercepted the request)", provider)
	}
	return "success", nil
}
