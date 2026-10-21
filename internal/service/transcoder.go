// Package service — HLS on-demand transcoder.
//
// TranscoderService spawns ffmpeg processes that segment a source media file
// into HLS (.m3u8 + .ts). Each playback scope owns a cache directory and
// every ffmpeg run writes to its own run-* subdirectory.
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
//   - Each authenticated user/player scope has at most one active ffmpeg job.
//   - jobs[jobKey] tracks the running goroutine + cancel func; mediaID remains
//     the actual media identity for database access and activity reporting.
//   - Legacy internal callers keep media-keyed behaviour.
//   - Equivalent requests within one scope reuse the current output.
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

	mu   sync.Mutex
	jobs map[string]*hlsJob
	// Start gates are reference-counted so random playback session keys do not
	// accumulate after their last starter/stopper has left.
	startGateMu sync.Mutex
	startGates  map[string]*hlsStartGate
	strmResolve func(ctx context.Context, raw string) (*StrmPlayResult, error)
	probe       *FFprobeService

	// availMu guards the cached ffmpeg availability used by Available().
	availMu        sync.Mutex
	availCheckedAt time.Time
	availValue     bool
	availKey       string
	ffmpegMu       sync.Mutex
	ffmpegCache    ffmpegValidationCache
}

// hlsJob holds the live state of one ffmpeg run.
type hlsJob struct {
	jobKey     string
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
	// seekGen is the client `_seek` token for this job. Newer gens win;
	// older/missing gens must not cancel a mid-file restart.
	seekGen int64
	// subtitleStream is an absolute ffmpeg stream index to burn into the
	// picture, or -1 when no bitmap subtitle is selected.
	subtitleStream int
	// quality is the requested local HLS quality profile ("" = global config).
	quality string
	// done is closed when the ffmpeg goroutine fully exits (after process death).
	done chan struct{}
	// Completed outputs stay reusable until idle eviction without occupying a
	// running-process slot. The binary and config are fixed for this generation.
	completed  bool
	ffmpegPath string
	config     *config.Config
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

// transcodeAvailabilityTTL bounds how long an Available() result is reused.
// Validating ffmpeg actually spawns it, and /playback is polled on every player
// load, so the answer is cached briefly. It stays short so a freshly installed
// ffmpeg becomes usable without restarting the server.
const transcodeAvailabilityTTL = 20 * time.Second

// Available reports whether HLS transcoding can run right now: transcoding must
// be enabled by configuration and a usable ffmpeg must resolve. Callers use it to
// stop advertising local HLS renditions that would only fail with a 500.
func (t *TranscoderService) Available() bool {
	if t == nil || t.cfg == nil {
		return false
	}
	cfg := t.transcodeConfig()
	if !cfg.Transcoder.Enabled {
		return false
	}
	encoder := ffmpegArgSettingsFromConfig(cfg, nil).encoder
	key := cfg.App.FFmpegPath + "\x00" + encoder
	t.availMu.Lock()
	defer t.availMu.Unlock()
	if !t.availValue && t.availKey == key && !t.availCheckedAt.IsZero() && time.Since(t.availCheckedAt) < transcodeAvailabilityTTL {
		return false
	}
	_, err := t.resolveFFmpegPathFor(context.Background(), cfg.App.FFmpegPath, encoder)
	available := err == nil
	if !available && t.log != nil {
		t.log.Debug("local HLS renditions unavailable", zap.Error(err))
	}
	t.availValue = available
	t.availCheckedAt = time.Now()
	t.availKey = key
	return available
}

func (t *TranscoderService) transcodeConfig() *config.Config {
	config.RuntimeMu.RLock()
	defer config.RuntimeMu.RUnlock()
	return &config.Config{
		App:        config.AppConfig{FFmpegPath: t.cfg.App.FFmpegPath, VAAPIDevice: t.cfg.App.VAAPIDevice},
		Transcoder: t.cfg.Transcoder,
	}
}

// HLSDir is the cache root for one internal playback job key.
func (t *TranscoderService) HLSDir(jobKey string) string {
	return filepath.Join(t.cfg.Cache.CacheDir, "hls", jobKey)
}

// PlaylistPath returns the absolute path of the m3u8 playlist for a media.
func (t *TranscoderService) PlaylistPath(jobKey string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if job := t.jobs[jobKey]; job != nil {
		return t.jobPlaylistPath(job)
	}
	return filepath.Join(t.HLSDir(jobKey), "index.m3u8")
}

// EnsureJob makes sure a transcode is running for mediaID from the start of
// the source. Prefer EnsureJobFrom when the player seeks into the middle.
func (t *TranscoderService) EnsureJob(ctx context.Context, mediaID string) (string, error) {
	return t.EnsureJobFrom(ctx, mediaID, 0, 0)
}

// EnsureJobFrom starts (or reuses) an HLS job that seeks the source to
// startSec before encoding. Reusing only happens when an active job already
// matches that offset; otherwise the previous job is cancelled and the HLS
// cache dir is wiped so the player can jump without waiting for a full
// head-to-tail transcode.
//
// seekGen is the client `_seek` query (unix ms). A newer gen replaces an older
// job; an older or missing gen must not clobber a mid-file restart — hls.js
// in-flight playlist refreshes from a destroyed player commonly arrive as
// start=0 right after a scrub and would otherwise reset playback to the head.
func (t *TranscoderService) EnsureJobFrom(ctx context.Context, mediaID string, startSec float64, seekGen int64) (string, error) {
	return t.EnsureJobFromSubtitle(ctx, mediaID, startSec, seekGen, -1)
}

// EnsureJobFromSubtitle is EnsureJobFrom with an optional bitmap subtitle
// stream. Selecting or closing a burned subtitle creates a new HLS generation.
func (t *TranscoderService) EnsureJobFromSubtitle(ctx context.Context, mediaID string, startSec float64, seekGen int64, subtitleStream int) (string, error) {
	return t.EnsureJobFromSubtitleQuality(ctx, mediaID, startSec, seekGen, subtitleStream, "")
}

// EnsureJobFromSubtitleQuality is EnsureJobFromSubtitle with an explicit local
// HLS quality profile. Empty qualityID keeps the global transcoder config.
func (t *TranscoderService) EnsureJobFromSubtitleQuality(ctx context.Context, mediaID string, startSec float64, seekGen int64, subtitleStream int, qualityID string, scopes ...HLSPlaybackScope) (string, error) {
	jobKey := hlsPlaybackJobKey(mediaID, scopes)
	cfg := t.transcodeConfig()
	if !cfg.Transcoder.Enabled {
		return "", ErrTranscodeDisabled
	}
	if startSec < 0 {
		startSec = 0
	}
	var quality *LocalHLSQuality
	if requested := strings.TrimSpace(qualityID); requested != "" {
		profile, ok := LocalHLSQualityByID(requested)
		if !ok {
			return "", fmt.Errorf("未知的 HLS 画质：%s", requested)
		}
		quality = &profile
		qualityID = profile.ID
	}
	m, err := t.repo.Media.FindByID(ctx, mediaID)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", ErrMediaNotFound
	}

	unlock := t.lockJobStart(jobKey)
	defer unlock()

	t.mu.Lock()
	if existing, ok := t.jobs[jobKey]; ok {
		reusable := true
		playlist := t.jobPlaylistPath(existing)
		if existing.completed {
			info, err := os.Stat(playlist)
			reusable = err == nil && info.Size() > 0
		}
		if reusable && sameHLSConfiguration(existing, startSec, subtitleStream, qualityID) {
			if seekGen > existing.seekGen {
				existing.seekGen = seekGen
			}
			t.touchJobLocked(jobKey)
			t.mu.Unlock()
			return playlist, nil
		}
		if reusable && !shouldReplaceHLSJobConfigurationQuality(existing, startSec, seekGen, subtitleStream, qualityID) {
			t.touchJobLocked(jobKey)
			t.mu.Unlock()
			return playlist, nil
		}
		prev := t.detachJobLocked(jobKey)
		t.mu.Unlock()
		waitJobExit(prev, 12*time.Second)
	} else {
		t.mu.Unlock()
	}

	input, err := t.resolveTranscodeInput(ctx, m)
	if err != nil {
		return "", err
	}
	input.StartSec = startSec
	if subtitleStream >= 0 {
		input.SubtitleStream = &subtitleStream
	}
	input.Quality = quality
	t.maybeFillDuration(ctx, m, input)
	settings := ffmpegArgSettingsFromConfig(cfg, quality)
	jobEncoder := settings.encoder
	if settings.noScale || subtitleStream >= 0 {
		jobEncoder = ""
	}
	bin, err := t.resolveFFmpegPathFor(ctx, cfg.App.FFmpegPath, jobEncoder)
	if err != nil {
		return "", err
	}

	rootDir := t.HLSDir(jobKey)
	// Runs use distinct directories even when Windows keeps an old segment
	// open. A delayed request can never read a newer run's seg_00000.ts.
	if err := resetHLSDir(rootDir); err != nil {
		return "", err
	}
	outDir, err := os.MkdirTemp(rootDir, "run-")
	if err != nil {
		return "", err
	}
	registered := false
	defer func() {
		if !registered {
			_ = os.RemoveAll(outDir)
			_ = os.Remove(rootDir)
		}
	}()

	t.mu.Lock()
	if max := t.maxConcurrent(); max > 0 && t.runningJobCountLocked() >= max {
		t.mu.Unlock()
		return "", ErrTranscodeBusy
	}

	jobCtx, cancel := context.WithCancel(context.Background())
	if jobEncoder == "" {
		jobEncoder = "software"
	}
	if subtitleStream >= 0 {
		jobEncoder = "software (subtitle burn-in)"
	}
	job := &hlsJob{
		jobKey:         jobKey,
		mediaID:        mediaID,
		outputDir:      outDir,
		cancel:         cancel,
		startedAt:      time.Now(),
		lastAccess:     time.Now(),
		encoder:        jobEncoder,
		startSec:       startSec,
		seekGen:        seekGen,
		subtitleStream: subtitleStream,
		quality:        qualityID,
		done:           make(chan struct{}),
		ffmpegPath:     bin,
		config:         cfg,
	}
	t.jobs[jobKey] = job
	registered = true
	t.mu.Unlock()

	helper.Go(t.log, "transcoder.monitorIdle", func() { t.monitorIdle(jobCtx, job) })
	helper.Go(t.log, "transcoder.ffmpeg", func() {
		defer close(job.done)
		t.runFFmpeg(jobCtx, job, input)
	})
	return filepath.Join(outDir, "index.m3u8"), nil
}

