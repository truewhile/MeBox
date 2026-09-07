// Package service — HLS on-demand transcoder.
//
// TranscoderService spawns ffmpeg processes that segment a source media file
// into HLS (.m3u8 + .ts). The output lives under cache.cache_dir/hls/<id>.
// The HTTP layer serves these files directly with a normal http.FileServer.
//
// Encoder selection (read once at startup from the config):
//
//	transcoder.encoder = "" | "nvenc" | "qsv" | "vaapi"
//
//	""      software libx264 (default; runs anywhere)
//	nvenc   h264_nvenc      (NVIDIA GPU, requires --gpus all on Docker)
//	qsv     h264_qsv        (Intel iGPU, requires /dev/dri:/dev/dri)
//	vaapi   h264_vaapi      (Mesa/Intel VAAPI, requires /dev/dri:/dev/dri
//	                         plus the kernel module loaded)
//
// Concurrency model:
//   - Each Media has at most one active ffmpeg job.
//   - jobs[mediaID] tracks the running goroutine + cancel func.
//   - Calling Start while a job already exists is a no-op.
//   - When the playlist file appears on disk we consider the job "ready"
//     and unblock the HTTP handler that was waiting on it.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// TranscoderService orchestrates background ffmpeg transcodes.
type TranscoderService struct {
	cfg  *config.Config
	log  *zap.Logger
	repo *repository.Container
	hub  *Hub

	mu          sync.Mutex
	jobs        map[string]*hlsJob
	strmResolve func(ctx context.Context, raw string) (*StrmPlayResult, error)
	probe       *FFprobeService
}

// hlsJob holds the live state of one ffmpeg run.
type hlsJob struct {
	mediaID    string
	outputDir  string
	cancel     context.CancelFunc
	startedAt  time.Time
	lastAccess time.Time
	playlistOK bool
	encoder    string
	// startSec is the source seek offset fed to ffmpeg (-ss). The HLS
	// playlist itself always starts at t=0 for that session.
	startSec float64
}

var (
	// ErrTranscodeDisabled is returned when HLS transcoding is globally disabled.
	ErrTranscodeDisabled = errors.New("transcode disabled")
	// ErrTranscodeBusy is returned when the server has reached its configured
	// ffmpeg concurrency limit.
	ErrTranscodeBusy = errors.New("transcode concurrency limit reached")
)

// NewTranscoderService is the constructor.
func NewTranscoderService(cfg *config.Config, log *zap.Logger, repo *repository.Container, hub *Hub) *TranscoderService {
	return &TranscoderService{
		cfg:  cfg,
		log:  log,
		repo: repo,
		hub:  hub,
		jobs: make(map[string]*hlsJob),
	}
}

// HLSDir is the per-media directory that holds index.m3u8 + segment files.
func (t *TranscoderService) HLSDir(mediaID string) string {
	return filepath.Join(t.cfg.Cache.CacheDir, "hls", mediaID)
}

// PlaylistPath returns the absolute path of the m3u8 playlist for a media.
func (t *TranscoderService) PlaylistPath(mediaID string) string {
	return filepath.Join(t.HLSDir(mediaID), "index.m3u8")
}

// EnsureJob makes sure a transcode is running for mediaID from the start of
// the source. Prefer EnsureJobFrom when the player seeks into the middle.
func (t *TranscoderService) EnsureJob(ctx context.Context, mediaID string) (string, error) {
	return t.EnsureJobFrom(ctx, mediaID, 0)
}

// EnsureJobFrom starts (or reuses) an HLS job that seeks the source to
// startSec before encoding. Reusing only happens when an active job already
// matches that offset; otherwise the previous job is cancelled and the HLS
// cache dir is wiped so the player can jump without waiting for a full
// head-to-tail transcode.
func (t *TranscoderService) EnsureJobFrom(ctx context.Context, mediaID string, startSec float64) (string, error) {
	if !t.cfg.Transcoder.Enabled {
		return "", ErrTranscodeDisabled
	}
	if startSec < 0 {
		startSec = 0
	}
	m, err := t.repo.Media.FindByID(ctx, mediaID)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", ErrMediaNotFound
	}

	t.mu.Lock()
	if existing, ok := t.jobs[mediaID]; ok {
		if sameHLSStart(existing.startSec, startSec) {
			t.touchJobLocked(mediaID)
			t.mu.Unlock()
			return t.PlaylistPath(mediaID), nil
		}
		existing.cancel()
		delete(t.jobs, mediaID)
	}
	t.mu.Unlock()

	input, err := t.resolveTranscodeInput(ctx, m)
	if err != nil {
		return "", err
	}
	input.StartSec = startSec
	t.maybeFillDuration(ctx, m, input)
	if _, err := t.resolveFFmpegPath(); err != nil {
		return "", err
	}

	outDir := t.HLSDir(mediaID)
	// Wipe prior segments so a mid-file restart cannot serve stale early chunks.
	if err := resetHLSDir(outDir); err != nil {
		return "", err
	}

	t.mu.Lock()
	if existing, ok := t.jobs[mediaID]; ok {
		if sameHLSStart(existing.startSec, startSec) {
			t.touchJobLocked(mediaID)
			t.mu.Unlock()
			return t.PlaylistPath(mediaID), nil
		}
		existing.cancel()
		delete(t.jobs, mediaID)
	}
	if max := t.maxConcurrent(); max > 0 && len(t.jobs) >= max {
		t.mu.Unlock()
		return "", ErrTranscodeBusy
	}

	jobCtx, cancel := context.WithCancel(context.Background())
	job := &hlsJob{
		mediaID:    mediaID,
		outputDir:  outDir,
		cancel:     cancel,
		startedAt:  time.Now(),
		lastAccess: time.Now(),
		encoder:    t.effectiveEncoder(),
		startSec:   startSec,
	}
	t.jobs[mediaID] = job
	t.mu.Unlock()

	helper.Go(t.log, "transcoder.monitorIdle", func() { t.monitorIdle(jobCtx, job) })
	helper.Go(t.log, "transcoder.ffmpeg", func() { t.runFFmpeg(jobCtx, job, input) })
	return t.PlaylistPath(mediaID), nil
}

