package service

import (
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
)

// 直通档位必须真的把视频流复制过去：一旦又带上 libx264 / -vf / 码率参数，
// 慢机器上的「原画」就还是几十秒起播。
func TestBuildFFmpegArgsRemuxCopiesVideoStream(t *testing.T) {
	cfg := &config.Config{}
	cfg.Transcoder.SegmentSeconds = 4
	cfg.Transcoder.Realtime = true
	cfg.Transcoder.Threads = 2
	cfg.Transcoder.MaxHeight = 720

	quality, ok := LocalHLSQualityByID("source")
	if !ok {
		t.Fatal("source quality missing")
	}
	args := buildFFmpegArgsForInput(cfg, transcodeInput{
		Source:    "https://cdn.example/x.m2ts",
		Headers:   map[string]string{"User-Agent": "MeBox-115-GoClient/1.0"},
		StartSec:  52.4,
		Quality:   &quality,
		CopyVideo: true,
	}, "/cache/hls/k/run-1/index.m3u8", "/cache/hls/k/run-1/seg_%05d.ts")
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"-c:v copy",
		"-c:a aac",
		"-ar 48000",
		"-b:a 128k",
		"-ac 2",
		"-avoid_negative_ts make_zero",
		"-ss 52.400",
		"-noaccurate_seek",
		"-hls_time 4",
		"-hls_segment_filename /cache/hls/k/run-1/seg_%05d.ts",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("remux args missing %q: %s", want, joined)
		}
	}
	for _, unwanted := range []string{
		"libx264",
		"-vf ",
		"-b:v",
		"-maxrate",
		"-bufsize",
		"-force_key_frames",
		"-preset",
		"-threads",
		"-pix_fmt",
	} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("remux args must not contain %q: %s", unwanted, joined)
		}
	}
	// 中途起播不能加 -re，否则会先按 1 倍速爬到目标位置才出第一个切片。
	if strings.Contains(joined, " -re ") {
		t.Errorf("mid-file remux must not use -re: %s", joined)
	}
}

// 烧录图形字幕要把字幕叠进画面，必须解码重编码，不能被直通标志绕过。
func TestBuildFFmpegArgsRemuxIgnoredForSubtitleBurnIn(t *testing.T) {
	cfg := &config.Config{}
	cfg.Transcoder.MaxHeight = 720
	cfg.Transcoder.SegmentSeconds = 4

	quality, _ := LocalHLSQualityByID("source")
	subtitle := 4
	args := buildFFmpegArgsForInput(cfg, transcodeInput{
		Source:         "/media/ep.mkv",
		Quality:        &quality,
		CopyVideo:      true,
		SubtitleStream: &subtitle,
	}, "/o/x.m3u8", "/o/seg_%05d.ts")
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "-c:v copy") {
		t.Fatalf("subtitle burn-in must not copy the video stream: %s", joined)
	}
	if !strings.Contains(joined, "libx264") || !strings.Contains(joined, "-filter_complex") {
		t.Fatalf("subtitle burn-in should re-encode: %s", joined)
	}
	if strings.Contains(joined, "-noaccurate_seek") {
		t.Fatalf("encoded path should keep accurate seek: %s", joined)
	}
}

// 只有「原画」档位（不缩放）可以直通；其它档位要改分辨率。
func TestRemuxAppliesToQuality(t *testing.T) {
	if remuxAppliesToQuality(nil) {
		t.Fatal("nil quality must not remux: it falls back to the configured max height")
	}
	source, _ := LocalHLSQualityByID("source")
	if !remuxAppliesToQuality(&source) {
		t.Fatal("source quality should remux")
	}
	for _, id := range []string{"1080", "720", "480"} {
		quality, ok := LocalHLSQualityByID(id)
		if !ok {
			t.Fatalf("quality %s missing", id)
		}
		if remuxAppliesToQuality(&quality) {
			t.Fatalf("quality %s must re-encode", id)
		}
	}
}

// 只有 8bit 4:2:0 的 H.264 能安全复制；Hi10P / 4:2:2 / HEVC 复制过去浏览器黑屏。
func TestRemuxCopyableHeight(t *testing.T) {
	const blurayPayload = `{"container":"mpegts","duration_sec":1451,"bit_rate":42609934,"streams":[{"index":0,"type":"video","codec":"h264","profile":"High","width":1920,"height":1080,"pix_fmt":"yuv420p","frame_rate":"23.976"},{"index":1,"type":"audio","codec":"pcm_bluray","channels":2}]}`

	cases := []struct {
		name  string
		probe *model.MediaProbe
		want  int
	}{
		{
			name:  "bluray h264 8bit 420",
			probe: &model.MediaProbe{VideoCodec: "h264", Height: 1080, Payload: blurayPayload},
			want:  1080,
		},
		{
			name:  "no payload falls back to codec and height",
			probe: &model.MediaProbe{VideoCodec: "h264", Height: 720},
			want:  720,
		},
		{
			name:  "hevc cannot be copied into browser hls",
			probe: &model.MediaProbe{VideoCodec: "hevc", Height: 1080, Payload: `{"streams":[{"type":"video","profile":"Main 10","pix_fmt":"yuv420p10le"}]}`},
			want:  0,
		},
		{
			name:  "hi10p h264 is unplayable in browsers",
			probe: &model.MediaProbe{VideoCodec: "h264", Height: 1080, Payload: `{"streams":[{"type":"video","profile":"High 10","pix_fmt":"yuv420p10le"}]}`},
			want:  0,
		},
		{
			name:  "4:2:2 h264 is unplayable in browsers",
			probe: &model.MediaProbe{VideoCodec: "h264", Height: 1080, Payload: `{"streams":[{"type":"video","profile":"High 4:2:2","pix_fmt":"yuv422p"}]}`},
			want:  0,
		},
		{
			name:  "missing probe",
			probe: nil,
			want:  0,
		},
		{
			name:  "unknown height",
			probe: &model.MediaProbe{VideoCodec: "h264", Height: 0, Payload: blurayPayload},
			want:  0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := remuxCopyableHeight(tc.probe); got != tc.want {
				t.Fatalf("remuxCopyableHeight() = %d, want %d", got, tc.want)
			}
		})
	}
}
