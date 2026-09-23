package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// 播放档案里可选的片头/片尾数据来源。
const (
	// SegmentSourceAuto 优先用文件内嵌章节（对这个片源最准），没有可用章节时
	// 回落到 TheIntroDB。
	SegmentSourceAuto = "auto"
	// SegmentSourceFFprobe 只用文件内嵌章节。
	SegmentSourceFFprobe = "ffprobe"
	// SegmentSourceTheIntroDB 只用社区数据库。它同时是 media_segments.source 的
	// 取值——两处必须一致，所以直接复用提供方的常量。
	SegmentSourceTheIntroDB = IntroDBSource
)

const (
	// mediaProbeTimeout 是一次后台探测的总预算（含把 strm 目标解析成直链）。
	mediaProbeTimeout = 90 * time.Second
	// mediaProbeFailureRetry 是探测失败后允许重新探测的间隔。失败的结果也会落库，
	// 否则每次播放都会为一个坏源重跑一次。
	mediaProbeFailureRetry = 6 * time.Hour
	// mediaProbeErrorLimit 限制落库的错误信息长度（列宽 512 字节，错误里可能
	// 带 URL，截断同时避免超长）。
	mediaProbeErrorLimit = 200
)

// NormalizeSegmentSource 把任意输入收敛到合法取值；未知值一律按 auto 处理
// （历史档案里这一列可能还是空串）。
func NormalizeSegmentSource(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case SegmentSourceFFprobe:
		return SegmentSourceFFprobe
	case SegmentSourceTheIntroDB:
		return SegmentSourceTheIntroDB
	default:
		return SegmentSourceAuto
	}
}

// mediaProber 是 MediaProbeService 需要的探测能力。抽成接口是为了在测试里注入
// 桩，避免依赖真实 ffprobe 二进制。
type mediaProber interface {
	ProbeFull(ctx context.Context, input ProbeInput) (*FullProbeResult, error)
}

// MediaProbeService 用 ffprobe 提取媒体的完整信息（容器、每路轨道、内嵌章节），
// 把结果落库缓存，并把章节映射成可跳过的片头/片尾区间。
//
// 核心约束：一次探测要 3～4 秒（远端直链更慢，跨洋要跑三次 HTTP 事务），所以
// 只允许异步跑。播放链路永远只读缓存，拿不到就下次再来——绝不能让一次探测挡在
// 起播路径上。
type MediaProbeService struct {
	log   *zap.Logger
	repo  *repository.Container
	probe mediaProber
	// resolve 把 strm 播放目标解析成最终直链（含绑定 UA 的请求头）。与转码、
	// 内嵌字幕发现走同一条换链路径，否则会踩到网盘 CDN 的防盗链 403。
	resolve func(ctx context.Context, raw, userAgent string) (*StrmPlayResult, error)

	mu       sync.Mutex
	inFlight map[string]struct{}
}

// NewMediaProbeService is the constructor.
func NewMediaProbeService(log *zap.Logger, repo *repository.Container, probe *FFprobeService) *MediaProbeService {
	svc := &MediaProbeService{
		log:      log,
		repo:     repo,
		inFlight: make(map[string]struct{}),
	}
	if probe != nil {
		svc.probe = probe
	}
	return svc
}

// SetPlayTargetResolver injects the strm → direct-link resolver.
func (s *MediaProbeService) SetPlayTargetResolver(resolve func(ctx context.Context, raw, userAgent string) (*StrmPlayResult, error)) *MediaProbeService {
	if s != nil {
		s.resolve = resolve
	}
	return s
}

// EnsureAsync 保证这部媒体的探测已排上队，并立刻返回。
//
// 已经有同一条媒体的探测在跑、或服务未配置好时返回 false；这次新排上一条返回
// true——调用方据此告诉客户端「稍后再拉一次」。
func (s *MediaProbeService) EnsureAsync(m *model.Media) bool {
	if s == nil || s.probe == nil || s.repo == nil || m == nil || strings.TrimSpace(m.ID) == "" {
		return false
	}
	if !s.reserve(m.ID) {
		return false
	}
	// 复制一份媒体行：调用方的对象可能属于请求作用域，后台协程不该继续引用它。
	snapshot := *m
	helper.Go(s.log, "service.mediaProbe", func() {
		defer s.release(snapshot.ID)
		s.run(context.Background(), &snapshot)
	})
	return true
}

