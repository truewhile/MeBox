package service

import (
	"context"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/cloud"
)

func TestBuildFFmpegArgs(t *testing.T) {
	base := &config.Config{}
	base.Transcoder.MaxHeight = 720
	base.Transcoder.SegmentSeconds = 4
	base.Transcoder.Realtime = true
	base.Transcoder.Threads = 2
	base.App.VAAPIDevice = "/dev/dri/renderD128"

	cases := []struct {
		name                   string
		encoder                string
		expectVCodec           string
		expectInArgs           []string
		expectNotPresetIfBlank bool
	}{
		{"software", "", "libx264", []string{"-re", "-preset", "veryfast", "-c:v", "libx264", "-threads", "2"}, false},
		{"nvenc", "nvenc", "h264_nvenc", []string{"-hwaccel", "cuda", "-c:v", "h264_nvenc", "-preset", "p4"}, false},
		{"qsv", "qsv", "h264_qsv", []string{"-hwaccel", "qsv", "-c:v", "h264_qsv"}, false},
		{"vaapi", "vaapi", "h264_vaapi", []string{"-hwaccel", "vaapi", "-vaapi_device", "/dev/dri/renderD128", "-c:v", "h264_vaapi"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := *base
			cfg.Transcoder.Encoder = tc.encoder
			cfg.Transcoder.HardwareAccel = tc.encoder != ""
			args := buildFFmpegArgs(&cfg, "/x.mkv", "/o/x.m3u8", "/o/seg_%05d.ts")
			joined := strings.Join(args, " ")
			for _, frag := range tc.expectInArgs {
				if !strings.Contains(joined, frag) {
					t.Errorf("expected %q in args, got: %s", frag, joined)
				}
			}
			// vaapi has no -preset flag.
			if tc.expectNotPresetIfBlank && strings.Contains(joined, "-preset") {
				t.Errorf("vaapi should not include -preset, got: %s", joined)
			}
		})
	}
}

func TestBuildFFmpegArgsIgnoresEncoderWhenHardwareAccelDisabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Transcoder.Encoder = "nvenc"
	cfg.Transcoder.HardwareAccel = false
	cfg.Transcoder.MaxHeight = 720
	cfg.Transcoder.SegmentSeconds = 4
	cfg.Transcoder.Realtime = true
	cfg.Transcoder.Threads = 2

	args := buildFFmpegArgs(cfg, "/x.mkv", "/o/x.m3u8", "/o/seg_%05d.ts")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "h264_nvenc") {
		t.Fatalf("hardware disabled should not use nvenc, got: %s", joined)
	}
	if !strings.Contains(joined, "libx264") {
		t.Fatalf("hardware disabled should fall back to libx264, got: %s", joined)
	}
}

func TestBuildFFmpegArgsCanDisableRealtimeAndThreadCap(t *testing.T) {
	cfg := &config.Config{}
	cfg.Transcoder.MaxHeight = 720
	cfg.Transcoder.SegmentSeconds = 4
	cfg.Transcoder.Realtime = false
	cfg.Transcoder.Threads = 0

	args := buildFFmpegArgs(cfg, "/x.mkv", "/o/x.m3u8", "/o/seg_%05d.ts")
	joined := " " + strings.Join(args, " ") + " "
	if strings.Contains(joined, " -re ") {
		t.Fatalf("realtime=false should not include -re, got: %s", joined)
	}
	if strings.Contains(joined, " -threads ") {
		t.Fatalf("threads=0 should not include -threads, got: %s", joined)
	}
}

func TestRequiredVideoEncoder(t *testing.T) {
	cases := map[string]string{
		"":      "libx264",
		"nvenc": "h264_nvenc",
		"qsv":   "h264_qsv",
		"vaapi": "h264_vaapi",
	}
	for encoder, want := range cases {
		if got := requiredVideoEncoder(encoder); got != want {
			t.Fatalf("requiredVideoEncoder(%q) = %q, want %q", encoder, got, want)
		}
	}
}

func TestHasFFmpegListEntry(t *testing.T) {
	out := " V..... libx264              libx264 H.264 / AVC\n A..... aac"
	if !hasFFmpegListEntry(out, "libx264") {
		t.Fatal("expected libx264 entry")
	}
	if hasFFmpegListEntry(out, "x264") {
		t.Fatal("must match whole ffmpeg list entries only")
	}
}

func TestResolveTranscodeInputHTTPSTRM(t *testing.T) {
	svc := &TranscoderService{}
	got, err := svc.resolveTranscodeInput(context.Background(), &model.Media{
		Container: "strm",
		STRMURL:   "https://cdn.example.com/a.wmv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "https://cdn.example.com/a.wmv" {
		t.Fatalf("source = %q", got.Source)
	}
}

func TestResolveTranscodeInputUsesResolver(t *testing.T) {
	svc := &TranscoderService{}
	svc.SetStrmPlayTargetResolver(func(_ context.Context, raw string) (*StrmPlayResult, error) {
		if raw != "/api/strm/play/cloud115/a.wmv?acct=1&pickcode=x" {
			t.Fatalf("raw = %q", raw)
		}
		return &StrmPlayResult{
			RedirectURL: "https://cdn.example.com/a.wmv",
			Link: &cloud.DirectLink{
				URL:     "https://cdn.example.com/a.wmv",
				Headers: map[string]string{"User-Agent": "Mozilla/5.0"},
			},
		}, nil
	})
	got, err := svc.resolveTranscodeInput(context.Background(), &model.Media{
		Container: "strm",
		STRMURL:   "/api/strm/play/cloud115/a.wmv?acct=1&pickcode=x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "https://cdn.example.com/a.wmv" {
		t.Fatalf("source = %q", got.Source)
	}
	if got.Headers["User-Agent"] != "Mozilla/5.0" {
		t.Fatalf("headers = %#v", got.Headers)
	}
}

func TestResolveTranscodeInputRejectsUnresolvedRelativeSTRM(t *testing.T) {
	svc := &TranscoderService{}
	_, err := svc.resolveTranscodeInput(context.Background(), &model.Media{
		Container: "strm",
		STRMURL:   "/api/strm/play/cloud115/a.wmv?acct=1&pickcode=x",
	})
	if err == nil {
		t.Fatal("expected unresolved relative strm to fail")
	}
}

func TestBuildFFmpegArgsHTTPInputReconnect(t *testing.T) {
	cfg := &config.Config{}
	cfg.Transcoder.MaxHeight = 720
	cfg.Transcoder.SegmentSeconds = 4
	args := buildFFmpegArgsForInput(cfg, transcodeInput{
		Source:  "https://cdn.example.com/a.wmv",
		Headers: map[string]string{"User-Agent": "MeBox", "Referer": "https://cdn.example.com/"},
	}, "/o/x.m3u8", "/o/seg_%05d.ts")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-reconnect") || !strings.Contains(joined, "-headers") {
		t.Fatalf("expected http reconnect/headers, got: %s", joined)
	}
	if !strings.Contains(joined, "User-Agent: MeBox") || !strings.Contains(joined, "Referer: https://cdn.example.com/") {
		t.Fatalf("expected request headers, got: %s", joined)
	}
	idxI, idxH := -1, -1
	for i, arg := range args {
		if arg == "-i" && idxI < 0 {
			idxI = i
		}
		if arg == "-headers" {
			idxH = i
		}
	}
	if idxI < 0 || idxH < 0 || idxH > idxI {
		t.Fatalf("http flags must come before -i, args=%v", args)
	}
}
