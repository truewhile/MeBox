package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/truewhile/MeBox/internal/model"
)

// 蓝光原盘（m2ts，40 Mbps 级别）在两核小机器上重编码只有 0.1～0.2 倍速，HLS
// 永远追不上播放，播放器等不到切片就报「转码失败」。当源视频本身就是浏览器
// 能解的 H.264 / 8bit / 4:2:0，且用户选的是「原画」档位（不改分辨率）时，
// 这里改走只换封装（-c:v copy）的直通路径：视频不重编码，音频转 AAC，
// CPU 占用接近于零，起播时间从几十秒降到几秒。
//
// 直通只对「原画」生效。1080P/720P/480P 档位意味着要改分辨率，必须重新编码；
// 那类请求仍然按原来的 libx264 路径处理（慢，但码率可控）。

// remuxEncoderLabel 是直通任务在日志与任务面板里的标签。
const remuxEncoderLabel = "remux"

// remuxEligible 判断这次请求能否复制视频流而不是重编码。
func (t *TranscoderService) remuxEligible(ctx context.Context, m *model.Media, quality *LocalHLSQuality, subtitleStream int) bool {
	if t == nil || t.repo == nil || m == nil {
		return false
	}
	// 烧录图形字幕要把字幕叠加进画面，必须解码+重编码。
	if subtitleStream >= 0 {
		return false
	}
	if !remuxAppliesToQuality(quality) {
		return false
	}
	probe, err := t.repo.MediaProbe.Get(ctx, m.ID)
	if err != nil || probe == nil {
		// 没有探测缓存就无法确认档次/像素格式，保守走重编码。
		return false
	}
	return remuxCopyableHeight(probe) > 0
}

// remuxAppliesToQuality 只放行「原画」档位（Height 为 0，即不缩放）。
// quality 为 nil 表示调用方没有指定档位（走全局 max_height），同样不直通，
// 否则会绕过配置的分辨率上限给出一个远超预期的码流。
func remuxAppliesToQuality(quality *LocalHLSQuality) bool {
	return quality != nil && quality.Height <= 0
}

// remuxCopyableHeight 在源视频可直接复制进 HLS 时返回它的高度，否则返回 0。
func remuxCopyableHeight(probe *model.MediaProbe) int {
	if probe == nil || probe.Height <= 0 {
		return 0
	}
	if !strings.EqualFold(strings.TrimSpace(probe.VideoCodec), "h264") {
		return 0
	}
	if !browserSafeH264Payload(probe.Payload) {
		return 0
	}
	return probe.Height
}

// probePayloadStreams 只解析落库 payload 里与直通判断有关的字段。
type probePayloadStreams struct {
	Streams []struct {
		Type    string `json:"type"`
		Profile string `json:"profile"`
		PixFmt  string `json:"pix_fmt"`
	} `json:"streams"`
}

// browserSafeH264Payload 只放行 8bit 4:2:0 的 Baseline/Main/High。
// Hi10P（High 10）和 4:2:2 母带浏览器解不出来，复制过去只会黑屏或无声。
func browserSafeH264Payload(payload string) bool {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		// 老数据没有 payload：已经有 codec + 分辨率，按 8bit 常规情况处理。
		return true
	}
	var parsed probePayloadStreams
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return false
	}
	for _, stream := range parsed.Streams {
		if !strings.EqualFold(strings.TrimSpace(stream.Type), "video") {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(stream.Profile)) {
		case "", "constrained baseline", "baseline", "main", "high":
		default:
			return false
		}
		switch strings.ToLower(strings.TrimSpace(stream.PixFmt)) {
		case "", "yuv420p", "yuvj420p":
		default:
			return false
		}
		return true
	}
	return true
}
