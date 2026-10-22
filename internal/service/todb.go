// Package service — TODb (TheOtherDB) segment client.
//
// TODb 的播放资料库按「片源」组织：同一个 TMDB 条目可能有爱奇艺、腾讯等多个
// 版本的 marker，每个版本自带 version_runtime（该片源的时长）。因此它的响应是
// 一个 items 数组，而不是单一区间集合。
//
// 接口是 POST + JSON body（GET 会返回 405），平台用 platform_type 表达：
//
//	tmdb_id_tv     + season_number / episode_number
//	tmdb_id_movie  无季集
//
// marker 的时间轴是**秒**（time_start / time_end），不是毫秒。
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// TODbBaseURL 是公开只读端点。
const TODbBaseURL = "https://playback.theotherdb.org/api/metadata"

// TODbSource 写入 media_segments.source。
const TODbSource = "todb"

const (
	todbTimeout     = 10 * time.Second
	todbMaxBodySize = 1 << 20
	// todbPageSize 一次取多少条版本记录。社区库大多数条目只有一个片源，
	// 但同一部剧的各平台版本可能都有人标，取多一点可以避免漏掉匹配时长的那个。
	todbPageSize = 10
)

// todbRequest 是 POST /api/metadata 的请求体。
type todbRequest struct {
	PlatformType    string   `json:"platform_type"`
	PlatformValue   string   `json:"platform_value"`
	SeasonNumber    int      `json:"season_number,omitempty"`
	EpisodeNumber   int      `json:"episode_number,omitempty"`
	Includes        []string `json:"includes"`
	Page            int      `json:"page"`
	PageSize        int      `json:"page_size"`
}

// todbMarker 是单个标记点。time_start/time_end 单位是秒。
type todbMarker struct {
	MarkerType string   `json:"marker_type"`
	TimeStart  *float64 `json:"time_start"`
	TimeEnd    *float64 `json:"time_end"`
}

// todbItem 是一个片源版本及其 marker。
type todbItem struct {
	VersionID      int          `json:"version_id"`
	VersionName    string       `json:"version_name"`
	VersionRuntime *int         `json:"version_runtime"`
	VideoType      string       `json:"video_type"`
	Marker         []todbMarker `json:"marker"`
}

type todbResponse struct {
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
	HasMore  bool       `json:"has_more"`
	Items    []todbItem `json:"items"`
}

// TODbService 查询 TODb 的片段数据。
type TODbService struct {
	log        *zap.Logger
	client     *http.Client
	baseURL    string
	pageSize   int
}

// NewTODbService is the constructor.
func NewTODbService(log *zap.Logger) *TODbService {
	return &TODbService{
		log:      log,
		client:   NewExternalHTTPClient(todbTimeout),
		baseURL:  TODbBaseURL,
		pageSize: todbPageSize,
	}
}

// SetBaseURL overrides the API endpoint (tests, mirrors).
func (s *TODbService) SetBaseURL(base string) *TODbService {
	if s != nil && strings.TrimSpace(base) != "" {
		s.baseURL = strings.TrimSpace(base)
	}
	return s
}

// Source implements SegmentProvider.
func (s *TODbService) Source() string { return TODbSource }

// FetchSegments implements SegmentProvider.
//
// 返回时会把多个版本的 marker 合并：同 kind 取第一个出现的区间。TODb 的片源
// 时长各不相同，严格按 version_runtime 匹配当前媒体需要可靠的总时长，而片段
// 查询经常跑在时长还不知道的时候（STRM 媒体），所以这里不做版本筛选——宁可
// 给一个近似位置，也好过整段缺失。真正需要精度的场景由 manual 标记覆盖。
func (s *TODbService) FetchSegments(ctx context.Context, q SegmentQuery) ([]SegmentSpan, error) {
	if s == nil || s.client == nil || q.TMDbID <= 0 {
		return nil, nil
	}
	payload := todbRequest{
		PlatformValue: fmt.Sprint(q.TMDbID),
		Includes:      []string{"marker"},
		Page:          1,
		PageSize:      s.pageSize,
	}
	if q.IsMovie {
		payload.PlatformType = "tmdb_id_movie"
	} else {
		payload.PlatformType = "tmdb_id_tv"
		payload.SeasonNumber = q.Season
		payload.EpisodeNumber = q.Episode
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("todb: unexpected status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, todbMaxBodySize))
	if err != nil {
		return nil, err
	}
	return parseTODbResponse(raw)
}

// parseTODbResponse 把各片源的 marker 合并成 spans。
//
// marker_type 的映射沿用报告里的规则：intro -> intro，credits/post_credits ->
// credits（片尾），其余（chapter 等章节标记）忽略——章节标题没有可靠的语义，
// 当成片头会让跳过点漂到任意位置。
func parseTODbResponse(body []byte) ([]SegmentSpan, error) {
	var raw todbResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse todb json: %w", err)
	}
	seen := make(map[string]bool, 4)
	spans := make([]SegmentSpan, 0, 4)
	for _, item := range raw.Items {
		for _, marker := range item.Marker {
			kind := todbMarkerKind(marker.MarkerType)
			if kind == "" || seen[kind] {
				continue
			}
			if marker.TimeStart == nil || marker.TimeEnd == nil {
				continue
			}
			startMs := int64(*marker.TimeStart * 1000)
			endMs := int64(*marker.TimeEnd * 1000)
			if startMs < 0 {
				startMs = 0
			}
			// 结束点缺失或非正表示「到片尾」，只有这样才是合法的开放区间；
			// 与 TheIntroDB 不同，TODb 用 0 而不是 null 表示没标结束点，
			// 所以这里把 <= start 的片尾当作开放区间。
			if endMs <= startMs {
				if kind != "credits" && kind != "preview" {
					continue
				}
				endMs = 0
			}
			spans = append(spans, SegmentSpan{Kind: kind, StartMs: startMs, EndMs: endMs})
			seen[kind] = true
		}
	}
	return spans, nil
}

// todbMarkerKind 把 TODb 的 marker_type 映射成库内 kind。
func todbMarkerKind(markerType string) string {
	switch strings.ToLower(strings.TrimSpace(markerType)) {
	case "intro":
		return "intro"
	case "recap":
		return "recap"
	case "credits", "post_credits", "outro":
		return "credits"
	case "preview":
		return "preview"
	default:
		return ""
	}
}
