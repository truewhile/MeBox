package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestHLSCompletedTranscodeStaysReadyAndIsReused(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required for the real HLS lifecycle test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	source := filepath.Join(t.TempDir(), "short.mp4")
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=64x64:rate=10", "-t", "1", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-threads", "1", source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v: %s", err, output)
	}
	cfg := &config.Config{}
	cfg.App.FFmpegPath = bin
	cfg.Cache.CacheDir = t.TempDir()
	cfg.Transcoder.Enabled = true
	cfg.Transcoder.MaxConcurrent = 1
	cfg.Transcoder.MaxHeight = 64
	cfg.Transcoder.SegmentSeconds = 1
	cfg.Transcoder.Threads = 1
	db := newServiceTestDB(t)
	media := model.Media{Path: source, DurationSec: 1}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewTranscoderService(cfg, zap.NewNop(), repository.New(db), NewHub(zap.NewNop()))
	t.Cleanup(svc.StopAll)
	playlist, err := svc.EnsureJob(ctx, media.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	job := svc.jobs[media.ID]
	svc.mu.Unlock()
	if job == nil {
		t.Fatal("transcode lost its job before the playlist could be read")
	}
	select {
	case <-job.done:
	case <-ctx.Done():
		t.Fatal("short transcode did not finish")
	}
	data, err := os.ReadFile(playlist)
	if err != nil || !strings.Contains(string(data), "#EXT-X-ENDLIST") {
		t.Fatalf("transcode did not produce a complete playlist: err=%v, data=%s", err, data)
	}
	if !svc.WaitReady(ctx, media.ID, 100*time.Millisecond) {
		t.Fatal("completed HLS playlist is incorrectly reported as not ready")
	}
	if len(svc.Active()) != 0 {
		t.Fatal("completed transcode still occupies an active job slot")
	}
	if _, err := svc.EnsureJob(ctx, media.ID); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	reused := svc.jobs[media.ID] == job
	svc.mu.Unlock()
	if !reused {
		t.Fatal("refreshing a completed playlist started a second transcode")
	}
	other := model.Media{Path: source + ".second", DurationSec: 1}
	if err := os.WriteFile(other.Path, mustReadTestFile(t, source), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureJob(ctx, other.ID); err != nil {
		t.Fatalf("completed cache must not consume MaxConcurrent: %v", err)
	}
}

func mustReadTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestHLSWaitReadyReturnsImmediatelyForMissingJob(t *testing.T) {
	svc := NewTranscoderService(&config.Config{}, zap.NewNop(), nil, NewHub(zap.NewNop()))
	started := time.Now()
	if svc.WaitReady(t.Context(), "failed", time.Second) {
		t.Fatal("missing job must not be ready")
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("missing/failed job waited for the readiness deadline: %v", elapsed)
	}
}

func countingFFmpeg(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test ffmpeg uses a POSIX shell")
	}
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$0.calls"
case " $* " in
  *" -encoders "*) printf ' V..... libx264\n V..... h264_nvenc\n' ;;
  *" -muxers "*) printf ' E hls\n' ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestFFmpegValidationIsCachedAndInvalidatedByEncoder(t *testing.T) {
	bin := countingFFmpeg(t)
	cfg := &config.Config{}
	cfg.App.FFmpegPath = bin
	svc := NewTranscoderService(cfg, zap.NewNop(), nil, NewHub(zap.NewNop()))
	for range 3 {
		if _, err := svc.resolveFFmpegPath(); err != nil {
			t.Fatal(err)
		}
	}
	calls := strings.Count(string(mustReadTestFile(t, bin+".calls")), "\n")
	if calls != 2 {
		t.Fatalf("repeated resolution spawned ffmpeg %d times, want one encoder/muxer probe pair", calls)
	}
	ApplyRuntimeSetting(cfg, "transcoder.hardware_accel", "true")
	ApplyRuntimeSetting(cfg, "transcoder.encoder", "nvenc")
	if _, err := svc.resolveFFmpegPath(); err != nil {
		t.Fatal(err)
	}
	calls = strings.Count(string(mustReadTestFile(t, bin+".calls")), "\n")
	if calls != 4 {
		t.Fatalf("encoder change must revalidate capabilities, calls=%d", calls)
	}
	changed := time.Now().Add(time.Second)
	if err := os.Chtimes(bin, changed, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.resolveFFmpegPath(); err != nil {
		t.Fatal(err)
	}
	calls = strings.Count(string(mustReadTestFile(t, bin+".calls")), "\n")
	if calls != 6 {
		t.Fatalf("binary replacement must invalidate validation, calls=%d", calls)
	}
}

func TestHLSIdleCleanupPreservesReplacementAndRecentActivity(t *testing.T) {
	svc := NewTranscoderService(&config.Config{}, zap.NewNop(), nil, NewHub(zap.NewNop()))
	done := make(chan struct{})
	close(done)
	cancelled := false
	old := &hlsJob{mediaID: "m", lastAccess: time.Now().Add(-time.Hour)}
	current := &hlsJob{mediaID: "m", lastAccess: time.Now(), done: done, cancel: func() { cancelled = true }}
	svc.jobs["m"] = current
	if !svc.stopIdleJob(old, time.Minute) || cancelled || svc.jobs["m"] != current {
		t.Fatal("an old watchdog must leave the replacement generation intact")
	}
	if svc.stopIdleJob(current, time.Minute) || cancelled {
		t.Fatal("recent segment access must prevent idle cancellation")
	}
	current.lastAccess = time.Now().Add(-time.Hour)
	if !svc.stopIdleJob(current, time.Minute) || !cancelled || svc.jobs["m"] != nil {
		t.Fatal("an idle generation must release its resources")
	}
}

func TestHLSOriginalQualityValidatesSoftwareEncoder(t *testing.T) {
	bin := countingFFmpeg(t)
	softwareOnly := strings.ReplaceAll(string(mustReadTestFile(t, bin)), ` V..... h264_nvenc\n`, "")
	if err := os.WriteFile(bin, []byte(softwareOnly), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())
	cfg := &config.Config{}
	cfg.App.FFmpegPath = bin
	cfg.Transcoder.Enabled = true
	cfg.Transcoder.HardwareAccel = true
	cfg.Transcoder.Encoder = "nvenc"
	cfg.Cache.CacheDir = t.TempDir()
	db := newServiceTestDB(t)
	source := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	media := model.Media{Path: source, DurationSec: 60}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewTranscoderService(cfg, zap.NewNop(), repository.New(db), NewHub(zap.NewNop()))
	t.Cleanup(svc.StopAll)
	if _, err := svc.EnsureJobFromSubtitleQuality(t.Context(), media.ID, 0, 1, -1, "source"); err != nil {
		t.Fatalf("source quality uses software and must not require NVENC: %v", err)
	}
	svc.mu.Lock()
	job := svc.jobs[media.ID]
	svc.mu.Unlock()
	select {
	case <-job.done:
	case <-time.After(time.Second):
		t.Fatal("fake ffmpeg did not finish")
	}
	calls := string(mustReadTestFile(t, bin+".calls"))
	if strings.Count(calls, "\n") != 3 || !strings.Contains(calls, "-c:v libx264") {
		t.Fatalf("startup must probe once and encode with libx264, calls=%s", calls)
	}
}