// shouldReplaceHLSJob reports whether an incoming playlist request may cancel
// the running job. Stale hls.js refreshes (older/missing `_seek`) must not win.
func shouldReplaceHLSJob(existing *hlsJob, startSec float64, seekGen int64) bool {
	if existing == nil {
		return true
	}
	if sameHLSStart(existing.startSec, startSec) {
		return false
	}
	return newerHLSGenerationMayReplace(existing, seekGen)
}

func shouldReplaceHLSJobConfiguration(existing *hlsJob, startSec float64, seekGen int64, subtitleStream int) bool {
	return shouldReplaceHLSJobConfigurationQuality(existing, startSec, seekGen, subtitleStream, "")
}

func shouldReplaceHLSJobConfigurationQuality(existing *hlsJob, startSec float64, seekGen int64, subtitleStream int, quality string) bool {
	if existing == nil {
		return true
	}
	if sameHLSConfiguration(existing, startSec, subtitleStream, quality) {
		return false
	}
	return newerHLSGenerationMayReplace(existing, seekGen)
}

func newerHLSGenerationMayReplace(existing *hlsJob, seekGen int64) bool {
	if seekGen > 0 && existing.seekGen > 0 && seekGen < existing.seekGen {
		return false
	}
	// Untagged request while a seek-tagged job is active: treat as stale.
	if seekGen == 0 && existing.seekGen > 0 {
		return false
	}
	return true
}

