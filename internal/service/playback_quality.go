package service

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/cloud115"
)

// PlaybackQuality 是返回给播放器的统一画质选项。
//
// Source:
//   - cloud:    115 云端转码清晰度
//   - original: 115 原文件（直连，可能仍是 MKV/HEVC/PGS）
//   - local:    MeBox 本地 HLS 重新编码清晰度
type PlaybackQuality struct {
	ID                string `json:"id"`
	Label             string `json:"label"`
	Height            int    `json:"height,omitempty"`
	Source            string `json:"source"`
	Available         bool   `json:"available"`
	RequiresTranscode bool   `json:"requires_transcode,omitempty"`
	RequiresVIP       bool   `json:"requires_vip,omitempty"`
	Note              string `json:"note,omitempty"`
}

type cloud115QualitySpec struct {
	ID     string
	Label  string
	Height int
	VIP    bool
}

var cloud115QualityLadder = []cloud115QualitySpec{
	{ID: "1", Label: "标清", Height: 480},
	{ID: "2", Label: "高清", Height: 540},
	{ID: "3", Label: "超清", Height: 720},
	{ID: "4", Label: "1080P", Height: 1080},
	{ID: "5", Label: "4K", Height: 2160, VIP: true},
}

var resolutionTokenRE = regexp.MustCompile(`(?i)(\d{3,4})p`)

func mediaSourceHeight(m *model.Media) int {
	if m == nil {
		return 0
	}
	if m.Height > 0 {
		return m.Height
	}
	for _, text := range []string{m.OriginalName, m.Path, m.STRMURL} {
		if h := parseHeightToken(text); h > 0 {
			return h
		}
	}
	return 0
}

func parseHeightToken(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	upper := strings.ToUpper(text)
	if strings.Contains(upper, "4K") || strings.Contains(upper, "2160P") {
		return 2160
	}
	for _, match := range resolutionTokenRE.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		height, err := strconv.Atoi(match[1])
		if err != nil || height < 240 || height > 4320 {
			continue
		}
		return height
	}
	return 0
}

func cloud115AvailableDefinitions(data *cloud115.VideoPlayData) map[string]bool {
	available := map[string]bool{}
	if data == nil {
		return available
	}
	for key := range data.DefinitionListNew {
		key = strings.TrimSpace(key)
		if key != "" {
			available[key] = true
		}
	}
	for key := range data.DefinitionList {
		key = strings.TrimSpace(key)
		if key != "" {
			available[key] = true
		}
	}
	for _, item := range data.VideoURL {
		id := item.DefinitionN
		if id <= 0 {
			id = item.Definition
		}
		if id > 0 {
			available[strconv.Itoa(id)] = true
		}
	}
	return available
}

func cloud115SourceHeight(m *model.Media, data *cloud115.VideoPlayData) int {
	if height := mediaSourceHeight(m); height > 0 {
		return height
	}
	maxHeight := 0
	if data != nil {
		for _, item := range data.VideoURL {
			if item.Height > maxHeight {
				maxHeight = item.Height
			}
		}
	}
	if maxHeight > 0 {
		return maxHeight
	}
	return 1080
}

