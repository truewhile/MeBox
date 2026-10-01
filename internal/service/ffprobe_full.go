package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ffprobeFullTimeout 是一次全量探测（含章节）的超时。远端直链实测 3～4 秒，
// 留足余量的同时不能让一个坏源把工作协程永久占住。
const ffprobeFullTimeout = 60 * time.Second

// ProbeInput 是喂给 ffprobe 的输入：本地文件给路径，STRM / 云盘给已解析的最终
// 直链加绑定请求头。解析由调用方负责（复用播放链路的换链逻辑），否则会踩到
// 115 CDN 的防盗链 403。
type ProbeInput struct {
	Source  string
	Headers map[string]string
}

// ProbeStream 是一路轨道的关键字段，供详情页展示。
type ProbeStream struct {
	Index         int    `json:"index"`
	Type          string `json:"type"`
	Codec         string `json:"codec,omitempty"`
	Profile       string `json:"profile,omitempty"`
	Language      string `json:"language,omitempty"`
	Title         string `json:"title,omitempty"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	PixFmt        string `json:"pix_fmt,omitempty"`
	FrameRate     string `json:"frame_rate,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	ChannelLayout string `json:"channel_layout,omitempty"`
	SampleRate    int    `json:"sample_rate,omitempty"`
	BitRate       int64  `json:"bit_rate,omitempty"`
	Default       bool   `json:"default,omitempty"`
	Forced        bool   `json:"forced,omitempty"`
}

// ProbeChapter 是一个内嵌章节区间。
type ProbeChapter struct {
	Index   int    `json:"index"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	Title   string `json:"title,omitempty"`
}

// FullProbeResult 是一次全量探测的裁剪结果。
type FullProbeResult struct {
	Container   string
	DurationSec int
	BitRate     int64
	Streams     []ProbeStream
	Chapters    []ProbeChapter
}

// mediaProbePayload 是落库的媒体信息结构。它只包含白名单字段——刻意不接受
// ffprobe 的原始 JSON，因为其中的 format.filename 是播放直链（含签名与
// pickcode），原样保存会外泄。
type mediaProbePayload struct {
	Container   string         `json:"container,omitempty"`
	DurationSec int            `json:"duration_sec"`
	BitRate     int64          `json:"bit_rate,omitempty"`
	Streams     []ProbeStream  `json:"streams"`
	Chapters    []ProbeChapter `json:"chapters,omitempty"`
}

// StreamsOfType 返回指定类型的轨道，供详情页分组展示。
func (r *FullProbeResult) StreamsOfType(kind string) []ProbeStream {
	if r == nil {
		return nil
	}
	out := make([]ProbeStream, 0, len(r.Streams))
	for _, s := range r.Streams {
		if s.Type == kind {
			out = append(out, s)
		}
	}
	return out
}

// PayloadJSON 序列化落库用的媒体信息。
func (r *FullProbeResult) PayloadJSON() (string, error) {
	if r == nil {
		return "", nil
	}
	streams := r.Streams
	if streams == nil {
		streams = []ProbeStream{}
	}
	body, err := json.Marshal(mediaProbePayload{
		Container:   r.Container,
		DurationSec: r.DurationSec,
		BitRate:     r.BitRate,
		Streams:     streams,
		Chapters:    r.Chapters,
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// ProbeFull 跑一次全量探测：容器信息 + 全部轨道 + 内嵌章节。
//
// 与 Probe 的区别：Probe 只取扫描需要的几个字段、对着媒体行的本地路径跑；
// ProbeFull 接受调用方解析好的输入（远端直链 + 绑定请求头），并额外抓章节。
func (f *FFprobeService) ProbeFull(ctx context.Context, input ProbeInput) (*FullProbeResult, error) {
	if f == nil || f.cfg == nil {
		return nil, errors.New("ffprobe service nil")
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		return nil, errors.New("empty probe source")
	}
	token, err := f.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer f.release(token)

	bin, err := resolveLocalExecutable(f.cfg.App.FFprobePath, "ffprobe")
	if err != nil {
		return nil, fmt.Errorf("ffprobe unavailable: %w", err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, ffprobeFullTimeout)
	defer cancel()

	args := []string{"-v", "error"}
	if headerText := ffmpegHeaderText(input.Headers); headerText != "" {
		args = append(args, "-headers", headerText)
	}
	args = append(args,
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-show_chapters",
		source,
	)
	out, err := exec.CommandContext(probeCtx, bin, args...).Output() // #nosec G204 -- bin is resolved by resolveLocalExecutable before execution.
	if err != nil {
		return nil, fmt.Errorf("ffprobe full: %w", err)
	}
	return parseFullProbeJSON(out)
}

// probeNumber 兼容 ffprobe 把数值输出成字符串或裸数字两种形态
// （duration 是字符串，chapter 的 start_time 也可能是数字）。解析不出来就保持
// 零值：这些字段都只是展示用，不该因为一个格式差异让整次探测失败。
type probeNumber float64

func (p *probeNumber) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(strings.Trim(string(data), `"`))
	if text == "" || text == "null" {
		return nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil
	}
	*p = probeNumber(value)
	return nil
}

