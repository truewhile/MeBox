package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service/cloud"
	"github.com/truewhile/MeBox/internal/service/cloud115"
)

var (
	ErrCloud115TranscodePending = errors.New("115 cloud transcode pending")
	ErrCloud115NotApplicable    = errors.New("115 cloud playback not applicable")
)

// Cloud115TranscodeState 描述当前请求清晰度的云端转码状态。
type Cloud115TranscodeState struct {
	State         string `json:"state"` // idle / ready / transcoding / unavailable
	Definition    string `json:"definition,omitempty"`
	Message       string `json:"message,omitempty"`
	RetryAfterSec int    `json:"retry_after_sec,omitempty"`
	StartedAt     int64  `json:"started_at,omitempty"`
}

// PlaybackInfo 是播放器加载时获取的统一播放能力描述。
type PlaybackInfo struct {
	MediaID        string                 `json:"media_id"`
	Provider       string                 `json:"provider"`
	Fallback       []string               `json:"fallback"`
	DefaultQuality string                 `json:"default_quality"`
	CloudQualities []PlaybackQuality      `json:"cloud_qualities,omitempty"`
	LocalQualities []PlaybackQuality      `json:"local_qualities"`
	Transcode      Cloud115TranscodeState `json:"transcode"`
}

type cloud115PushAttempt struct {
	at      time.Time
	success bool
	message string
}

// Cloud115PlaybackService 负责 115 云端播放、清晰度列表和云端转码触发。
// 实际的 HLS 代理在 Cloud115HLSProxy 中。
type Cloud115PlaybackService struct {
	cfg  *config.Config
	log  *zap.Logger
	repo *repository.Container
	strm *StrmService

	// transcoder 用于判断本地 HLS 档位此刻是否真的可用；未注入时按"不可用"处理，
	// 避免向播放器推荐必然 500 的转码档位。
	transcoder *TranscoderService

	mu        sync.Mutex
	pushState map[string]cloud115PushAttempt

	proxy *Cloud115HLSProxy
}

// SetTranscoder 注入转码服务，用于标注本地 HLS 档位的可用性。
func (s *Cloud115PlaybackService) SetTranscoder(transcoder *TranscoderService) *Cloud115PlaybackService {
	if s != nil {
		s.transcoder = transcoder
	}
	return s
}

// localTranscodeAvailable 报告本地 HLS 此刻是否可用。
func (s *Cloud115PlaybackService) localTranscodeAvailable() bool {
	if s == nil || s.transcoder == nil {
		return false
	}
	return s.transcoder.Available()
}

func NewCloud115PlaybackService(cfg *config.Config, log *zap.Logger, repo *repository.Container, strm *StrmService) *Cloud115PlaybackService {
	svc := &Cloud115PlaybackService{
		cfg:       cfg,
		log:       log,
		repo:      repo,
		strm:      strm,
		pushState: make(map[string]cloud115PushAttempt),
	}
	svc.proxy = newCloud115HLSProxy(svc)
	return svc
}

// HLSProxy 返回 115 云 HLS 反向代理。
func (s *Cloud115PlaybackService) HLSProxy() *Cloud115HLSProxy {
	if s == nil {
		return nil
	}
	return s.proxy
}

// PlaybackInfo 返回媒体在 direct/115-cloud/local 三种模式下的能力描述。
//
// requestedDefinition 为空或 0 时默认选择 1080P；该档位不可用时返回
// transcoding 状态（并只触发一次加速转码，避免轮询风暴）。
func (s *Cloud115PlaybackService) PlaybackInfo(ctx context.Context, mediaID string, requestedDefinition int) (*PlaybackInfo, error) {
	if s == nil || s.repo == nil || s.repo.Media == nil {
		return nil, ErrMediaNotFound
	}
	m, err := s.repo.Media.FindByID(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, ErrMediaNotFound
	}

	provider := MediaPlaybackProvider(m)
	localQualities := LocalQualityOptions(m, s.localTranscodeAvailable())
	localDefault := DefaultLocalHLSQualityID(m)
	info := &PlaybackInfo{
		MediaID:        m.ID,
		Provider:       provider,
		DefaultQuality: localDefault,
		LocalQualities: localQualities,
		Transcode:      Cloud115TranscodeState{State: "idle"},
	}
	if provider != model.StrmProvider115 {
		info.Fallback = []string{"direct", "local_hls"}
		return info, nil
	}

	account, pickCode, client, err := s.resolve115Target(ctx, m)
	if err != nil {
		info.Fallback = []string{"direct", "local_hls"}
		info.Transcode = Cloud115TranscodeState{State: "unavailable", Message: err.Error()}
		return info, nil
	}

	resp, data, playErr := client.GetVideoPlayInfo(ctx, pickCode, cloud115.DefaultUA)
	if cloud115PlayInfoFatal(resp, playErr) {
		info.Fallback = []string{"direct", "local_hls"}
		info.Transcode = Cloud115TranscodeState{
			State:   "unavailable",
			Message: cloud115PlayErrorMessage(playErr, resp),
		}
		return info, nil
	}

	cloudQualities := Cloud115QualityOptions(m, data)
	info.CloudQualities = cloudQualities
	info.Fallback = []string{"direct", "cloud_hls", "local_hls"}
	info.DefaultQuality = DefaultCloud115Quality(cloudQualities)

	requested := strings.TrimSpace(strconv.Itoa(requestedDefinition))
	if requestedDefinition <= 0 || requested == "0" {
		requested = info.DefaultQuality
	}
	selected, ok := findPlaybackQuality(cloudQualities, requested)
	if !ok {
		info.Transcode = Cloud115TranscodeState{
			State:      "unavailable",
			Definition: requested,
			Message:    "115 不支持该清晰度",
		}
		return info, nil
	}
	if selected.Source == "original" {
		info.Transcode = Cloud115TranscodeState{State: "ready", Definition: selected.ID}
		return info, nil
	}
	if selected.Available {
		info.Transcode = Cloud115TranscodeState{State: "ready", Definition: selected.ID}
		return info, nil
	}

	info.Transcode = s.ensureCloudTranscode(ctx, account.ID, pickCode, selected, client)
	return info, nil
}