func (s *MediaProbeService) reserve(mediaID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight == nil {
		s.inFlight = make(map[string]struct{})
	}
	if _, running := s.inFlight[mediaID]; running {
		return false
	}
	s.inFlight[mediaID] = struct{}{}
	return true
}

func (s *MediaProbeService) release(mediaID string) {
	s.mu.Lock()
	delete(s.inFlight, mediaID)
	s.mu.Unlock()
}

// run 执行一次探测并落库。它跑在后台，没有调用方能接收错误，所以任何失败都只
// 记录、不外抛。
func (s *MediaProbeService) run(ctx context.Context, m *model.Media) {
	ctx, cancel := context.WithTimeout(ctx, mediaProbeTimeout)
	defer cancel()

	input, err := s.probeInput(ctx, m)
	if err != nil {
		s.markFailure(ctx, m.ID, err)
		return
	}
	result, err := s.probe.ProbeFull(ctx, input)
	if err != nil {
		s.markFailure(ctx, m.ID, err)
		return
	}
	if err := s.persistProbe(ctx, m, result); err != nil {
		s.markFailure(ctx, m.ID, err)
	}
}

// probeInput 把媒体行解析成 ffprobe 能直接打开的输入。
//
// 本地文件给路径；STRM / 云盘先解析成最终直链并带上绑定的请求头——直链与 UA
// 必须配套，用错会被 CDN 拒绝。
func (s *MediaProbeService) probeInput(ctx context.Context, m *model.Media) (ProbeInput, error) {
	if m == nil {
		return ProbeInput{}, ErrMediaNotFound
	}
	if !isStrmMediaRow(m) {
		if _, err := os.Stat(m.Path); err != nil {
			return ProbeInput{}, ErrMediaNotFound
		}
		return ProbeInput{Source: m.Path}, nil
	}
	raw := strings.TrimSpace(m.STRMURL)
	if raw == "" && strings.HasSuffix(strings.ToLower(strings.TrimSpace(m.Path)), ".strm") {
		parsed, err := readLocalSTRMTarget(m.Path)
		if err == nil {
			raw = strings.TrimSpace(parsed)
		}
	}
	if raw == "" {
		return ProbeInput{}, errors.New("strm play target missing")
	}
	if s.resolve != nil {
		resolved, err := s.resolve(ctx, raw, "")
		if err != nil {
			return ProbeInput{}, err
		}
		in, err := transcodeInputFromPlayResult(resolved)
		if err != nil {
			return ProbeInput{}, err
		}
		return ProbeInput{Source: in.Source, Headers: in.Headers}, nil
	}
	if isHTTPPlaybackTarget(raw) {
		return ProbeInput{Source: raw}, nil
	}
	return ProbeInput{}, errors.New("strm probe source unavailable")
}

// persistProbe 把一次成功的探测落库：先写片段区间，再写探测行。
//
// 顺序很重要：探测行是「已经探过」的标记，客户端靠它决定要不要继续轮询。先写
// 它会让客户端在区间还没落库时就停止等待。
func (s *MediaProbeService) persistProbe(ctx context.Context, m *model.Media, result *FullProbeResult) error {
	if s.repo == nil || s.repo.MediaProbe == nil || s.repo.MediaSegment == nil {
		return errors.New("media probe repository not wired")
	}
	rows := chaptersToSegments(result.Chapters)
	for i := range rows {
		rows[i].MediaID = m.ID
		rows[i].SeriesID = m.SeriesID
	}
	if err := s.repo.MediaSegment.ReplaceForMedia(ctx, m.ID, SegmentSourceFFprobe, rows); err != nil {
		return err
	}
	payload, err := result.PayloadJSON()
	if err != nil {
		return err
	}
	row := &model.MediaProbe{
		MediaID:         m.ID,
		Signature:       mediaProbeSignature(m),
		Source:          mediaProbeInputKind(m),
		Container:       result.Container,
		DurationSec:     result.DurationSec,
		BitRate:         result.BitRate,
		Width:           firstStreamDimension(result, "video", true),
		Height:          firstStreamDimension(result, "video", false),
		VideoCodec:      firstStreamCodec(result, "video"),
		AudioCodec:      firstStreamCodec(result, "audio"),
		VideoStreams:    len(result.StreamsOfType("video")),
		AudioStreams:    len(result.StreamsOfType("audio")),
		SubtitleStreams: len(result.StreamsOfType("subtitle")),
		ChapterCount:    len(result.Chapters),
		Payload:         payload,
		ProbedAt:        time.Now(),
	}
	if err := s.repo.MediaProbe.Upsert(ctx, row); err != nil {
		return err
	}
	s.backfillDuration(ctx, m, result.DurationSec)
	return nil
}