func (p probeNumber) float() float64 { return float64(p) }
func (p probeNumber) int() int       { return int(float64(p)) }
func (p probeNumber) int64() int64   { return int64(float64(p)) }

// rawFullProbe 镜像 ffprobe -show_format -show_streams -show_chapters 的输出。
// 只声明用得到的字段；format.filename 刻意不声明，避免它进入任何落库路径。
type rawFullProbe struct {
	Format struct {
		FormatName string      `json:"format_name"`
		Duration   probeNumber `json:"duration"`
		BitRate    probeNumber `json:"bit_rate"`
	} `json:"format"`
	Streams []struct {
		Index         int         `json:"index"`
		CodecType     string      `json:"codec_type"`
		CodecName     string      `json:"codec_name"`
		Profile       string      `json:"profile"`
		Width         int         `json:"width"`
		Height        int         `json:"height"`
		PixFmt        string      `json:"pix_fmt"`
		AvgFrameRate  string      `json:"avg_frame_rate"`
		Channels      int         `json:"channels"`
		ChannelLayout string      `json:"channel_layout"`
		SampleRate    probeNumber `json:"sample_rate"`
		BitRate       probeNumber `json:"bit_rate"`
		Tags          struct {
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"tags"`
		Disposition struct {
			Default int `json:"default"`
			Forced  int `json:"forced"`
		} `json:"disposition"`
	} `json:"streams"`
	Chapters []struct {
		StartTime probeNumber `json:"start_time"`
		EndTime   probeNumber `json:"end_time"`
		Tags      struct {
			Title string `json:"title"`
		} `json:"tags"`
	} `json:"chapters"`
}

func parseFullProbeJSON(data []byte) (*FullProbeResult, error) {
	var raw rawFullProbe
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse ffprobe json: %w", err)
	}
	result := &FullProbeResult{
		Container:   strings.TrimSpace(raw.Format.FormatName),
		DurationSec: raw.Format.Duration.int(),
		BitRate:     raw.Format.BitRate.int64(),
		Streams:     make([]ProbeStream, 0, len(raw.Streams)),
		Chapters:    make([]ProbeChapter, 0, len(raw.Chapters)),
	}
	for _, s := range raw.Streams {
		stream := ProbeStream{
			Index:         s.Index,
			Type:          s.CodecType,
			Codec:         s.CodecName,
			Profile:       s.Profile,
			Language:      strings.TrimSpace(s.Tags.Language),
			Title:         strings.TrimSpace(s.Tags.Title),
			Width:         s.Width,
			Height:        s.Height,
			PixFmt:        s.PixFmt,
			FrameRate:     normalizeFrameRate(s.AvgFrameRate),
			Channels:      s.Channels,
			ChannelLayout: s.ChannelLayout,
			SampleRate:    s.SampleRate.int(),
			BitRate:       s.BitRate.int64(),
			Default:       s.Disposition.Default != 0,
			Forced:        s.Disposition.Forced != 0,
		}
		result.Streams = append(result.Streams, stream)
	}
	for index, chapter := range raw.Chapters {
		result.Chapters = append(result.Chapters, ProbeChapter{
			Index:   index,
			StartMs: secondsToMillis(chapter.StartTime.float()),
			EndMs:   secondsToMillis(chapter.EndTime.float()),
			Title:   strings.TrimSpace(chapter.Tags.Title),
		})
	}
	return result, nil
}

// secondsToMillis 把 ffprobe 的秒（浮点）转成毫秒整数。
func secondsToMillis(seconds float64) int64 {
	if seconds <= 0 {
		return 0
	}
	return int64(seconds*1000 + 0.5)
}

// normalizeFrameRate 把 "24000/1001" 这类分数帧率换算成可读形式；"0/0"
// （未知）返回空串。
func normalizeFrameRate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		return raw
	}
	num, errNum := strconv.ParseFloat(parts[0], 64)
	den, errDen := strconv.ParseFloat(parts[1], 64)
	if errNum != nil || errDen != nil || den == 0 || num <= 0 {
		return ""
	}
	return strconv.FormatFloat(num/den, 'f', 3, 64)
}