func sameHLSConfiguration(existing *hlsJob, startSec float64, subtitleStream int, quality string) bool {
	return existing != nil &&
		existing.subtitleStream == subtitleStream &&
		existing.quality == quality &&
		sameHLSStart(existing.startSec, startSec)
}

type hlsStartGate struct {
	mu    sync.Mutex
	users int
}

func (t *TranscoderService) lockJobStart(key string) func() {
	t.startGateMu.Lock()
	if t.startGates == nil {
		t.startGates = make(map[string]*hlsStartGate)
	}
	gate := t.startGates[key]
	if gate == nil {
		gate = &hlsStartGate{}
		t.startGates[key] = gate
	}
	gate.users++
	t.startGateMu.Unlock()
	gate.mu.Lock()
	return func() {
		gate.mu.Unlock()
		t.startGateMu.Lock()
		gate.users--
		if gate.users == 0 {
			delete(t.startGates, key)
		}
		t.startGateMu.Unlock()
	}
}

// detachJobLocked cancels and removes a job from the map without waiting.
// Caller must hold t.mu and must waitJobExit afterwards before wiping the HLS dir.
func (t *TranscoderService) detachJobLocked(mediaID string) *hlsJob {
	j, ok := t.jobs[mediaID]
	if !ok {
		return nil
	}
	j.cancel()
	delete(t.jobs, mediaID)
	return j
}

func waitJobExit(job *hlsJob, timeout time.Duration) {
	if job == nil || job.done == nil {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-job.done:
	case <-timer.C:
	}
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