// backfillDuration 把探测到的时长补进 media.duration_sec。STRM / 云盘媒体在扫描
// 阶段拿不到时长，而末段区间（end_ms = 0）要靠它才能换算出真实结束时间。
func (s *MediaProbeService) backfillDuration(ctx context.Context, m *model.Media, durationSec int) {
	if s.repo == nil || s.repo.DB == nil || m == nil || durationSec <= 0 || m.DurationSec > 0 {
		return
	}
	err := s.repo.DB.WithContext(ctx).Model(&model.Media{}).
		Where("id = ? AND duration_sec <= 0", m.ID).
		Update("duration_sec", durationSec).Error
	if err != nil && s.log != nil {
		s.log.Debug("backfill probed duration failed", zap.String("media_id", m.ID), zap.Error(err))
	}
}

func (s *MediaProbeService) markFailure(ctx context.Context, mediaID string, probeErr error) {
	if s == nil || s.repo == nil || probeErr == nil {
		return
	}
	message := truncateProbeError(probeErr)
	if err := s.repo.MediaProbe.MarkFailure(ctx, mediaID, message, time.Now()); err != nil && s.log != nil {
		s.log.Debug("record media probe failure failed", zap.String("media_id", mediaID), zap.Error(err))
	}
	if s.log != nil {
		s.log.Debug("media probe failed", zap.String("media_id", mediaID), zap.Error(probeErr))
	}
}

// truncateProbeError 限制错误信息长度。错误里可能带被拒绝的直链，落库时截断，
// 避免超长并减少敏感内容。
func truncateProbeError(err error) string {
	message := strings.TrimSpace(err.Error())
	runes := []rune(message)
	if len(runes) > mediaProbeErrorLimit {
		return string(runes[:mediaProbeErrorLimit])
	}
	return message
}

// mediaProbeInputKind 记录输入形态，供详情页判断「这个时长是本地读的还是远端读的」。
func mediaProbeInputKind(m *model.Media) string {
	if m == nil {
		return ""
	}
	if isStrmMediaRow(m) {
		return "strm"
	}
	return "local"
}

// mediaProbeSignature 是「探的是哪个文件」的指纹。
//
// 本地文件用路径 + 大小 + 修改时间；STRM / 云盘没有本地文件，用固化的播放目标，
// 且绝不能用解析后的直链（每次签名都不同，缓存会永远失效）。整体做哈希：
// 既固定长度，也不把路径或 pickcode 再抄一份进数据库。
func mediaProbeSignature(m *model.Media) string {
	if m == nil {
		return ""
	}
	var base string
	if isStrmMediaRow(m) {
		target := strings.TrimSpace(m.STRMURL)
		if target == "" {
			target = strings.TrimSpace(m.Path)
		}
		base = "strm|" + target
	} else if info, err := os.Stat(m.Path); err == nil {
		base = fmt.Sprintf("local|%s|%d|%d", m.Path, info.Size(), info.ModTime().Unix())
	} else {
		base = "local|" + m.Path
	}
	sum := sha256.Sum256([]byte(base))
	return hex.EncodeToString(sum[:16])
}

func firstStreamCodec(result *FullProbeResult, kind string) string {
	if result == nil {
		return ""
	}
	for _, stream := range result.Streams {
		if stream.Type == kind && stream.Codec != "" {
			return stream.Codec
		}
	}
	return ""
}

// firstStreamDimension 取第一路指定类型轨道的宽（width=true）或高。
func firstStreamDimension(result *FullProbeResult, kind string, width bool) int {
	if result == nil {
		return 0
	}
	for _, stream := range result.Streams {
		if stream.Type != kind {
			continue
		}
		if width {
			return stream.Width
		}
		return stream.Height
	}
	return 0
}
