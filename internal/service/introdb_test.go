package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// 这两段响应是从 api.theintrodb.org/v3/media 实测抓下来的原文，
// 用来锁住 null 语义：start_ms: null = 从片头开始，end_ms: null = 一直到片尾。
const (
	introDBTVPayload    = `{"tmdb_id":1396,"type":"tv","season":1,"episode":1,"intro":[{"start_ms":228664,"end_ms":246143}],"credits":[{"start_ms":3431000,"end_ms":null}]}`
	introDBMoviePayload = `{"tmdb_id":27205,"type":"movie","intro":[{"start_ms":null,"end_ms":38000}]}`
)

func TestParseIntroDBResponseResolvesNullBounds(t *testing.T) {
	spans, err := parseIntroDBResponse([]byte(introDBTVPayload))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2 (%#v)", len(spans), spans)
	}
	if spans[0].Kind != "intro" || spans[0].StartMs != 228_664 || spans[0].EndMs != 246_143 {
		t.Fatalf("intro span = %#v", spans[0])
	}
	// end_ms: null 表示一直到片尾，落成 0 由客户端结合时长补齐。
	if spans[1].Kind != "credits" || spans[1].StartMs != 3_431_000 || spans[1].EndMs != 0 {
		t.Fatalf("credits span = %#v", spans[1])
	}

	movie, err := parseIntroDBResponse([]byte(introDBMoviePayload))
	if err != nil {
		t.Fatalf("parse movie: %v", err)
	}
	if len(movie) != 1 {
		t.Fatalf("movie spans = %d, want 1", len(movie))
	}
	// start_ms: null = 从片头开始。
	if movie[0].StartMs != 0 || movie[0].EndMs != 38_000 {
		t.Fatalf("movie intro span = %#v", movie[0])
	}
}

func TestParseIntroDBResponseDropsEmptyRanges(t *testing.T) {
	body := `{"tmdb_id":1,"type":"movie",
		"intro":[{"start_ms":5000,"end_ms":5000},{"start_ms":9000,"end_ms":8000},{"start_ms":1000,"end_ms":2000}],
		"recap":[],"credits":[],"preview":[]}`
	spans, err := parseIntroDBResponse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 只有 end > start 的区间是可跳过的；end == 0（到片尾）是合法值，此处不涉及。
	if len(spans) != 1 || spans[0].StartMs != 1_000 || spans[0].EndMs != 2_000 {
		t.Fatalf("spans = %#v, want only the 1000-2000 range", spans)
	}
}

func TestParseIntroDBResponseOrdersByType(t *testing.T) {
	body := `{"tmdb_id":1,"type":"tv","credits":[{"start_ms":900,"end_ms":1000}],
		"intro":[{"start_ms":100,"end_ms":200}],"recap":[{"start_ms":50,"end_ms":60}]}`
	spans, err := parseIntroDBResponse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"intro", "recap", "credits"}
	if len(spans) != len(want) {
		t.Fatalf("spans = %#v, want %d", spans, len(want))
	}
	for i, kind := range want {
		if spans[i].Kind != kind {
			t.Fatalf("span[%d].kind = %q, want %q", i, spans[i].Kind, kind)
		}
	}
}

func TestIntroDBMediaURLOnlyAddsSeasonEpisodeForTV(t *testing.T) {
	svc := NewIntroDBService(zap.NewNop())
	if got, want := svc.mediaURL(1396, 1, 1),
		"https://api.theintrodb.org/v3/media?tmdb_id=1396&season=1&episode=1"; got != want {
		t.Fatalf("tv url = %q, want %q", got, want)
	}
	// 电影（season/episode 为 0）不能带季集参数，否则会被当成剧集查不到。
	if got, want := svc.mediaURL(27205, 0, 0),
		"https://api.theintrodb.org/v3/media?tmdb_id=27205"; got != want {
		t.Fatalf("movie url = %q, want %q", got, want)
	}
}

func TestIntroDBFetchTreatsNotFoundAsNoData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	svc := NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL)
	spans, err := svc.Fetch(t.Context(), 999_999, 1, 1)
	if err != nil {
		t.Fatalf("404 must not be an error, got %v", err)
	}
	if len(spans) != 0 {
		t.Fatalf("spans = %#v, want none", spans)
	}
}

func TestIntroDBFetchReportsUnexpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	svc := NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL)
	if _, err := svc.Fetch(t.Context(), 1, 0, 0); err == nil {
		t.Fatal("500 should surface as an error so the caller can keep its cache")
	}
}

