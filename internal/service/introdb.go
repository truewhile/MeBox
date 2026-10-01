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
	// introDBMaxAttempts 是一次 Fetch 允许的请求次数（原请求 + 1 次重试）。
	// 实测：短时间连发 45 个请求有 15 个被返回 429，加 1.2 秒间隔重试后
	// 其中 10 个成功，所以限流是真实存在的、值得一次重试。
	introDBMaxAttempts = 2
	// introDBRetryDelay 是服务端没给 Retry-After 时的默认重试间隔。
	introDBRetryDelay = time.Second
	// introDBMaxRetryDelay 限制服务端要求的等待时间：一次播放不值得为它
	// 挂住几十秒，等待超过这个值就按这个值等（然后可能再次被限流）。
	introDBMaxRetryDelay = 3 * time.Second
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
	log        *zap.Logger
	client     *http.Client
	baseURL    string
	retryDelay time.Duration
}

// NewIntroDBService is the constructor. The client honours environment and OS
// proxy settings so it behaves like the other third-party API clients.
func NewIntroDBService(log *zap.Logger) *IntroDBService {
	return &IntroDBService{
		log:        log,
		client:     NewExternalHTTPClient(introDBTimeout),
		baseURL:    IntroDBBaseURL,
		retryDelay: introDBRetryDelay,
	}
}

// SetBaseURL overrides the API root (tests, mirrors).
func (s *IntroDBService) SetBaseURL(base string) *IntroDBService {
	if s != nil && strings.TrimSpace(base) != "" {
		s.baseURL = strings.TrimRight(strings.TrimSpace(base), "/")
	}
	return s
}

// SetRetryDelay overrides the wait between attempts. Tests set it to 0 so a
// retry does not really sleep.
func (s *IntroDBService) SetRetryDelay(delay time.Duration) *IntroDBService {
	if s != nil {
		s.retryDelay = delay
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
//
// 429/503 会重试一次（社区库在短时间连发下确实会限流）。重试前会先确认调用方
// 的 deadline 还够用；预算不够就直接返回错误，让调用方保留自己的缓存，
// 把「拿不到片段」维持在「少一个跳过按钮」的量级。
func (s *IntroDBService) Fetch(ctx context.Context, tmdbID, season, episode int) ([]IntroDBSpan, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("introdb service nil")
	}
	if tmdbID <= 0 {
		return nil, nil
	}
	endpoint := s.mediaURL(tmdbID, season, episode)
	for attempt := 1; ; attempt++ {
		result := s.fetchOnce(ctx, endpoint)
		if result.err == nil {
			return result.spans, nil
		}
		if !result.retryable || attempt >= introDBMaxAttempts {
			return nil, result.err
		}
		if !waitForIntroDBRetry(ctx, s.retryWait(result.retryAfter)) {
			return nil, result.err
		}
	}
}

// introDBFetchAttempt 是一次请求的结果：数据或错误，外加「值不值得重试」。
type introDBFetchAttempt struct {
	spans      []IntroDBSpan
	err        error
	retryable  bool
	retryAfter time.Duration
}

func (s *IntroDBService) fetchOnce(ctx context.Context, endpoint string) introDBFetchAttempt {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return introDBFetchAttempt{err: err}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return introDBFetchAttempt{err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// 「查到但社区库里没有」不是错误，调用方据此写负缓存。
		return introDBFetchAttempt{}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return introDBFetchAttempt{
			err:        fmt.Errorf("introdb: unexpected status %d", resp.StatusCode),
			retryable:  introDBRetryableStatus(resp.StatusCode),
			retryAfter: parseIntroDBRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, introDBMaxBodySize))
	if err != nil {
		return introDBFetchAttempt{err: err}
	}
	spans, err := parseIntroDBResponse(body)
	if err != nil {
		return introDBFetchAttempt{err: err}
	}
	return introDBFetchAttempt{spans: spans}
}

// introDBRetryableStatus 只认明确的「稍后再来」状态。500 之类的服务端故障
// 重试也不会变好，却会白占调用方的等待预算。
func introDBRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

// parseIntroDBRetryAfter 解析 Retry-After 的秒数形式；HTTP-date 形式在限流
// 场景很少见，解析不出来就退回默认间隔。
func parseIntroDBRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (s *IntroDBService) retryWait(retryAfter time.Duration) time.Duration {
	wait := retryAfter
	if wait <= 0 {
		wait = s.retryDelay
	}
	if wait > introDBMaxRetryDelay {
		wait = introDBMaxRetryDelay
	}
	return wait
}

// waitForIntroDBRetry 睡到重试时刻，或调用方的 ctx 先结束。返回 false 表示
// 预算已经用完，调用方不该再等。
func waitForIntroDBRetry(ctx context.Context, wait time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if wait <= 0 {
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
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