// Cloud115QualityOptions 返回 115 已转码和“按源分辨率可转码”的清晰度。
// 未出现在 definition_list_new/video_url 中的档位会标记为需要云端转码。
func Cloud115QualityOptions(m *model.Media, data *cloud115.VideoPlayData) []PlaybackQuality {
	sourceHeight := cloud115SourceHeight(m, data)
	available := cloud115AvailableDefinitions(data)
	out := make([]PlaybackQuality, 0, len(cloud115QualityLadder)+1)

	out = append(out, PlaybackQuality{
		ID:        "100",
		Label:     "原画",
		Height:    sourceHeight,
		Source:    "original",
		Available: true,
		Note:      "播放原始文件，可能仍是 MKV/HEVC/PGS",
	})

	for _, spec := range cloud115QualityLadder {
		if sourceHeight > 0 && spec.Height > sourceHeight && !available[spec.ID] {
			continue
		}
		label := spec.Label
		if data != nil && strings.TrimSpace(data.DefinitionListNew[spec.ID]) != "" {
			label = strings.TrimSpace(data.DefinitionListNew[spec.ID])
		}
		isAvailable := available[spec.ID]
		item := PlaybackQuality{
			ID:                spec.ID,
			Label:             label,
			Height:            spec.Height,
			Source:            "cloud",
			Available:         isAvailable,
			RequiresTranscode: !isAvailable,
			RequiresVIP:       spec.VIP,
		}
		if !isAvailable {
			item.Note = "需要 115 云端转码"
		}
		if spec.VIP {
			if item.Note != "" {
				item.Note += "，"
			}
			item.Note += "需要年费 VIP"
		}
		out = append(out, item)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source == "original"
		}
		if out[i].Height != out[j].Height {
			return out[i].Height > out[j].Height
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// DefaultCloud115Quality 默认优先 1080P；没有 1080P 时退回最高的云端档位。
func DefaultCloud115Quality(options []PlaybackQuality) string {
	for _, option := range options {
		if option.ID == "4" && option.Source == "cloud" {
			return option.ID
		}
	}
	best := ""
	bestHeight := -1
	for _, option := range options {
		if option.Source != "cloud" {
			continue
		}
		if option.Height > bestHeight {
			best = option.ID
			bestHeight = option.Height
		}
	}
	if best != "" {
		return best
	}
	return "4"
}

// LocalQualityOptions 返回 MeBox 本地 HLS 可用的画质档位。
//
// transcodeAvailable 为 false（未装 ffmpeg、或转码被全局关闭）时档位仍然返回，
// 但 Available 为 false：播放器据此直接走直连播放，而不是先请求 /api/hls 拿到
// 一个必然失败的 500 再回退。档位列表保留是为了让前端仍能展示"需要转码"的说明。
func LocalQualityOptions(m *model.Media, transcodeAvailable bool) []PlaybackQuality {
	sourceHeight := mediaSourceHeight(m)
	presets := []PlaybackQuality{
		{ID: "source", Label: "原画", Height: sourceHeight, Source: "local", Available: transcodeAvailable, Note: "本地 HLS，保持源分辨率"},
		{ID: "1080", Label: "1080P", Height: 1080, Source: "local", Available: transcodeAvailable},
		{ID: "720", Label: "720P", Height: 720, Source: "local", Available: transcodeAvailable},
		{ID: "480", Label: "480P", Height: 480, Source: "local", Available: transcodeAvailable},
	}
	if !transcodeAvailable {
		for i := range presets {
			presets[i].RequiresTranscode = true
			if presets[i].Note == "" {
				presets[i].Note = "需要 ffmpeg，当前不可用"
			}
		}
	}
	out := make([]PlaybackQuality, 0, len(presets))
	for _, preset := range presets {
		if preset.ID != "source" && sourceHeight > 0 && preset.Height > sourceHeight {
			continue
		}
		out = append(out, preset)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ID == "source" {
			return true
		}
		if out[j].ID == "source" {
			return false
		}
		return out[i].Height > out[j].Height
	})
	return out
}

// LocalHLSQuality 是传给 ffmpeg 的本地 HLS 画质参数。
type LocalHLSQuality struct {
	ID           string
	Label        string
	Height       int
	VideoBitrate string
	MaxRate      string
	BufSize      string
}

var localHLSQualities = map[string]LocalHLSQuality{
	"source": {ID: "source", Label: "原画", Height: 0, VideoBitrate: "8000k", MaxRate: "10000k", BufSize: "12000k"},
	"1080":   {ID: "1080", Label: "1080P", Height: 1080, VideoBitrate: "4000k", MaxRate: "4500k", BufSize: "6000k"},
	"720":    {ID: "720", Label: "720P", Height: 720, VideoBitrate: "2500k", MaxRate: "2800k", BufSize: "4000k"},
	"480":    {ID: "480", Label: "480P", Height: 480, VideoBitrate: "1200k", MaxRate: "1400k", BufSize: "2000k"},
}

func LocalHLSQualityByID(id string) (LocalHLSQuality, bool) {
	quality, ok := localHLSQualities[strings.TrimSpace(id)]
	return quality, ok
}

func DefaultLocalHLSQualityID(m *model.Media) string {
	sourceHeight := mediaSourceHeight(m)
	switch {
	case sourceHeight >= 1080:
		return "1080"
	case sourceHeight >= 720:
		return "720"
	case sourceHeight >= 480:
		return "480"
	case sourceHeight > 0:
		return "source"
	default:
		return "720"
	}
}