// StartTranscode 显式触发某个清晰度的云端转码，返回最新状态。
func (s *Cloud115PlaybackService) StartTranscode(ctx context.Context, mediaID string, definition int) (*PlaybackInfo, error) {
	return s.PlaybackInfo(ctx, mediaID, definition)
}

// ResolveCloud115URL 返回指定清晰度的 115 云端 m3u8 地址。
// 档位尚未转码完成时返回 ErrCloud115TranscodePending。
func (s *Cloud115PlaybackService) ResolveCloud115URL(ctx context.Context, mediaID string, definition int) (string, *cloud115.VideoPlayData, error) {
	m, err := s.repo.Media.FindByID(ctx, mediaID)
	if err != nil {
		return "", nil, err
	}
	if m == nil {
		return "", nil, ErrMediaNotFound
	}
	if MediaPlaybackProvider(m) != model.StrmProvider115 {
		return "", nil, ErrCloud115NotApplicable
	}
	_, pickCode, client, err := s.resolve115Target(ctx, m)
	if err != nil {
		return "", nil, err
	}
	resp, data, playErr := client.GetVideoPlayInfo(ctx, pickCode, cloud115.DefaultUA)
	if cloud115PlayInfoFatal(resp, playErr) {
		return "", data, fmt.Errorf("115 云端播放不可用：%s", cloud115PlayErrorMessage(playErr, resp))
	}
	requested := definition
	if requested <= 0 {
		requested = 4
	}
	item := findVideoURL(data, requested)
	if item == nil || strings.TrimSpace(item.URL) == "" {
		return "", data, ErrCloud115TranscodePending
	}
	return strings.TrimSpace(item.URL), data, nil
}

func (s *Cloud115PlaybackService) ensureCloudTranscode(
	ctx context.Context,
	accountID, pickCode string,
	quality PlaybackQuality,
	client *cloud115.OpenClient,
) Cloud115TranscodeState {
	key := accountID + ":" + pickCode
	now := time.Now()

	s.mu.Lock()
	if last, ok := s.pushState[key]; ok {
		ttl := 2 * time.Minute
		if last.success {
			ttl = 3 * time.Hour
		}
		if now.Sub(last.at) < ttl {
			s.mu.Unlock()
			return Cloud115TranscodeState{
				State:         "transcoding",
				Definition:    quality.ID,
				Message:       last.message,
				RetryAfterSec: 5,
				StartedAt:     last.at.Unix(),
			}
		}
	}
	s.mu.Unlock()

	err := client.SubmitVideoPush(ctx, pickCode, "vip_push")
	attempt := cloud115PushAttempt{at: now, success: err == nil}
	if err != nil {
		attempt.message = fmt.Sprintf("115 云端转码排队中（加速请求未成功：%s）", cloud115PlayErrorMessage(err, nil))
	} else {
		attempt.message = fmt.Sprintf("已触发 115 云端转码，正在等待 %s…", quality.Label)
	}

	s.mu.Lock()
	s.pushState[key] = attempt
	s.mu.Unlock()

	return Cloud115TranscodeState{
		State:         "transcoding",
		Definition:    quality.ID,
		Message:       attempt.message,
		RetryAfterSec: 5,
		StartedAt:     now.Unix(),
	}
}

