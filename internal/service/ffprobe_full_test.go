package service

import (
	"strings"
	"testing"
)

const fullProbeFixture = `{
  "format": {
    "format_name": "matroska,webm",
    "duration": "1451.024000",
    "bit_rate": "8000000",
    "filename": "https://cdn.example.com/secret/movie.mkv?d=vip-abc-pickcode&token=xyz"
  },
  "streams": [
    {"index": 0, "codec_type": "video", "codec_name": "hevc", "profile": "Main 10",
     "width": 3840, "height": 2160, "pix_fmt": "yuv420p10le", "avg_frame_rate": "24000/1001",
     "bit_rate": "7800000", "disposition": {"default": 1}},
    {"index": 1, "codec_type": "audio", "codec_name": "eac3", "channels": 6,
     "channel_layout": "5.1(side)", "sample_rate": "48000",
     "tags": {"language": "eng", "title": "Surround"}, "disposition": {"default": 1}},
    {"index": 2, "codec_type": "subtitle", "codec_name": "ass",
     "tags": {"language": "chi"}, "disposition": {"forced": 1}}
  ],
  "chapters": [
    {"start_time": "0.000000", "end_time": "95.000000", "tags": {"title": "Chapter 01"}},
    {"start_time": "228.664000", "end_time": "246.143000", "tags": {"title": "Opening"}}
  ]
}`

func TestParseFullProbeJSONExtractsStreamsAndChapters(t *testing.T) {
	got, err := parseFullProbeJSON([]byte(fullProbeFixture))
	if err != nil {
		t.Fatalf("parseFullProbeJSON: %v", err)
	}
	if got.Container != "matroska,webm" || got.DurationSec != 1451 || got.BitRate != 8_000_000 {
		t.Fatalf("container/duration/bitrate = %q/%d/%d", got.Container, got.DurationSec, got.BitRate)
	}
	if len(got.Streams) != 3 {
		t.Fatalf("streams = %#v, want 3", got.Streams)
	}
	video := got.Streams[0]
	if video.Type != "video" || video.Codec != "hevc" || video.Width != 3840 || video.Height != 2160 {
		t.Fatalf("video stream = %#v", video)
	}
	if video.FrameRate != "23.976" {
		t.Fatalf("frame rate = %q, want 23.976 (converted from 24000/1001)", video.FrameRate)
	}
	if !video.Default {
		t.Fatal("video stream should be flagged default")
	}
	audio := got.Streams[1]
	if audio.Codec != "eac3" || audio.Language != "eng" || audio.Title != "Surround" || audio.Channels != 6 || audio.SampleRate != 48000 {
		t.Fatalf("audio stream = %#v", audio)
	}
	sub := got.Streams[2]
	if sub.Type != "subtitle" || sub.Language != "chi" || !sub.Forced {
		t.Fatalf("subtitle stream = %#v", sub)
	}

	if len(got.Chapters) != 2 {
		t.Fatalf("chapters = %#v, want 2", got.Chapters)
	}
	if got.Chapters[1].StartMs != 228_664 || got.Chapters[1].EndMs != 246_143 || got.Chapters[1].Title != "Opening" {
		t.Fatalf("chapter[1] = %#v", got.Chapters[1])
	}

	if videoCount := len(got.StreamsOfType("video")); videoCount != 1 {
		t.Fatalf("StreamsOfType(video) = %d, want 1", videoCount)
	}
	if audioCount := len(got.StreamsOfType("audio")); audioCount != 1 {
		t.Fatalf("StreamsOfType(audio) = %d, want 1", audioCount)
	}
}

// 落库的 payload 绝不能带 ffprobe 的 format.filename：那是解析后的播放直链，
// 里面是网盘签名和 pickcode，存进数据库等于把可直接下载的链接留下来。
func TestFullProbePayloadNeverLeaksSourceURL(t *testing.T) {
	got, err := parseFullProbeJSON([]byte(fullProbeFixture))
	if err != nil {
		t.Fatalf("parseFullProbeJSON: %v", err)
	}
	payload, err := got.PayloadJSON()
	if err != nil {
		t.Fatalf("PayloadJSON: %v", err)
	}
	for _, needle := range []string{"filename", "cdn.example.com", "pickcode", "token=", "http"} {
		if strings.Contains(payload, needle) {
			t.Fatalf("payload 泄漏了 %q:\n%s", needle, payload)
		}
	}
	// 技术信息本身必须保留。
	for _, needle := range []string{"matroska,webm", "hevc", "3840", "Opening"} {
		if !strings.Contains(payload, needle) {
			t.Fatalf("payload 缺少 %q:\n%s", needle, payload)
		}
	}
}

// ffprobe 对 duration / start_time 有时输出字符串、有时输出裸数字，两种都要能读。
func TestParseFullProbeJSONAcceptsNumericAndStringTimes(t *testing.T) {
	got, err := parseFullProbeJSON([]byte(`{
		"format": {"format_name": "mp4", "duration": 125.5},
		"streams": [],
		"chapters": [{"start_time": 12.5, "end_time": 20}]
	}`))
	if err != nil {
		t.Fatalf("parseFullProbeJSON: %v", err)
	}
	if got.DurationSec != 125 {
		t.Fatalf("duration = %d, want 125", got.DurationSec)
	}
	if len(got.Chapters) != 1 || got.Chapters[0].StartMs != 12_500 || got.Chapters[0].EndMs != 20_000 {
		t.Fatalf("chapters = %#v", got.Chapters)
	}
}

func TestParseFullProbeJSONToleratesMissingSections(t *testing.T) {
	got, err := parseFullProbeJSON([]byte(`{}`))
	if err != nil {
		t.Fatalf("parseFullProbeJSON: %v", err)
	}
	if got.DurationSec != 0 || len(got.Streams) != 0 || len(got.Chapters) != 0 {
		t.Fatalf("result = %#v, want empty", got)
	}
	payload, err := got.PayloadJSON()
	if err != nil {
		t.Fatalf("PayloadJSON: %v", err)
	}
	// 空轨道必须序列化成 []，不能是 null——详情页前端按数组消费。
	if !strings.Contains(payload, `"streams":[]`) {
		t.Fatalf("payload = %s, want an empty streams array", payload)
	}
}

func TestNormalizeFrameRate(t *testing.T) {
	cases := map[string]string{
		"24000/1001": "23.976",
		"25/1":       "25.000",
		"0/0":        "",
		"":           "",
		"25":         "25",
	}
	for input, want := range cases {
		if got := normalizeFrameRate(input); got != want {
			t.Errorf("normalizeFrameRate(%q) = %q, want %q", input, got, want)
		}
	}
}
