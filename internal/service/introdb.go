// Package service — TheIntroDB client.
//
// TheIntroDB (https://theintrodb.org) is a community database of "skip"
// timestamps: intro, recap, end credits and previews. Reads are public and
// need no API key, which is what makes it usable as an automatic filler for
// the player's 跳过片头/片尾 feature.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	// IntroDBBaseURL is the public read endpoint. Overridable on the service
	// for tests and for pointing at a mirror.
	IntroDBBaseURL = "https://api.theintrodb.org/v3"
	// IntroDBSource tags rows that came from this provider.
	IntroDBSource = "theintrodb"

	introDBTimeout     = 8 * time.Second
	introDBMaxBodySize = 1 << 20
)

// IntroDBSpan is one resolved skip range, still in provider terms.
// EndMs == 0 means "runs to the end of the media" (TheIntroDB returns
// end_ms: null for end credits); the caller resolves it against the duration.
type IntroDBSpan struct {
	Kind    string
	StartMs int64
	EndMs   int64
}

// IntroDBService queries TheIntroDB for one media item.
type IntroDBService struct {
	log     *zap.Logger
	client  *http.Client
	baseURL string
}

// NewIntroDBService is the constructor. The client honours environment and OS
// proxy settings so it behaves like the other third-party API clients.
func NewIntroDBService(log *zap.Logger) *IntroDBService {
	return &IntroDBService{
		log:     log,
		client:  NewExternalHTTPClient(introDBTimeout),
		baseURL: IntroDBBaseURL,
	}
}

// SetBaseURL overrides the API root (tests, mirrors).
func (s *IntroDBService) SetBaseURL(base string) *IntroDBService {
	if s != nil && strings.TrimSpace(base) != "" {
		s.baseURL = strings.TrimRight(strings.TrimSpace(base), "/")
	}
	return s
}

// introDBRange mirrors one entry of a segment array. start_ms/end_ms are
// pointers because the API distinguishes null (= open-ended) from 0.
type introDBRange struct {
	StartMs *int64 `json:"start_ms"`
	EndMs   *int64 `json:"end_ms"`
}

type introDBResponse struct {
	TMDbID  int            `json:"tmdb_id"`
	Type    string         `json:"type"`
	Intro   []introDBRange `json:"intro"`
	Recap   []introDBRange `json:"recap"`
	Credits []introDBRange `json:"credits"`
	Preview []introDBRange `json:"preview"`
}

// Fetch returns the skip ranges TheIntroDB knows about. A 404 means the
// database simply has nothing for this title, which is not an error: the
// caller records it as a negative cache entry.
//
// season/episode are required for TV; pass 0/0 for movies.
func (s *IntroDBService) Fetch(ctx context.Context, tmdbID, season, episode int) ([]IntroDBSpan, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("introdb service nil")
	}
	if tmdbID <= 0 {
		return nil, nil
	}
	endpoint := s.mediaURL(tmdbID, season, episode)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, nil
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("introdb: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, introDBMaxBodySize))
	if err != nil {
		return nil, err
	}
	return parseIntroDBResponse(body)
}

func (s *IntroDBService) mediaURL(tmdbID, season, episode int) string {
	var b strings.Builder
	b.WriteString(s.baseURL)
	b.WriteString("/media?tmdb_id=")
	b.WriteString(strconv.Itoa(tmdbID))
	// TheIntroDB 对剧集必须带 season+episode，只给 tmdb_id 会返回 404。
	if season > 0 && episode > 0 {
		b.WriteString("&season=")
		b.WriteString(strconv.Itoa(season))
		b.WriteString("&episode=")
		b.WriteString(strconv.Itoa(episode))
	}
	return b.String()
}

// parseIntroDBResponse flattens the per-type arrays into spans, preserving the
// intro -> recap -> credits -> preview order so the player sees the earliest
// range first.
func parseIntroDBResponse(body []byte) ([]IntroDBSpan, error) {
	var raw introDBResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse introdb json: %w", err)
	}
	groups := []struct {
		kind   string
		ranges []introDBRange
	}{
		{"intro", raw.Intro},
		{"recap", raw.Recap},
		{"credits", raw.Credits},
		{"preview", raw.Preview},
	}
	spans := make([]IntroDBSpan, 0, len(raw.Intro)+len(raw.Credits))
	for _, group := range groups {
		for _, r := range group.ranges {
			var start int64
			if r.StartMs != nil {
				start = *r.StartMs
			}
			var end int64
			if r.EndMs != nil {
				end = *r.EndMs
			}
			if start < 0 {
				start = 0
			}
			// end == 0 表示「延续到片尾」，是合法值；其余情况 end 必须大于 start，
			// 否则这段区间没有任何可跳过的内容，直接丢弃避免在播放器里出现空按钮。
			if end != 0 && end <= start {
				continue
			}
			spans = append(spans, IntroDBSpan{Kind: group.kind, StartMs: start, EndMs: end})
		}
	}
	return spans, nil
}

// logIntroDBFailure 只在 debug 级别记录，避免社区库不可达时把日志刷满。
func logIntroDBFailure(log *zap.Logger, tmdbID int, err error) {
	if log == nil || err == nil {
		return
	}
	log.Debug("introdb lookup failed", zap.Int("tmdb_id", tmdbID), zap.Error(err))
}