func sameHLSStart(a, b float64) bool {
	const tol = 0.75
	if a < b {
		return b-a < tol
	}
	return a-b < tol
}

func resetHLSDir(dir string) error {
	var lastErr error
	for i := 0; i < 6; i++ {
		lastErr = os.RemoveAll(dir)
		if lastErr == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// Best-effort: if RemoveAll kept failing on Windows locks, at least drop the playlist
	// so WaitReady cannot treat the stale file as belonging to the new job.
	_ = os.Remove(filepath.Join(dir, "index.m3u8"))
	return nil
}

// SetStrmPlayTargetResolver wires STRM URL resolution so ffmpeg can transcode
// remote .strm media (HTTP 直链 or local source path) after direct play fails.
func (t *TranscoderService) SetStrmPlayTargetResolver(resolve func(ctx context.Context, raw string) (*StrmPlayResult, error)) {
	if t == nil {
		return
	}
	t.strmResolve = resolve
}

// SetProbe wires ffprobe so STRM/HLS jobs can persist source duration when the
// media row still has duration_sec=0 (common for .strm that was never probed).
func (t *TranscoderService) SetProbe(probe *FFprobeService) {
	if t == nil {
		return
	}
	t.probe = probe
}

func (t *TranscoderService) maybeFillDuration(ctx context.Context, m *model.Media, input transcodeInput) {
	if t == nil || t.probe == nil || m == nil || m.DurationSec > 0 || strings.TrimSpace(input.Source) == "" {
		return
	}
	var (
		res *ProbeResult
		err error
	)
	if isHTTPSource(input.Source) {
		res, err = t.probe.ProbeHTTP(ctx, input.Source, input.Headers)
	} else {
		res, err = t.probe.Probe(ctx, input.Source)
	}
	if err != nil || res == nil || res.DurationSec <= 0 {
		return
	}
	m.DurationSec = res.DurationSec
	if t.repo == nil || t.repo.DB == nil {
		return
	}
	if err := t.repo.DB.WithContext(ctx).Model(&model.Media{}).Where("id = ?", m.ID).Update("duration_sec", res.DurationSec).Error; err != nil && t.log != nil {
		t.log.Debug("persist probed duration failed", zap.String("media_id", m.ID), zap.Error(err))
	}
}

func (t *TranscoderService) resolveTranscodeInput(ctx context.Context, m *model.Media) (transcodeInput, error) {
	if m == nil {
		return transcodeInput{}, ErrMediaNotFound
	}
	if !isStrmMediaRow(m) {
		if _, err := os.Stat(m.Path); err != nil {
			return transcodeInput{}, ErrMediaNotFound
		}
		return transcodeInput{Source: m.Path}, nil
	}
	raw := strings.TrimSpace(m.STRMURL)
	if raw == "" && strings.HasSuffix(strings.ToLower(strings.TrimSpace(m.Path)), ".strm") {
		parsed, err := readLocalSTRMTarget(m.Path)
		if err != nil || strings.TrimSpace(parsed) == "" {
			return transcodeInput{}, fmt.Errorf("strm play target missing")
		}
		raw = parsed
	}
	if raw == "" {
		return transcodeInput{}, fmt.Errorf("strm play target missing")
	}
	if t != nil && t.strmResolve != nil {
		src, err := t.strmResolve(ctx, raw)
		if err != nil {
			return transcodeInput{}, err
		}
		return transcodeInputFromPlayResult(src)
	}
	if isHTTPPlaybackTarget(raw) {
		return transcodeInput{Source: raw}, nil
	}
	return transcodeInput{}, fmt.Errorf("strm transcode source unavailable")
}

func transcodeInputFromPlayResult(src *StrmPlayResult) (transcodeInput, error) {
	if src == nil {
		return transcodeInput{}, fmt.Errorf("strm transcode source unavailable")
	}
	if path := strings.TrimSpace(src.LocalPath); path != "" {
		if _, err := os.Stat(path); err != nil {
			return transcodeInput{}, ErrMediaNotFound
		}
		return transcodeInput{Source: path}, nil
	}
	if url := strings.TrimSpace(src.RedirectURL); url != "" {
		in := transcodeInput{Source: url}
		if src.Link != nil {
			in.Headers = src.Link.Headers
		}
		return in, nil
	}
	if src.Link != nil && strings.TrimSpace(src.Link.URL) != "" {
		return transcodeInput{Source: src.Link.URL, Headers: src.Link.Headers}, nil
	}
	return transcodeInput{}, fmt.Errorf("strm transcode source unavailable")
}
