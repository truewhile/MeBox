package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

// TODb 的接口是 POST + JSON body（GET 返回 405），平台类型用 tmdb_id_tv /
// tmdb_id_movie 表达，marker 的时间轴是秒。
func TestTODbFetchSendsPostWithTMDBPlatform(t *testing.T) {
	var body map[string]any
	var method string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"page":1,"page_size":10,"has_more":false,"items":[]}`))
	}))
	defer server.Close()

	svc := NewTODbService(zap.NewNop()).SetBaseURL(server.URL)
	if _, err := svc.FetchSegments(t.Context(), SegmentQuery{TMDbID: 226045, Season: 1, Episode: 1}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if method != http.MethodPost {
		t.Fatalf("method = %s, want POST", method)
	}
	if body["platform_type"] != "tmdb_id_tv" || body["platform_value"] != "226045" {
		t.Fatalf("body = %#v, want tmdb_id_tv/226045", body)
	}
	if body["season_number"] != float64(1) || body["episode_number"] != float64(1) {
		t.Fatalf("body = %#v, want season/episode 1", body)
	}
}

func TestTODbFetchUsesMoviePlatform(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()

	svc := NewTODbService(zap.NewNop()).SetBaseURL(server.URL)
	if _, err := svc.FetchSegments(t.Context(), SegmentQuery{TMDbID: 83533, IsMovie: true}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if body["platform_type"] != "tmdb_id_movie" {
		t.Fatalf("platform_type = %v, want tmdb_id_movie", body["platform_type"])
	}
	if _, ok := body["season_number"]; ok {
		t.Fatalf("body = %#v, want no season_number for a movie", body)
	}
}

// 实测返回：marker 的时间是秒，marker_type 为 intro。
func TestParseTODbResponseConvertsSecondsToMilliseconds(t *testing.T) {
	body := `{"page":1,"page_size":3,"has_more":false,"items":[
		{"version_id":1192,"version_name":"爱奇艺","version_runtime":1300,"video_type":"tv",
		 "marker":[{"marker_id":1588,"marker_type":"intro","time_start":0,"time_end":27}]}]}`
	spans, err := parseTODbResponse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("spans = %#v, want 1", spans)
	}
	if spans[0].Kind != "intro" || spans[0].StartMs != 0 || spans[0].EndMs != 27_000 {
		t.Fatalf("span = %#v, want intro 0-27000ms", spans[0])
	}
}

func TestParseTODbResponseMapsMarkerTypes(t *testing.T) {
	body := `{"items":[{"marker":[
		{"marker_type":"intro","time_start":10,"time_end":30},
		{"marker_type":"credits","time_start":100,"time_end":120},
		{"marker_type":"post_credits","time_start":130,"time_end":140},
		{"marker_type":"chapter","time_start":5,"time_end":6}
	]}]}`
	spans, err := parseTODbResponse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// chapter 被忽略；post_credits 与 credits 同为片尾，同 kind 只保留第一个。
	if len(spans) != 2 {
		t.Fatalf("spans = %#v, want intro + credits", spans)
	}
	if spans[0].Kind != "intro" || spans[1].Kind != "credits" {
		t.Fatalf("kinds = %s/%s, want intro/credits", spans[0].Kind, spans[1].Kind)
	}
}

// 片尾的结束点缺失或非正表示「到片尾」，是合法的开放区间；片头不行。
func TestParseTODbResponseAllowsOpenEndedCreditsOnly(t *testing.T) {
	body := `{"items":[{"marker":[
		{"marker_type":"intro","time_start":10,"time_end":10},
		{"marker_type":"credits","time_start":100,"time_end":100}
	]}]}`
	spans, err := parseTODbResponse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(spans) != 1 || spans[0].Kind != "credits" || spans[0].EndMs != 0 {
		t.Fatalf("spans = %#v, want only an open-ended credits", spans)
	}
}
