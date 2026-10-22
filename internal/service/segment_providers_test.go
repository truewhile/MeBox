package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// 官方规范（theintrodb.org/openapi.yaml，SegmentTimestamp）对「没有数值」有两种
// 写法，含义完全不同：end_ms: null = 一直到片尾；end_ms: 0 = 这一段不存在。
// 实测约三分之一的剧集会返回 {"start_ms":null,"end_ms":0} 的 intro 占位。
const (
	// 真实返回：intro 是占位、credits 到片尾。
	introDBNoSegmentPayload = `{"tvdb_id":73762,"type":"tv","season":1,"episode":1,"intro":[{"start_ms":null,"end_ms":0}],"credits":[{"start_ms":2574000,"end_ms":null}]}`
	// 整集都是占位：recap/credits 都是 (0,0)。
	introDBAllPlaceholderPayload = `{"tvdb_id":375921,"type":"tv","season":1,"episode":1,"recap":[{"start_ms":0,"end_ms":0}],"credits":[{"start_ms":0,"end_ms":0}]}`
)

// 把 end_ms: 0 当成「到片尾」会让 intro 变成 [0, 片长]，整集都是片头：
// 一开播就弹跳过按钮，开了自动跳过直接跳到片尾。
func TestParseIntroDBResponseDropsZeroEndPlaceholders(t *testing.T) {
	spans, err := parseIntroDBResponse([]byte(introDBNoSegmentPayload))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("spans = %#v, want only the credits range", spans)
	}
	if spans[0].Kind != "credits" || spans[0].StartMs != 2_574_000 || spans[0].EndMs != 0 {
		t.Fatalf("credits span = %#v, want start 2574000 and open end", spans[0])
	}
}

func TestParseIntroDBResponseDropsAllPlaceholderSegments(t *testing.T) {
	spans, err := parseIntroDBResponse([]byte(introDBAllPlaceholderPayload))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(spans) != 0 {
		t.Fatalf("spans = %#v, want none for all-placeholder payload", spans)
	}
}

// 片头/回顾不允许开放结束点：即使数据源给了 null，也不能补齐成「一直到片尾」。
func TestParseIntroDBResponseRejectsOpenEndedIntro(t *testing.T) {
	body := `{"tvdb_id":1,"type":"tv","season":1,"episode":1,"intro":[{"start_ms":1000,"end_ms":null}]}`
	spans, err := parseIntroDBResponse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(spans) != 0 {
		t.Fatalf("spans = %#v, want an open-ended intro to be dropped", spans)
	}
}

// IntroDB 的响应把区间给成对象而不是数组，且片尾字段叫 outro。
func TestParseIntroDBAppResponseMapsKinds(t *testing.T) {
	body := `{"imdb_id":"tt0903747","media_type":"tv","is_movie":false,"season":1,"episode":1,
		"intro":{"start_ms":437000,"end_ms":531000},
		"recap":null,
		"outro":{"start_ms":3431000,"end_ms":3500000},
		"post_credits":{"start_ms":3600000,"end_ms":3610000}}`
	spans, err := parseIntroDBAppResponse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []struct {
		kind     string
		start    int64
		end      int64
	}{
		{"intro", 437_000, 531_000},
		{"credits", 3_431_000, 3_500_000},
		{"preview", 3_600_000, 3_610_000},
	}
	if len(spans) != len(want) {
		t.Fatalf("spans = %#v, want %d", spans, len(want))
	}
	for i, w := range want {
		if spans[i].Kind != w.kind || spans[i].StartMs != w.start || spans[i].EndMs != w.end {
			t.Fatalf("span[%d] = %#v, want %s %d-%d", i, spans[i], w.kind, w.start, w.end)
		}
	}
}

// IntroDB 只认 IMDb id：没有 IMDb id 时不能退化成用 TMDb id 拼请求，
// 那只会在服务端拿到 400，被误记成「查询失败」而保留旧缓存。
func TestIntroDBAppFetchSkipsWithoutIMDbID(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	svc := NewIntroDBAppService(zap.NewNop()).SetBaseURL(server.URL)
	spans, err := svc.FetchSegments(t.Context(), SegmentQuery{TMDbID: 1396, Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(spans) != 0 || calls != 0 {
		t.Fatalf("spans = %#v calls = %d, want no request without an imdb id", spans, calls)
	}
}

func TestIntroDBAppFetchSendsIMDbAndSeasonEpisode(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"imdb_id":"tt0903747","media_type":"tv","is_movie":false,
			"intro":{"start_ms":1000,"end_ms":2000}}`))
	}))
	defer server.Close()

	svc := NewIntroDBAppService(zap.NewNop()).SetBaseURL(server.URL)
	if _, err := svc.FetchSegments(t.Context(), SegmentQuery{
		IMDbID: "tt0903747", Season: 2, Episode: 3,
	}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, want := range []string{"imdb_id=tt0903747", "season=2", "episode=3"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query = %q, want it to contain %q", gotQuery, want)
		}
	}
}

// 电影要用 is_movie，且不能带季集参数。
func TestIntroDBAppFetchUsesIsMovieForMovies(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"imdb_id":"tt1375666","is_movie":true}`))
	}))
	defer server.Close()

	svc := NewIntroDBAppService(zap.NewNop()).SetBaseURL(server.URL)
	if _, err := svc.FetchSegments(t.Context(), SegmentQuery{IMDbID: "tt1375666", IsMovie: true}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(gotQuery, "is_movie=true") {
		t.Fatalf("query = %q, want is_movie=true", gotQuery)
	}
	if strings.Contains(gotQuery, "season=") || strings.Contains(gotQuery, "episode=") {
		t.Fatalf("query = %q, want no season/episode for a movie", gotQuery)
	}
}
