// Package service — TMDb metadata provider.
//
// TMDbProvider implements the (minimal) MetadataProvider interface and uses
// the public The Movie Database REST API. The API key is taken from
// secrets.tmdb_api_key; when empty the provider returns nil from every
// method so the scraper can no-op gracefully.
//
// We only call the two endpoints the scrape pipeline actually needs:
//
//	GET /search/movie?query=...&year=...
//	GET /movie/{id}?language=zh-CN
//
// TV / anime support follows the same pattern; for the bootstrap we expose
// a single SearchMovie path so that the home page and library gallery can
// show real posters as soon as a TMDb key is configured.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

// TMDbProvider talks to https://api.themoviedb.org/3.
type TMDbProvider struct {
	cfg       *config.Config
	log       *zap.Logger
	client    *http.Client
	base      string
	imgCDN    string
	apiConfig *APIConfigService
}

// NewTMDbProvider is the constructor. APIBase / image CDN can be overridden
// via secrets.tmdb_api_proxy + tmdb_image_proxy for users behind GFW.
// apiConfig is optional; when non-nil, the provider will also check the
// api_configs table for TMDB API key.
func NewTMDbProvider(cfg *config.Config, log *zap.Logger, apiConfig *APIConfigService) *TMDbProvider {
	base := cfg.Secrets.TMDbAPIProxy
	if base == "" {
		base = "https://api.themoviedb.org/3"
	}
	img := cfg.Secrets.TMDbImageProxy
	if img == "" {
		img = "https://image.tmdb.org/t/p"
	}
	return &TMDbProvider{
		cfg:       cfg,
		log:       log,
		apiConfig: apiConfig,
		base:      base,
		imgCDN:    img,
		// 默认 8s 超时：首页同时调 trending + popular，15s 太久会让用户感觉
		// 卡死。如果 TMDb 真有问题，handler 层会快速降级返回空列表。
		// 同时让 client 读取 HTTP(S)_PROXY 与 Windows 本机系统代理。
		client: NewExternalHTTPClient(8 * time.Second),
	}
}

// Enabled reports whether the operator has supplied an API key.
// It checks both the config file and the database (via apiConfig).
func (t *TMDbProvider) Enabled() bool {
	// Fast path: check config
	if t.cfg.Secrets.TMDbAPIKey != "" {
		return true
	}
	// Secondary check: if we have apiConfig, the key might be in the database
	// We can't query the database here (no ctx), so we rely on the caller
	// to check properly before making API calls.
	// The actual key resolution happens in resolveAPIKey(ctx).
	return t.apiConfig != nil
}

// resolveAPIKey returns the TMDb API key, checking config first, then database.
func (t *TMDbProvider) resolveAPIKey(ctx context.Context) string {
	// Check config first (fast path)
	if t.cfg.Secrets.TMDbAPIKey != "" {
		t.log.Debug("tmdb: using API key from config file")
		return t.cfg.Secrets.TMDbAPIKey
	}
	// Fall back to database
	if t.apiConfig != nil {
		resolved, err := t.apiConfig.Resolve(ctx, "tmdb")
		if err != nil {
			t.log.Warn("tmdb: failed to resolve API key from database", zap.Error(err))
		} else if resolved.APIKey == "" {
			t.log.Warn("tmdb: API key is empty in database")
		} else {
			t.log.Debug("tmdb: using API key from database")
			return resolved.APIKey
		}
	} else {
		t.log.Warn("tmdb: apiConfig is nil, cannot resolve API key from database")
	}
	return ""
}

// resolveBaseURL returns the TMDb base URL, checking config first, then database.
func (t *TMDbProvider) resolveBaseURL(ctx context.Context) string {
	// Check config first
	base := t.cfg.Secrets.TMDbAPIProxy
	if base == "" {
		base = "https://api.themoviedb.org/3"
	}
	// Override from database if available
	if t.apiConfig != nil {
		resolved, err := t.apiConfig.Resolve(ctx, "tmdb")
		if err == nil && resolved.BaseURL != "" {
			base = resolved.BaseURL
		}
	}
	return base
}

func (t *TMDbProvider) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("tmdb %s: %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ResolveIMDbID 用 TMDb 的 external_ids 端点把 TMDb 条目解析成 IMDb id。
//
// 这是为只认 IMDb id 的片段源（IntroDB）准备的。结果不做本地缓存：TMDb 的
// external_ids 响应很小，而片段查询本身已经有 30 天/24 小时的正负缓存兜底，
// 再叠一层只会引入失效问题。
//
// TMDb 未配置或上游不可达时返回空字符串与 error，调用方据此跳过该源，
// 而不是把所有媒体都记成「查不到」。
func (t *TMDbProvider) ResolveIMDbID(ctx context.Context, tmdbID int, isMovie bool) (string, error) {
	if t == nil || t.cfg == nil || tmdbID <= 0 {
		return "", nil
	}
	apiKey := t.resolveAPIKey(ctx)
	if apiKey == "" {
		return "", nil
	}
	mediaType := "tv"
	if isMovie {
		mediaType = "movie"
	}
	u := t.resolveBaseURL(ctx) + "/" + mediaType + "/" + fmt.Sprint(tmdbID) +
		"/external_ids?api_key=" + url.QueryEscape(apiKey)
	var r struct {
		IMDbID string `json:"imdb_id"`
	}
	if err := t.getJSON(ctx, u, &r); err != nil {
		return "", err
	}
	return strings.TrimSpace(r.IMDbID), nil
}
