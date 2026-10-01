package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// MetaTubeProvider 是对接 MetaTube Server REST API 的刮削客户端。
type MetaTubeProvider struct {
	log    *zap.Logger
	client *http.Client
}

// NewMetaTubeProvider 创建 MetaTube 刮削服务实例。
func NewMetaTubeProvider(log *zap.Logger) *MetaTubeProvider {
	return &MetaTubeProvider{
		log:    log,
		client: NewExternalHTTPClient(15 * time.Second),
	}
}

// Search 执行番号/关键词搜索，返回标准化 Match 列表。
func (p *MetaTubeProvider) Search(ctx context.Context, cfg MetaTubeConfig, query string) ([]*Match, error) {
	serverURL := strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
	if serverURL == "" {
		return nil, errors.New("metatube server url not configured")
	}

	searchURL := fmt.Sprintf("%s/v1/movies/search?q=%s", serverURL, url.QueryEscape(query))
	if strings.TrimSpace(cfg.DefaultProvider) != "" {
		searchURL += "&provider=" + url.QueryEscape(strings.TrimSpace(cfg.DefaultProvider))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	p.applyAuthHeader(req, cfg.Token)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metatube search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("metatube search returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	var results []MetaTubeSearchResult
	var wrapped struct {
		Data []MetaTubeSearchResult `json:"data"`
	}
	if wrapErr := json.Unmarshal(bodyBytes, &wrapped); wrapErr == nil && len(wrapped.Data) > 0 {
		results = wrapped.Data
	} else if directErr := json.Unmarshal(bodyBytes, &results); directErr != nil {
		return nil, fmt.Errorf("decode metatube search response failed: %w", directErr)
	}

	if len(results) == 0 {
		return nil, nil
	}

	matches := make([]*Match, 0, len(results))
	for _, res := range results {
		match := p.convertSearchResultToMatch(cfg, query, &res)
		if match != nil {
			matches = append(matches, match)
		}
	}
	return matches, nil
}

// GetMovie 根据 Provider 与影片 ID 获取完整元数据。
func (p *MetaTubeProvider) GetMovie(ctx context.Context, cfg MetaTubeConfig, provider, id string) (*Match, error) {
	serverURL := strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
	if serverURL == "" {
		return nil, errors.New("metatube server url not configured")
	}

	movieURL := fmt.Sprintf("%s/v1/movies/%s/%s", serverURL, url.PathEscape(provider), url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, movieURL, nil)
	if err != nil {
		return nil, err
	}
	p.applyAuthHeader(req, cfg.Token)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metatube movie request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("metatube movie returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	var movie MetaTubeMovieInfo
	var wrapped struct {
		Data MetaTubeMovieInfo `json:"data"`
	}
	if wrapErr := json.Unmarshal(bodyBytes, &wrapped); wrapErr == nil && wrapped.Data.ID != "" {
		movie = wrapped.Data
	} else if directErr := json.Unmarshal(bodyBytes, &movie); directErr != nil {
		return nil, fmt.Errorf("decode metatube movie response failed: %w", directErr)
	}

	return p.convertMovieInfoToMatch(cfg, &movie), nil
}

// SearchAndGetBestMatch 执行搜索并拉取首个最佳结果的完整电影详情。
func (p *MetaTubeProvider) SearchAndGetBestMatch(ctx context.Context, cfg MetaTubeConfig, code string) (*Match, error) {
	results, err := p.Search(ctx, cfg, code)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}

	// 优先寻找带有 ID 和 Provider 的结果拉取详情
	for _, candidate := range results {
		if candidate.DoubanID != "" && candidate.TheTVDBID != "" {
			if detailed, err := p.GetMovie(ctx, cfg, candidate.TheTVDBID, candidate.DoubanID); err == nil && detailed != nil {
				return detailed, nil
			}
		}
	}

	return results[0], nil
}

// GetProviders 获取 MetaTube 服务端支持的 Provider 列表。
func (p *MetaTubeProvider) GetProviders(ctx context.Context, cfg MetaTubeConfig) ([]string, error) {
	serverURL := strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
	if serverURL == "" {
		return nil, errors.New("metatube server url not configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/v1/providers", nil)
	if err != nil {
		return nil, err
	}
	p.applyAuthHeader(req, cfg.Token)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metatube providers request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metatube providers returned status %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var respObj MetaTubeProvidersResponse
	if err := json.Unmarshal(bodyBytes, &respObj); err == nil && len(respObj.Data.MovieProviders) > 0 {
		providers := make([]string, 0, len(respObj.Data.MovieProviders))
		for prov := range respObj.Data.MovieProviders {
			providers = append(providers, prov)
		}
		sort.Strings(providers)
		return providers, nil
	}

	// 兼容纯数组返回
	var arrayProviders []string
	if err := json.Unmarshal(bodyBytes, &arrayProviders); err == nil {
		return arrayProviders, nil
	}

	return nil, fmt.Errorf("decode metatube providers failed")
}

// TestConnection 测试与 MetaTube 服务端的连通性。
func (p *MetaTubeProvider) TestConnection(ctx context.Context, serverURL, token string) (*MetaTubeTestResult, error) {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" {
		return &MetaTubeTestResult{Success: false, Error: "server URL is required"}, errors.New("server URL is required")
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/v1/providers", nil)
	if err != nil {
		return &MetaTubeTestResult{Success: false, Error: err.Error()}, err
	}
	p.applyAuthHeader(req, token)

	resp, err := p.client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return &MetaTubeTestResult{
			Success:   false,
			LatencyMs: latency,
			Error:     fmt.Sprintf("connection failed: %v", err),
		}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &MetaTubeTestResult{
			Success:   false,
			LatencyMs: latency,
			Error:     "unauthorized: invalid token",
		}, errors.New("unauthorized: invalid token")
	}

	if resp.StatusCode != http.StatusOK {
		return &MetaTubeTestResult{
			Success:   false,
			LatencyMs: latency,
			Error:     fmt.Sprintf("server returned HTTP %d", resp.StatusCode),
		}, fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var respObj MetaTubeProvidersResponse
	var providers []string
	if err := json.Unmarshal(bodyBytes, &respObj); err == nil && len(respObj.Data.MovieProviders) > 0 {
		for prov := range respObj.Data.MovieProviders {
			providers = append(providers, prov)
		}
		sort.Strings(providers)
	} else {
		_ = json.Unmarshal(bodyBytes, &providers)
	}

	return &MetaTubeTestResult{
		Success:   true,
		LatencyMs: latency,
		Providers: providers,
	}, nil
}

func (p *MetaTubeProvider) applyAuthHeader(req *http.Request, token string) {
	token = strings.TrimSpace(token)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Token", token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MeBox/1.0 (MetaTube Client)")
}

func metaTubePeople(movie *MetaTubeMovieInfo, enableActor bool) []map[string]any {
	if movie == nil {
		return nil
	}
	people := make([]map[string]any, 0, len(movie.Actors)+len(movie.Directors)+1)
	add := func(name, personType string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		personID := embyPersonID(name, personType)
		for _, existing := range people {
			if existing["Id"] == personID {
				return
			}
		}
		people = append(people, map[string]any{
			"Id":   personID,
			"Name": name,
			"Type": personType,
			"Role": personType,
		})
	}
	if enableActor {
		for _, actor := range movie.Actors {
			add(actor, "Actor")
		}
	}
	add(movie.Director, "Director")
	for _, director := range movie.Directors {
		add(director, "Director")
	}
	return people
}

func metaTubePeopleFromActors(actors []string) []map[string]any {
	people := make([]map[string]any, 0, len(actors))
	seen := map[string]bool{}
	for _, actor := range actors {
		actor = strings.TrimSpace(actor)
		if actor == "" {
			continue
		}
		personID := embyPersonID(actor, "Actor")
		if seen[personID] {
			continue
		}
		seen[personID] = true
		people = append(people, map[string]any{
			"Id":   personID,
			"Name": actor,
			"Type": "Actor",
			"Role": "Actor",
		})
	}
	return people
}

func (p *MetaTubeProvider) convertSearchResultToMatch(cfg MetaTubeConfig, query string, res *MetaTubeSearchResult) *Match {
	if res == nil {
		return nil
	}
	code := strings.TrimSpace(res.Number)
	if code == "" {
		code = normalizeAdultCode(query)
	}

	title := strings.TrimSpace(res.Title)
	if title == "" {
		title = code
	}
	formattedTitle := FormatAdultTitle(code, title)

	year := parseYearFromDate(res.ReleaseDate)

	posterSource := firstNonEmpty(res.BigCoverURL, res.CoverURL, res.BigThumbURL, res.ThumbURL)
	posterURL, backdropURL := metaTubeArtworkURLs(cfg, res.Provider, res.ID, posterSource)
	if posterURL == "" {
		posterURL = firstNonEmpty(res.BigThumbURL, res.ThumbURL, res.BigCoverURL, res.CoverURL)
	}
	if backdropURL == "" {
		backdropURL = firstNonEmpty(res.BigCoverURL, res.CoverURL, posterURL)
	}

	return &Match{
		Provider:     "metatube",
		MediaType:    "adult",
		Title:        formattedTitle,
		OriginalName: code,
		PosterURL:    posterURL,
		BackdropURL:  backdropURL,
		Year:         year,
		ReleaseDate:  cleanDateString(res.ReleaseDate),
		Rating:       res.Score,
		NSFW:         true,
		People:       metaTubePeopleFromActors(res.Actors),
		DoubanID:     res.ID,       // 借用字段存储原始 ID 便于详情反查
		TheTVDBID:    res.Provider, // 借用字段存储 Provider
	}
}

func (p *MetaTubeProvider) convertMovieInfoToMatch(cfg MetaTubeConfig, movie *MetaTubeMovieInfo) *Match {
	if movie == nil {
		return nil
	}

	code := strings.TrimSpace(movie.Number)
	if code == "" {
		code = movie.ID
	}

	title := strings.TrimSpace(movie.Title)
	if title == "" {
		title = code
	}
	formattedTitle := FormatAdultTitle(code, title)

	year := parseYearFromDate(movie.ReleaseDate)

	posterSource := firstNonEmpty(movie.BigCoverURL, movie.CoverURL, movie.BigThumbURL, movie.ThumbURL)
	posterURL, backdropURL := metaTubeArtworkURLs(cfg, movie.Provider, movie.ID, posterSource)
	if posterURL == "" {
		posterURL = firstNonEmpty(movie.BigThumbURL, movie.ThumbURL, movie.BigCoverURL, movie.CoverURL)
	}
	if backdropURL == "" {
		if len(movie.PreviewImages) > 0 && movie.PreviewImages[0] != "" {
			backdropURL = movie.PreviewImages[0]
		} else {
			backdropURL = firstNonEmpty(movie.BigCoverURL, movie.CoverURL, posterURL)
		}
	}

	genres := make([]string, 0, len(movie.Genres)+2)
	for _, g := range movie.Genres {
		if strings.TrimSpace(g) != "" {
			genres = append(genres, strings.TrimSpace(g))
		}
	}
	if strings.TrimSpace(movie.Maker) != "" {
		genres = append(genres, strings.TrimSpace(movie.Maker))
	}
	if strings.TrimSpace(movie.Label) != "" && movie.Label != movie.Maker {
		genres = append(genres, strings.TrimSpace(movie.Label))
	}

	return &Match{
		Provider:     "metatube",
		MediaType:    "adult",
		Title:        formattedTitle,
		OriginalName: code,
		Overview:     strings.TrimSpace(movie.Summary),
		PosterURL:    posterURL,
		BackdropURL:  backdropURL,
		Year:         year,
		ReleaseDate:  cleanDateString(movie.ReleaseDate),
		Rating:       movie.Score,
		Genres:       dedupeStrings(genres),
		NSFW:         true,
		People:       metaTubePeople(movie, cfg.EnableActor),
		DoubanID:     movie.ID,
		TheTVDBID:    movie.Provider,
	}
}

func metaTubeArtworkURLs(cfg MetaTubeConfig, provider, id, posterSource string) (string, string) {
	serverURL := strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
	provider = strings.TrimSpace(provider)
	id = strings.TrimSpace(id)
	if serverURL == "" || provider == "" || id == "" {
		return "", ""
	}

	imageURL := func(kind string, primary bool) string {
		base := fmt.Sprintf("%s/v1/images/%s/%s/%s", serverURL, kind, url.PathEscape(provider), url.PathEscape(id))
		values := url.Values{}
		values.Set("quality", "90")
		if primary {
			values.Set("ratio", "-1")
			if strings.TrimSpace(posterSource) != "" {
				values.Set("url", strings.TrimSpace(posterSource))
			}
			if cfg.CropCover {
				values.Set("pos", "1")
				values.Set("auto", "true")
			} else {
				values.Set("pos", "-1")
				values.Set("auto", "false")
			}
		}
		return base + "?" + values.Encode()
	}

	return imageURL("primary", true), imageURL("backdrop", false)
}

func parseYearFromDate(dateStr string) int {
	dateStr = strings.TrimSpace(dateStr)
	if len(dateStr) >= 4 {
		if y, err := strconv.Atoi(dateStr[:4]); err == nil && y > 1900 && y < 2100 {
			return y
		}
	}
	return 0
}

func cleanDateString(dateStr string) string {
	dateStr = strings.TrimSpace(dateStr)
	if len(dateStr) >= 10 {
		return dateStr[:10]
	}
	return dateStr
}
