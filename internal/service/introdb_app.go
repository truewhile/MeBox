// Package service — IntroDB (https://introdb.app) segment client.
//
// 与 TheIntroDB 是两家不同的社区库：IntroDB 只接受 IMDb id 定位媒体，
// 用 is_movie 区分电影与剧集，响应把每种区间直接给成 {start_ms,end_ms} 对象
// （而不是按类型分数组），片尾字段名是 outro 而不是 credits，
// 另外还有 post_credits（片尾彩蛋，映射为预告/额外片尾）。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// IntroDBAppBaseURL 是公开只读端点。POST /submit 需要 API key，这里不做写入。
const IntroDBAppBaseURL = "https://api.introdb.app"

// IntroDBAppSource 写入 media_segments.source，与 TheIntroDB 的 "theintrodb" 区分。
const IntroDBAppSource = "introdb"

const (
	introDBAppTimeout     = 8 * time.Second
	introDBAppMaxBodySize = 1 << 20
)

// introDBAppSegment 是 IntroDB 的单个区间对象。null 表示该类型没有数据。
type introDBAppSegment struct {
	StartMs *int64 `json:"start_ms"`
	EndMs   *int64 `json:"end_ms"`
}

// introDBAppResponse 对应 GET /segments。
type introDBAppResponse struct {
	IMDbID      string             `json:"imdb_id"`
	MediaType   string             `json:"media_type"`
	IsMovie     bool               `json:"is_movie"`
	Season      int                `json:"season"`
	Episode     int                `json:"episode"`
	Intro       *introDBAppSegment `json:"intro"`
	Recap       *introDBAppSegment `json:"recap"`
	Outro       *introDBAppSegment `json:"outro"`
	PostCredits *introDBAppSegment `json:"post_credits"`
}

// IntroDBAppService 查询 IntroDB 的片段数据。
type IntroDBAppService struct {
	log     *zap.Logger
	client  *http.Client
	baseURL string
}

// NewIntroDBAppService is the constructor. The client honours environment and
// OS proxy settings so it behaves like the other third-party API clients.
func NewIntroDBAppService(log *zap.Logger) *IntroDBAppService {
	return &IntroDBAppService{
		log:     log,
		client:  NewExternalHTTPClient(introDBAppTimeout),
		baseURL: IntroDBAppBaseURL,
	}
}

// SetBaseURL overrides the API root (tests, mirrors).
func (s *IntroDBAppService) SetBaseURL(base string) *IntroDBAppService {
	if s != nil && strings.TrimSpace(base) != "" {
		s.baseURL = strings.TrimRight(strings.TrimSpace(base), "/")
	}
	return s
}

// Source implements SegmentProvider.
func (s *IntroDBAppService) Source() string { return IntroDBAppSource }

// FetchSegments implements SegmentProvider.
//
// 没有 IMDb id 时直接返回空：IntroDB 不接受 TMDb id，用别的 id 拼请求只会拿到
// 400，那会被误记成「查询失败」从而保留旧缓存、永远不再刷新。
func (s *IntroDBAppService) FetchSegments(ctx context.Context, q SegmentQuery) ([]SegmentSpan, error) {
	if s == nil || s.client == nil || strings.TrimSpace(q.IMDbID) == "" {
		return nil, nil
	}
	endpoint := s.segmentsURL(q)
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

	// 404 = 库里没有这部片，是正常的「无数据」，不是错误。
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("introdb: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, introDBAppMaxBodySize))
	if err != nil {
		return nil, err
	}
	return parseIntroDBAppResponse(body)
}

func (s *IntroDBAppService) segmentsURL(q SegmentQuery) string {
	values := url.Values{}
	values.Set("imdb_id", strings.TrimSpace(q.IMDbID))
	if q.IsMovie {
		values.Set("is_movie", "true")
	} else if q.Season > 0 && q.Episode > 0 {
		values.Set("season", strconv.Itoa(q.Season))
		values.Set("episode", strconv.Itoa(q.Episode))
	}
	return s.baseURL + "/segments?" + values.Encode()
}

// parseIntroDBAppResponse 把 IntroDB 的四个区间对象摊平成 spans。
//
// 顺序固定为 intro -> recap -> outro -> post_credits，与 TheIntroDB 的
// intro/recap/credits/preview 对齐，后处理逻辑因此可以对来源无感。
func parseIntroDBAppResponse(body []byte) ([]SegmentSpan, error) {
	var raw introDBAppResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse introdb json: %w", err)
	}
	groups := []struct {
		kind string
		seg  *introDBAppSegment
	}{
		{"intro", raw.Intro},
		{"recap", raw.Recap},
		{"credits", raw.Outro},
		{"preview", raw.PostCredits},
	}
	spans := make([]SegmentSpan, 0, len(groups))
	for _, group := range groups {
		if group.seg == nil || group.seg.StartMs == nil || group.seg.EndMs == nil {
			continue
		}
		start, end := *group.seg.StartMs, *group.seg.EndMs
		if start < 0 {
			start = 0
		}
		if end <= start {
			continue
		}
		spans = append(spans, SegmentSpan{Kind: group.kind, StartMs: start, EndMs: end})
	}
	return spans, nil
}