func TestIntroDBFetchSkipsRequestWithoutTMDbID(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(introDBMoviePayload))
	}))
	defer server.Close()

	svc := NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL)
	spans, err := svc.Fetch(t.Context(), 0, 0, 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(spans) != 0 || calls != 0 {
		t.Fatalf("spans = %#v calls = %d, want no request without a tmdb id", spans, calls)
	}
}

func TestIntroDBFetchParsesBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("tmdb_id"); got != "1396" {
			t.Errorf("tmdb_id = %q, want 1396", got)
		}
		if got := r.URL.Query().Get("season"); got != "1" {
			t.Errorf("season = %q, want 1", got)
		}
		if got := r.URL.Query().Get("episode"); got != "1" {
			t.Errorf("episode = %q, want 1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(introDBTVPayload))
	}))
	defer server.Close()

	svc := NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL)
	spans, err := svc.Fetch(t.Context(), 1396, 1, 1)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(spans) != 2 || spans[0].Kind != "intro" {
		t.Fatalf("spans = %#v", spans)
	}
}

// 实测：从生产机连发 45 个请求有 15 个被返回 429，加间隔重试后其中 10 个成功。
// 所以限流值得一次重试，否则那部分播放会静默少掉「跳过片头」按钮。
func TestIntroDBFetchRetriesRateLimit(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(introDBTVPayload))
	}))
	defer server.Close()

	svc := NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL).SetRetryDelay(0)
	spans, err := svc.Fetch(t.Context(), 1396, 1, 1)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("spans = %#v, want the retried response", spans)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2 (the original plus one retry)", got)
	}
}

func TestIntroDBFetchGivesUpAfterRetryBudget(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	svc := NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL).SetRetryDelay(0)
	if _, err := svc.Fetch(t.Context(), 1396, 1, 1); err == nil {
		t.Fatal("a persistent 429 must surface as an error so the caller keeps its cache")
	}
	// 只重试一次：限流通常不是靠密集重试解决的，而调用方的等待预算有限。
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2 (one retry, then give up)", got)
	}
}

func TestIntroDBFetchDoesNotRetryNotFound(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	svc := NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL).SetRetryDelay(0)
	spans, err := svc.Fetch(t.Context(), 424242, 0, 0)
	if err != nil || len(spans) != 0 {
		t.Fatalf("spans = %#v err = %v, want a cached miss", spans, err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1: 404 means \"no data\", it is not worth retrying", got)
	}
}

// 调用方预算不够时不能为了重试干等：Emby 只给 5 秒，等下去会把
// 「少一个跳过按钮」升级成「请求超时」。
func TestIntroDBFetchSkipsRetryWhenCallerBudgetIsSpent(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	svc := NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL).SetRetryDelay(time.Minute)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	if _, err := svc.Fetch(ctx, 1396, 1, 1); err == nil {
		t.Fatal("want an error when the budget is spent")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Fetch waited %s, want it to give up promptly", elapsed)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1 without a retry", got)
	}
}

func TestIntroDBRetryWaitPrefersRetryAfterHeader(t *testing.T) {
	svc := NewIntroDBService(zap.NewNop())
	if got := svc.retryWait(2 * time.Second); got != 2*time.Second {
		t.Fatalf("retryWait(2s) = %s, want the server's value", got)
	}
	if got := svc.retryWait(0); got != introDBRetryDelay {
		t.Fatalf("retryWait(0) = %s, want the default %s", got, introDBRetryDelay)
	}
	// 服务端可以要求等很久，但一次播放不值得为它挂住几十秒。
	if got := svc.retryWait(10 * time.Minute); got != introDBMaxRetryDelay {
		t.Fatalf("retryWait(10m) = %s, want it capped at %s", got, introDBMaxRetryDelay)
	}
}

func TestParseIntroDBRetryAfter(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
	}{
		{"2", 2 * time.Second},
		{" 3 ", 3 * time.Second},
		{"", 0},
		{"abc", 0},
		{"-5", 0},
		{"0", 0},
	}
	for _, tc := range cases {
		if got := parseIntroDBRetryAfter(tc.value); got != tc.want {
			t.Fatalf("parseIntroDBRetryAfter(%q) = %s, want %s", tc.value, got, tc.want)
		}
	}
}

func TestIntroDBRetryableStatus(t *testing.T) {
	retryable := []int{http.StatusTooManyRequests, http.StatusServiceUnavailable}
	for _, status := range retryable {
		if !introDBRetryableStatus(status) {
			t.Fatalf("status %d should be retryable", status)
		}
	}
	// 404 由 fetchOnce 单独处理；500 之类的服务端故障重试也不会变好，
	// 却会白占调用方的等待预算。
	notRetryable := []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusBadRequest}
	for _, status := range notRetryable {
		if introDBRetryableStatus(status) {
			t.Fatalf("status %d should not be retryable", status)
		}
	}
}
