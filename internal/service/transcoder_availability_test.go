package service

import (
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

// Available() gates the local HLS renditions advertised to the player, so it must
// be false both when transcoding is disabled by configuration and when no usable
// ffmpeg can be resolved.
func TestTranscoderAvailableGatesOnConfigAndBinary(t *testing.T) {
	cases := []struct {
		name       string
		enabled    bool
		ffmpegPath string
	}{
		{name: "disabled by config", enabled: false, ffmpegPath: filepath.Join(t.TempDir(), "missing-ffmpeg")},
		{name: "no ffmpeg binary", enabled: true, ffmpegPath: filepath.Join(t.TempDir(), "missing-ffmpeg")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Transcoder.Enabled = tc.enabled
			cfg.App.FFmpegPath = tc.ffmpegPath
			svc := NewTranscoderService(cfg, zap.NewNop(), nil, NewHub(zap.NewNop()))

			if svc.Available() {
				t.Fatalf("Available() = true, want false (enabled=%v path=%q)", tc.enabled, tc.ffmpegPath)
			}
		})
	}
}

// A nil service or nil config must not panic: playback info is built on paths
// where wiring may be partial.
func TestTranscoderAvailableNilSafe(t *testing.T) {
	var nilSvc *TranscoderService
	if nilSvc.Available() {
		t.Fatal("nil transcoder should report unavailable")
	}
	svc := &TranscoderService{}
	if svc.Available() {
		t.Fatal("transcoder without config should report unavailable")
	}
}

// Playback info must not advertise local HLS renditions when the playback service
// has no transcoder wired in.
func TestCloud115PlaybackServiceWithoutTranscoderMarksLocalQualitiesUnavailable(t *testing.T) {
	svc := NewCloud115PlaybackService(&config.Config{}, zap.NewNop(), nil, nil)
	if svc.localTranscodeAvailable() {
		t.Fatal("playback service without a transcoder must report local HLS unavailable")
	}
	options := LocalQualityOptions(nil, svc.localTranscodeAvailable())
	if len(options) == 0 {
		t.Fatal("local quality presets should still be listed for the UI")
	}
	for _, option := range options {
		if option.Available {
			t.Fatalf("quality %s advertised as available without a transcoder", option.ID)
		}
	}
}
