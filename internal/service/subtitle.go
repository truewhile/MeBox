// Package service — subtitle handling.
//
// SubtitleService finds external subtitle files next to a media file and
// exposes them as WebVTT so the browser <track> element can load them
// directly, or as the original bytes for Emby/Jellyfin clients.
//
// External-subtitle discovery rules (matching the legacy Python defaults):
//
//  1. Same directory, same basename, different extension.
//  2. Same directory, ".sub/" or "subs/" subdirectory.
//  3. Sibling languages e.g. movie.zh.srt / movie.en.srt → exposed as
//     ?lang=zh / ?lang=en.
//
// Supported extensions: .srt, .ass, .ssa, .vtt.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// SubtitleService is the discovery + conversion entry point.
type SubtitleService struct {
	log         *zap.Logger
	repo        *repository.Container
	cfg         *config.Config
	strmResolve func(ctx context.Context, raw string) (*StrmPlayResult, error)

	// 目录发现是 Emby 条目列表的热路径（每个媒体源一次 DB 查询 + 最多 5 次
	// os.ReadDir），而字幕文件极少变化：按 media_id 做短 TTL 缓存。
	cacheMu   sync.Mutex
	discovery map[string]subtitleDiscoveryEntry
}

const (
	subtitleDiscoveryTTL      = 2 * time.Minute
	subtitleDiscoveryCacheCap = 4096
)

type subtitleDiscoveryEntry struct {
	tracks    []SubtitleTrack
	expiresAt time.Time
}

// NewSubtitleService is the constructor.
func NewSubtitleService(cfg *config.Config, log *zap.Logger, repo *repository.Container) *SubtitleService {
	return &SubtitleService{log: log, repo: repo, cfg: cfg}
}

// SubtitleTrack describes one external subtitle file.
type SubtitleTrack struct {
	Lang        string `json:"lang"`
	Label       string `json:"label"`
	Path        string `json:"path"`
	URL         string `json:"url"`
	Codec       string `json:"codec"`
	Source      string `json:"source"`
	Delivery    string `json:"delivery"`
	StreamIndex int    `json:"stream_index,omitempty"`
}

// extToCodec maps the file extension to the inner codec name.
var extToCodec = map[string]string{
	".srt": "srt",
	".vtt": "vtt",
	".ass": "ass",
	".ssa": "ssa",
}

// Discover lists every external subtitle file for a media row. The URL is
// relative; the caller should prepend /api/subtitles/<media_id>?path=...
// when serializing for the frontend.
func (s *SubtitleService) Discover(ctx context.Context, mediaID string) ([]SubtitleTrack, error) {
	return s.discover(ctx, mediaID)
}

