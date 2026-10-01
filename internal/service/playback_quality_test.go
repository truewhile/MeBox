package service

import (
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/cloud115"
)

func TestCloud115QualityOptionsDefault1080AndTranscode(t *testing.T) {
	media := &model.Media{Height: 1080}
	data := &cloud115.VideoPlayData{
		DefinitionListNew: map[string]string{"3": "超清"},
		VideoURL: []cloud115.VideoURLItem{
			{Definition: 3, DefinitionN: 3, Height: 720, Width: 1280, Title: "超清"},
		},
	}
	options := Cloud115QualityOptions(media, data)
	if got := DefaultCloud115Quality(options); got != "4" {
		t.Fatalf("default quality = %q, want 4", got)
	}
	quality4, ok := findPlaybackQuality(options, "4")
	if !ok {
		t.Fatal("1080P option missing")
	}
	if quality4.Available || !quality4.RequiresTranscode {
		t.Fatalf("1080P should require transcode: %#v", quality4)
	}
	quality3, ok := findPlaybackQuality(options, "3")
	if !ok || !quality3.Available || quality3.RequiresTranscode {
		t.Fatalf("超清 should be available: %#v", quality3)
	}
	if quality4K, ok := findPlaybackQuality(options, "5"); ok {
		t.Fatalf("1080P source should not list 4K: %#v", quality4K)
	}
}

func TestCloud115QualityOptionsListsAvailableOnlyOnce(t *testing.T) {
	media := &model.Media{Height: 2160}
	data := &cloud115.VideoPlayData{
		DefinitionListNew: map[string]string{"4": "1080P", "5": "4K"},
		VideoURL: []cloud115.VideoURLItem{
			{Definition: 4, DefinitionN: 4, Height: 1080},
			{Definition: 5, DefinitionN: 5, Height: 2160},
		},
	}
	options := Cloud115QualityOptions(media, data)
	for _, id := range []string{"4", "5"} {
		quality, ok := findPlaybackQuality(options, id)
		if !ok || !quality.Available {
			t.Fatalf("quality %s should be available: %#v", id, quality)
		}
	}
	quality5, _ := findPlaybackQuality(options, "5")
	if !quality5.RequiresVIP {
		t.Fatalf("4K should be marked VIP: %#v", quality5)
	}
}

func TestLocalQualityOptionsFollowSourceHeight(t *testing.T) {
	media := &model.Media{Height: 720}
	options := LocalQualityOptions(media, true)
	if _, ok := findPlaybackQuality(options, "1080"); ok {
		t.Fatal("720p source should not list 1080P")
	}
	for _, id := range []string{"source", "720", "480"} {
		quality, ok := findPlaybackQuality(options, id)
		if !ok {
			t.Fatalf("local quality %s missing", id)
		}
		if !quality.Available {
			t.Fatalf("local quality %s should be available while transcoding works", id)
		}
	}
	if got := DefaultLocalHLSQualityID(media); got != "720" {
		t.Fatalf("default local quality = %q, want 720", got)
	}
}

// Without a usable ffmpeg the local HLS renditions must not advertise themselves
// as available: the player would request /api/hls/... and take a guaranteed 500
// before falling back to direct play.
func TestLocalQualityOptionsMarkUnavailableWithoutTranscoder(t *testing.T) {
	media := &model.Media{Height: 1080}
	for _, id := range []string{"source", "1080", "720", "480"} {
		for _, available := range []bool{true, false} {
			quality, ok := findPlaybackQuality(LocalQualityOptions(media, available), id)
			if !ok {
				t.Fatalf("local quality %s missing", id)
			}
			if quality.Available != available {
				t.Fatalf("quality %s availability = %v, want %v", id, quality.Available, available)
			}
			if !available && !quality.RequiresTranscode {
				t.Fatalf("unavailable quality %s should be flagged as requiring transcoding", id)
			}
		}
	}
}

func TestLocalHLSQualityFFmpegArgs(t *testing.T) {
	cfg := &config.Config{}
	cfg.Transcoder.MaxHeight = 720
	cfg.Transcoder.SegmentSeconds = 4
	cfg.Transcoder.Realtime = false
	cfg.Transcoder.HardwareAccel = false

	quality480, _ := LocalHLSQualityByID("480")
	args := buildFFmpegArgsForInput(cfg, transcodeInput{Source: "/x.mkv", Quality: &quality480}, "/o/x.m3u8", "/o/seg_%05d.ts")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "scale=-2:min(480\\,ih)") {
		t.Fatalf("480P scale filter missing: %s", joined)
	}
	if !strings.Contains(joined, "-b:v 1200k") {
		t.Fatalf("480P bitrate missing: %s", joined)
	}

	cfg.Transcoder.HardwareAccel = true
	cfg.Transcoder.Encoder = "nvenc"
	qualitySource, _ := LocalHLSQualityByID("source")
	args = buildFFmpegArgsForInput(cfg, transcodeInput{Source: "/x.mkv", Quality: &qualitySource}, "/o/x.m3u8", "/o/seg_%05d.ts")
	joined = strings.Join(args, " ")
	if strings.Contains(joined, "h264_nvenc") {
		t.Fatalf("source quality should not use hardware scale path: %s", joined)
	}
	if strings.Contains(joined, "scale=-2:min(") {
		t.Fatalf("source quality should not downscale: %s", joined)
	}
	if !strings.Contains(joined, "libx264") {
		t.Fatalf("source quality should use software x264: %s", joined)
	}
}