func (s *Cloud115PlaybackService) resolve115Target(ctx context.Context, m *model.Media) (*model.StrmAccount, string, *cloud115.OpenClient, error) {
	if s == nil || s.strm == nil || s.repo == nil || s.repo.StrmAccount == nil {
		return nil, "", nil, ErrCloud115NotApplicable
	}
	raw := strings.TrimSpace(m.STRMURL)
	if raw == "" && strings.HasSuffix(strings.ToLower(strings.TrimSpace(m.Path)), ".strm") {
		parsed, err := readLocalSTRMTarget(m.Path)
		if err != nil {
			return nil, "", nil, err
		}
		raw = strings.TrimSpace(parsed)
	}
	if raw == "" {
		return nil, "", nil, fmt.Errorf("缺少 STRM 播放目标")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", nil, err
	}
	if !strings.Contains(strings.ToLower(u.Path), "/api/strm/play/cloud115/") &&
		!strings.Contains(strings.ToLower(u.Path), "/api/cloud/play/cloud115") {
		return nil, "", nil, ErrCloud115NotApplicable
	}
	accountID := strings.TrimSpace(u.Query().Get("acct"))
	if accountID == "" {
		return nil, "", nil, fmt.Errorf("缺少 115 账号 ID")
	}
	pickCode := strings.TrimSpace(u.Query().Get("pickcode"))
	if pickCode == "" {
		return nil, "", nil, fmt.Errorf("缺少 115 pickcode")
	}
	account, err := s.repo.StrmAccount.FindByID(ctx, accountID)
	if err != nil {
		return nil, "", nil, err
	}
	if account == nil || !account.Enabled {
		return nil, "", nil, fmt.Errorf("115 账号不存在或已禁用")
	}
	if account.Provider != model.StrmProvider115 {
		return nil, "", nil, ErrCloud115NotApplicable
	}
	provider, err := s.strm.providerFor(ctx, account)
	if err != nil {
		return nil, "", nil, err
	}
	openProvider, ok := provider.(cloud.OpenAPI115Provider)
	if !ok || openProvider.OpenClient() == nil {
		return nil, "", nil, ErrCloud115NotApplicable
	}
	return account, pickCode, openProvider.OpenClient(), nil
}

// MediaPlaybackProvider 返回媒体行的播放提供方（用于前端降级顺序）。
func MediaPlaybackProvider(m *model.Media) string {
	if m == nil {
		return model.StrmProviderLocal
	}
	raw := strings.TrimSpace(m.STRMURL)
	if raw == "" && strings.HasSuffix(strings.ToLower(strings.TrimSpace(m.Path)), ".strm") {
		if target, err := readLocalSTRMTarget(m.Path); err == nil {
			raw = strings.TrimSpace(target)
		}
	}
	if raw != "" {
		if u, err := url.Parse(raw); err == nil {
			path := strings.ToLower(u.Path)
			if idx := strings.Index(path, "/api/strm/play/"); idx >= 0 {
				rest := strings.Trim(path[idx+len("/api/strm/play/"):], "/")
				if provider, _, ok := strings.Cut(rest, "/"); ok && provider != "" {
					return provider
				}
			}
			if idx := strings.Index(path, "/api/cloud/play/"); idx >= 0 {
				provider := strings.Trim(path[idx+len("/api/cloud/play/"):], "/")
				if provider != "" {
					return provider
				}
			}
		}
	}
	if IsStrmMediaRow(m) {
		return "strm"
	}
	return model.StrmProviderLocal
}

func findPlaybackQuality(options []PlaybackQuality, id string) (PlaybackQuality, bool) {
	id = strings.TrimSpace(id)
	for _, option := range options {
		if option.ID == id {
			return option, true
		}
	}
	return PlaybackQuality{}, false
}

func findVideoURL(data *cloud115.VideoPlayData, definition int) *cloud115.VideoURLItem {
	if data == nil || definition <= 0 {
		return nil
	}
	for i := range data.VideoURL {
		item := &data.VideoURL[i]
		if item.DefinitionN == definition || item.Definition == definition {
			return item
		}
	}
	return nil
}

func cloud115PlayInfoFatal(resp *cloud115.RespBase, err error) bool {
	if resp == nil {
		return true
	}
	if bool(resp.State) {
		return false
	}
	switch resp.Code {
	case cloud115.AccessTokenAuthFail,
		cloud115.AccessTokenExpiryCode,
		cloud115.AccessAuthInvalid,
		cloud115.AccessTokenFormatInvalid,
		cloud115.RefreshTokenInvalid,
		cloud115.TokenRefreshFail,
		cloud115.RequestMaxLimitCode,
		cloud115.RequestRateLimitCode:
		return true
	}
	// 非鉴权/限流错误一律当作“尚未转码完成”处理，继续轮询。
	_ = err
	return false
}

func cloud115PlayErrorMessage(err error, resp *cloud115.RespBase) string {
	if err != nil {
		var apiErr *cloud115.OpenAPIError
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case cloud115.AccessTokenAuthFail,
				cloud115.AccessTokenExpiryCode,
				cloud115.AccessAuthInvalid,
				cloud115.AccessTokenFormatInvalid,
				cloud115.RefreshTokenInvalid:
				return "115 授权已过期，请重新授权账号"
			}
			if strings.TrimSpace(apiErr.Message) != "" {
				return apiErr.Message
			}
		}
		return err.Error()
	}
	if resp != nil {
		if msg := strings.TrimSpace(resp.Message); msg != "" {
			return msg
		}
		if msg := strings.TrimSpace(resp.Error); msg != "" {
			return msg
		}
	}
	return "115 云端播放不可用"
}