// DiscoverExternalOnly 只返回媒体旁边的外挂字幕文件，不含容器内嵌字幕轨。
// Emby 字幕接口（/Videos/:id/Subtitles/...）用。
func (s *SubtitleService) DiscoverExternalOnly(ctx context.Context, mediaID string) ([]SubtitleTrack, error) {
	tracks, err := s.discover(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	out := make([]SubtitleTrack, 0, len(tracks))
	for _, track := range tracks {
		if track.Source != "embedded" {
			out = append(out, track)
		}
	}
	return out, nil
}

func (s *SubtitleService) discover(ctx context.Context, mediaID string) ([]SubtitleTrack, error) {
	if tracks, ok := s.cachedDiscovery(mediaID); ok {
		return tracks, nil
	}
	tracks, err := s.discoverUncached(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	s.rememberDiscovery(mediaID, tracks)
	return tracks, nil
}

func (s *SubtitleService) cachedDiscovery(mediaID string) ([]SubtitleTrack, bool) {
	now := time.Now()
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entry, ok := s.discovery[mediaID]
	if !ok {
		return nil, false
	}
	if now.After(entry.expiresAt) {
		delete(s.discovery, mediaID)
		return nil, false
	}
	// 返回副本，避免调用方修改缓存内容。
	return append([]SubtitleTrack(nil), entry.tracks...), true
}

func (s *SubtitleService) rememberDiscovery(mediaID string, tracks []SubtitleTrack) {
	now := time.Now()
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.discovery == nil {
		s.discovery = make(map[string]subtitleDiscoveryEntry)
	}
	if len(s.discovery) >= subtitleDiscoveryCacheCap {
		s.discovery = make(map[string]subtitleDiscoveryEntry)
	}
	s.discovery[mediaID] = subtitleDiscoveryEntry{
		tracks:    append([]SubtitleTrack(nil), tracks...),
		expiresAt: now.Add(subtitleDiscoveryTTL),
	}
}

func (s *SubtitleService) discoverUncached(ctx context.Context, mediaID string) ([]SubtitleTrack, error) {
	m, err := s.repo.Media.FindByID(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("media not found")
	}
	dir := filepath.Dir(m.Path)
	bases := mediaSidecarBaseVariants(m.Path)
	if len(bases) == 0 {
		bases = []string{strings.TrimSuffix(filepath.Base(m.Path), filepath.Ext(m.Path))}
	}

	candidates := make([]string, 0, 16)
	candidates = append(candidates, dir)
	for _, sub := range []string{"subs", "Subs", "sub", ".sub"} {
		candidates = append(candidates, filepath.Join(dir, sub))
	}

	tracks := make([]SubtitleTrack, 0)
	for _, c := range candidates {
		entries, err := os.ReadDir(c)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			codec, ok := extToCodec[ext]
			if !ok {
				continue
			}
			fullName := strings.TrimSuffix(e.Name(), ext)
			matchedBase := ""
			if c == dir {
				for _, base := range bases {
					if strings.HasPrefix(strings.ToLower(fullName), strings.ToLower(base)) {
						matchedBase = base
						break
					}
				}
				if matchedBase == "" {
					// In the same directory we require a basename match;
					// inside subs/ subdirs we accept anything.
					continue
				}
			} else if len(bases) > 0 {
				matchedBase = bases[0]
			}
			lang := detectLang(fullName, matchedBase)
			tracks = append(tracks, SubtitleTrack{
				Lang:     lang,
				Label:    lang,
				Path:     filepath.Join(c, e.Name()),
				Codec:    codec,
				Source:   "external",
				Delivery: "webvtt",
			})
		}
	}
	embedded, err := s.discoverEmbedded(ctx, m)
	if err != nil {
		if s.log != nil {
			s.log.Debug("discover embedded subtitles failed", zap.String("media_id", mediaID), zap.Error(err))
		}
	} else {
		tracks = append(tracks, embedded...)
	}
	return tracks, nil
}

type embeddedSubtitleProbe struct {
	Streams []struct {
		Index     int    `json:"index"`
		CodecName string `json:"codec_name"`
		Tags      struct {
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"tags"`
		Disposition struct {
			Default int `json:"default"`
			Forced  int `json:"forced"`
		} `json:"disposition"`
	} `json:"streams"`
}

var imageSubtitleCodecs = map[string]bool{
	"hdmv_pgs_subtitle": true,
	"dvd_subtitle":      true,
	"dvb_subtitle":      true,
	"xsub":              true,
}

func (s *SubtitleService) discoverEmbedded(ctx context.Context, media *model.Media) ([]SubtitleTrack, error) {
	if s == nil || s.cfg == nil {
		return nil, errors.New("subtitle probe unavailable")
	}
	input, err := s.resolveInput(ctx, media)
	if err != nil {
		return nil, err
	}
	bin, err := resolveLocalExecutable(s.cfg.App.FFprobePath, "ffprobe")
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"-v", "error"}
	if headers := ffmpegHeaderText(input.Headers); headers != "" {
		args = append(args, "-headers", headers)
	}
	args = append(args,
		"-select_streams", "s",
		"-show_entries", "stream=index,codec_name:stream_tags=language,title:stream_disposition=default,forced",
		"-of", "json", input.Source,
	)
	out, err := exec.CommandContext(probeCtx, bin, args...).Output() // #nosec G204 -- executable is resolved locally and arguments do not use a shell.
	if err != nil {
		return nil, err
	}
	var probe embeddedSubtitleProbe
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, err
	}
	return subtitleTracksFromProbe(probe), nil
}

func subtitleTracksFromProbe(probe embeddedSubtitleProbe) []SubtitleTrack {
	tracks := make([]SubtitleTrack, 0, len(probe.Streams))
	for _, stream := range probe.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		lang := strings.ToLower(strings.TrimSpace(stream.Tags.Language))
		if lang == "" {
			lang = "und"
		}
		label := strings.TrimSpace(stream.Tags.Title)
		if label == "" {
			label = lang
		}
		if stream.Disposition.Forced != 0 {
			label += "（强制）"
		} else if stream.Disposition.Default != 0 {
			label += "（默认）"
		}
		delivery := "webvtt"
		if imageSubtitleCodecs[codec] {
			delivery = "burn"
		}
		sourceLabel := "（内嵌）"
		if delivery == "burn" {
			sourceLabel = "（内嵌·图片）"
		}
		tracks = append(tracks, SubtitleTrack{
			Lang:        lang,
			Label:       label + sourceLabel,
			Path:        "embedded:" + strconv.Itoa(stream.Index),
			Codec:       codec,
			Source:      "embedded",
			Delivery:    delivery,
			StreamIndex: stream.Index,
		})
	}
	return tracks
}

func (s *SubtitleService) SetStrmPlayTargetResolver(resolve func(context.Context, string) (*StrmPlayResult, error)) {
	if s != nil {
		s.strmResolve = resolve
	}
}

func (s *SubtitleService) resolveInput(ctx context.Context, media *model.Media) (transcodeInput, error) {
	if media == nil {
		return transcodeInput{}, ErrMediaNotFound
	}
	if !isStrmMediaRow(media) {
		if _, err := os.Stat(media.Path); err != nil {
			return transcodeInput{}, ErrMediaNotFound
		}
		return transcodeInput{Source: media.Path}, nil
	}
	raw := strings.TrimSpace(media.STRMURL)
	if raw == "" && strings.HasSuffix(strings.ToLower(media.Path), ".strm") {
		raw, _ = readLocalSTRMTarget(media.Path)
	}
	if s.strmResolve != nil {
		resolved, err := s.strmResolve(ctx, raw)
		if err != nil {
			return transcodeInput{}, err
		}
		return transcodeInputFromPlayResult(resolved)
	}
	if isHTTPPlaybackTarget(raw) {
		return transcodeInput{Source: raw}, nil
	}
	return transcodeInput{}, errors.New("subtitle source unavailable")
}

// langTag matches the .zh / .zh-cn / .chs language sub-extensions.
var langTag = regexp.MustCompile(`(?i)\.([a-z]{2,3}(?:[-_][a-z]{2,4})?)$`)

func detectLang(name, base string) string {
	suffix := strings.TrimPrefix(name, base)
	suffix = strings.TrimPrefix(suffix, ".")
	if m := langTag.FindStringSubmatch("." + suffix); len(m) >= 2 {
		return strings.ToLower(m[1])
	}
	if suffix == "" {
		return "und" // undetermined
	}
	return strings.ToLower(suffix)
}

// Serve writes the subtitle file as WebVTT (.vtt). SRT/SSA files are
// converted minimally on the fly. Returns ErrSubtitleNotFound when the
// path is rejected (path traversal / not in the media directory).
func (s *SubtitleService) Serve(ctx context.Context, mediaID, sub string, w io.Writer) error {
	m, err := s.repo.Media.FindByID(ctx, mediaID)
	if err != nil || m == nil {
		return errors.New("media not found")
	}
	if strings.HasPrefix(sub, "embedded:") {
		index, err := strconv.Atoi(strings.TrimPrefix(sub, "embedded:"))
		if err != nil || index < 0 {
			return errors.New("invalid embedded subtitle")
		}
		return s.serveEmbedded(ctx, m, index, w)
	}
	abs, err := filepath.Abs(sub)
	if err != nil {
		return err
	}
	mediaDir, _ := filepath.Abs(filepath.Dir(m.Path))
	if !pathWithin(abs, mediaDir) {
		return fmt.Errorf("path escape")
	}

	f, err := os.Open(abs) // #nosec G304 -- abs is constrained to the media file directory with pathWithin.
	if err != nil {
		return err
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		return err
	}

	switch strings.ToLower(filepath.Ext(abs)) {
	case ".vtt":
		_, err = w.Write(body)
	case ".srt":
		_, err = w.Write([]byte(srtToVTT(string(body))))
	case ".ass", ".ssa":
		_, err = w.Write([]byte(assToVTT(string(body))))
	default:
		return errors.New("unsupported subtitle format")
	}
	return err
}

func (s *SubtitleService) serveEmbedded(ctx context.Context, media *model.Media, streamIndex int, w io.Writer) error {
	input, err := s.resolveInput(ctx, media)
	if err != nil {
		return err
	}
	bin, err := resolveLocalExecutable(s.cfg.App.FFmpegPath, "ffmpeg")
	if err != nil {
		return err
	}
	args := []string{"-hide_banner", "-loglevel", "error"}
	args = append(args, ffmpegHTTPInputArgs(input)...)
	args = append(args, "-i", input.Source, "-map", "0:"+strconv.Itoa(streamIndex), "-f", "webvtt", "-")
	cmd := exec.CommandContext(ctx, bin, args...) // #nosec G204 -- executable is resolved locally and arguments do not use a shell.
	cmd.Stdout = w
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("extract embedded subtitle: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ServeRaw writes the subtitle file in its original format without any
// WebVTT conversion. Emby/Jellyfin clients advertise the source codec (ASS,
// subrip, etc.) in MediaStreams, then fetch the subtitle bytes via the
// DeliveryUrl and parse them with a decoder matching that codec — so the bytes
// must be the unmodified source, not a conversion. Unlike Serve (used by the
// browser <track> path, which requires WebVTT), ServeRaw preserves the file
// exactly as-is. Same path-safety constraints as Serve.
func (s *SubtitleService) ServeRaw(ctx context.Context, mediaID, sub string, w io.Writer) error {
	m, err := s.repo.Media.FindByID(ctx, mediaID)
	if err != nil || m == nil {
		return errors.New("media not found")
	}
	abs, err := filepath.Abs(sub)
	if err != nil {
		return err
	}
	mediaDir, _ := filepath.Abs(filepath.Dir(m.Path))
	if !pathWithin(abs, mediaDir) {
		return fmt.Errorf("path escape")
	}
	f, err := os.Open(abs) // #nosec G304 -- abs is constrained to the media file directory with pathWithin.
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}
