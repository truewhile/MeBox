package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

func (t *TranscoderService) runFFmpeg(ctx context.Context, job *hlsJob, input transcodeInput) {
	playlist := filepath.Join(job.outputDir, "index.m3u8")
	segments := filepath.Join(job.outputDir, "seg_%05d.ts")

	args := buildFFmpegArgsForInput(job.config, input, playlist, segments)

	cmd := exec.CommandContext(ctx, job.ffmpegPath, args...) // #nosec G204 -- the binary was validated before creating this job.
	setFFmpegSysProcAttr(cmd)
	cmd.Stderr = os.Stderr

	t.log.Info("transcode started",
		zap.String("media_id", job.mediaID),
		zap.String("encoder", job.encoder),
		zap.String("source", input.Source),
		zap.Float64("start_sec", input.StartSec),
	)
	t.hub.Publish("transcode", map[string]any{
		"media_id":  job.mediaID,
		"encoder":   job.encoder,
		"status":    "started",
		"start_sec": input.StartSec,
	})

	err := cmd.Run()
	if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		t.log.Warn("ffmpeg exited",
			zap.String("media_id", job.mediaID),
			zap.Float64("start_sec", input.StartSec),
			zap.Error(err),
		)
	}

	t.mu.Lock()
	// Only update the map entry if we are still the registered generation.
	if cur, ok := t.jobs[job.mediaID]; ok && cur == job {
		if err == nil && ctx.Err() == nil {
			job.completed = true
		} else {
			delete(t.jobs, job.mediaID)
		}
	}
	t.mu.Unlock()
	if err != nil || ctx.Err() != nil {
		job.cancel()
	}

	t.hub.Publish("transcode", map[string]any{
		"media_id": job.mediaID,
		"status":   "stopped",
		"duration": time.Since(job.startedAt).Seconds(),
	})
}

func (t *TranscoderService) resolveFFmpegPath() (string, error) {
	cfg := t.transcodeConfig()
	return t.resolveFFmpegPathFor(context.Background(), cfg.App.FFmpegPath, ffmpegArgSettingsFromConfig(cfg, nil).encoder)
}

type ffmpegValidationCache struct {
	configuredPath string
	path           string
	encoder        string
	size           int64
	modTime        time.Time
	checkedAt      time.Time
}

func (t *TranscoderService) resolveFFmpegPathFor(ctx context.Context, configuredPath, encoder string) (string, error) {
	t.ffmpegMu.Lock()
	defer t.ffmpegMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	configuredPath = strings.TrimSpace(configuredPath)
	cached := t.ffmpegCache
	if cached.path != "" && cached.encoder == encoder &&
		(configuredPath == cached.configuredPath || configuredPath == cached.path) &&
		time.Since(cached.checkedAt) < transcodeAvailabilityTTL {
		if info, err := os.Stat(cached.path); err == nil && info.Size() == cached.size && info.ModTime().Equal(cached.modTime) {
			return cached.path, nil
		}
	}

	var lastErr error
	for _, bin := range executableCandidates(configuredPath, "ffmpeg") {
		if err := validateFFmpegForTranscode(ctx, bin, encoder); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			lastErr = err
			continue
		}
		if info, err := os.Stat(bin); err == nil {
			t.ffmpegCache = ffmpegValidationCache{configuredPath: configuredPath, path: bin, encoder: encoder,
				size: info.Size(), modTime: info.ModTime(), checkedAt: time.Now()}
		}
		config.RuntimeMu.Lock()
		if strings.TrimSpace(t.cfg.App.FFmpegPath) == configuredPath {
			t.cfg.App.FFmpegPath = bin
		}
		config.RuntimeMu.Unlock()
		return bin, nil
	}
	if lastErr != nil {
		return "", fmt.Errorf("no usable ffmpeg found for HLS transcode: %w", lastErr)
	}
	return "", fmt.Errorf("ffmpeg not found in PATH or common local app directories; configure app.ffmpeg_path to an existing local ffmpeg")
}

func (t *TranscoderService) effectiveEncoder() string {
	cfg := t.transcodeConfig()
	if !cfg.Transcoder.HardwareAccel {
		return ""
	}
	return normalizedHardwareEncoder(cfg.Transcoder.Encoder)
}

func normalizedHardwareEncoder(encoder string) string {
	switch strings.ToLower(strings.TrimSpace(encoder)) {
	case "nvenc", "qsv", "vaapi":
		return strings.ToLower(strings.TrimSpace(encoder))
	default:
		return ""
	}
}

func validateFFmpegForTranscode(ctx context.Context, bin, encoder string) error {
	required := requiredVideoEncoder(encoder)
	out, err := commandOutput(ctx, 8*time.Second, bin, "-hide_banner", "-encoders")
	if err != nil {
		return fmt.Errorf("%s cannot list encoders: %w", bin, err)
	}
	if !hasFFmpegListEntry(string(out), required) {
		return fmt.Errorf("%s does not provide required encoder %s", bin, required)
	}

	out, err = commandOutput(ctx, 8*time.Second, bin, "-hide_banner", "-muxers")
	if err != nil || !hasFFmpegListEntry(string(out), "hls") {
		out, err = commandOutput(ctx, 8*time.Second, bin, "-hide_banner", "-formats")
		if err != nil {
			return fmt.Errorf("%s cannot list muxers/formats: %w", bin, err)
		}
	}
	if !hasFFmpegListEntry(string(out), "hls") {
		return fmt.Errorf("%s does not provide hls muxer", bin)
	}
	return nil
}

func requiredVideoEncoder(encoder string) string {
	switch encoder {
	case "nvenc":
		return "h264_nvenc"
	case "qsv":
		return "h264_qsv"
	case "vaapi":
		return "h264_vaapi"
	default:
		return "libx264"
	}
}

func hasFFmpegListEntry(output, name string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		for _, field := range fields {
			if field == name {
				return true
			}
		}
	}
	return false
}

// HumanFFmpegProfile is exposed for the admin UI / settings view.
func (t *TranscoderService) HumanFFmpegProfile() string {
	cfg := t.transcodeConfig()
	return fmt.Sprintf("ffmpeg=%s, encoder=%s, output=%s",
		cfg.App.FFmpegPath, cfg.Transcoder.Encoder, filepath.Join(t.cfg.Cache.CacheDir, "hls"))
}
