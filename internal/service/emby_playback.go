package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

// PlaybackInfo returns a PlaybackInfoResponse usable by Emby clients.
// 远程 Emby 条目直接转发远程 PlaybackInfo，并按账号 proxy_play 配置决定
// 播放地址指向远程（直连）还是 MeBox 本地代理端点。
func (e *EmbyService) PlaybackInfo(ctx context.Context, mediaID, userID string) (map[string]any, error) {
	if e.remote != nil && IsEmbyRemoteID(mediaID) {
		mountID, remoteID, _ := DecodeEmbyRemoteID(mediaID)
		mount, acct, err := e.remote.ResolveMount(ctx, mountID)
		if err != nil {
			return nil, ErrEmbyRemoteNotFound
		}
		if !EmbyMountLibraryAllowed(e.mediaVisibility(ctx, userID), mount) {
			return nil, ErrEmbyRemoteNotFound
		}
		out, err := e.remote.RemotePlaybackInfo(ctx, mount, acct, remoteID, userID)
		if err != nil {
			return nil, err
		}
		if out == nil {
			return nil, ErrEmbyRemoteNotFound
		}
		if err := e.mergeRemoteUserData(ctx, userID, out); err != nil {
			return nil, err
		}
		out["PlaySessionId"] = fmt.Sprintf("remote-%s-%d", mountID, time.Now().UnixMilli())
		return out, nil
	}
	m, err := e.playableMedia(ctx, mediaID, userID)
	if err != nil || m == nil {
		return nil, err
	}
	return map[string]any{
		"MediaSources":  e.mediaSourcesForItem(ctx, m, false, e.directPlayOnly(ctx)),
		"PlaySessionId": fmt.Sprintf("%s-%d", m.ID, time.Now().UnixMilli()),
	}, nil
}

// ErrEmbyRemoteNotFound 表示伪装 ID 对应的远程挂载账号不存在/已禁用。
var ErrEmbyRemoteNotFound = fmt.Errorf("remote emby account not found")

// RemoteAccountByID 供 handler 层解码伪装 ID 后获取远程账号。
func (e *EmbyService) RemoteAccountByID(ctx context.Context, accountID string) *model.StrmAccount {
	if e == nil || e.remote == nil {
		return nil
	}
	return e.remote.AccountByID(ctx, accountID)
}

// ProxyRemoteVideoStream 反向代理远程 Emby 视频流（保留 Range）。
func (e *EmbyService) ProxyRemoteVideoStream(ctx context.Context, w http.ResponseWriter, r *http.Request, mountID, remoteID string) error {
	if e == nil || e.remote == nil {
		return ErrEmbyRemoteNotFound
	}
	_, acct, err := e.remote.ResolveMount(ctx, mountID)
	if err != nil {
		return ErrEmbyRemoteNotFound
	}
	return e.remote.ProxyVideoStream(ctx, w, r, acct, remoteID)
}

// ProxyRemoteSubtitle 反向代理远程 Emby 字幕流。
func (e *EmbyService) ProxyRemoteSubtitle(ctx context.Context, w http.ResponseWriter, r *http.Request, mountID, remoteID, index string) error {
	if e == nil || e.remote == nil {
		return ErrSubtitleNotFound
	}
	_, acct, err := e.remote.ResolveMount(ctx, mountID)
	if err != nil {
		return ErrSubtitleNotFound
	}
	return e.remote.ProxySubtitle(ctx, w, r, acct, remoteID, index)
}

// ProxyRemoteSetPlayed 把已看/未看状态透传到远程 Emby。
func (e *EmbyService) ProxyRemoteSetPlayed(ctx context.Context, mountID, remoteID string, played bool) error {
	if e == nil || e.remote == nil {
		return ErrEmbyRemoteNotFound
	}
	_, acct, err := e.remote.ResolveMount(ctx, mountID)
	if err != nil {
		return ErrEmbyRemoteNotFound
	}
	return e.remote.ProxySetPlayed(ctx, acct, remoteID, played)
}

// ProxyRemoteSetFavorite 把收藏/取消收藏状态透传到远程 Emby。
func (e *EmbyService) ProxyRemoteSetFavorite(ctx context.Context, mountID, remoteID string, favorite bool) error {
	if e == nil || e.remote == nil {
		return ErrEmbyRemoteNotFound
	}
	_, acct, err := e.remote.ResolveMount(ctx, mountID)
	if err != nil {
		return ErrEmbyRemoteNotFound
	}
	return e.remote.ProxySetFavorite(ctx, acct, remoteID, favorite)
}

// ServeSubtitleStream resolves the Emby /Videos/:id/Subtitles/:index/Stream
// request to one of the media's sideloaded external subtitle tracks and writes
// the original (unconverted) subtitle bytes to w — matching the source Codec
// advertised in MediaStreams. The index mapping mirrors appendSubtitleStreams
// so the DeliveryUrl advertised in MediaStreams lines up exactly with the
// served track: subtitles start at 1 when no audio stream is present, otherwise
// at 2 (after Video 0 + Audio 1).
//
// 只服务外挂字幕文件（DiscoverExternalOnly）：云盘/strm 媒体的容器内嵌字幕
// 不做服务端提取，客户端直连播放直链时自行解析。
func (e *EmbyService) ServeSubtitleStream(ctx context.Context, w io.Writer, mediaID, indexStr string, userID string) error {
	if e == nil || e.subtitle == nil {
		return ErrSubtitleUnavailable
	}
	if e.remote != nil && IsEmbyRemoteID(mediaID) {
		// 远程字幕由反向代理透传（需要 http.ResponseWriter 能力），handler 层
		// 已对远程 ID 走 ProxyRemoteSubtitle，这里不重复处理。
		return ErrSubtitleNotFound
	}
	m, err := e.playableMedia(ctx, mediaID, userID)
	if err != nil || m == nil {
		return ErrSubtitleNotFound
	}
	index, err := strconv.Atoi(strings.TrimSpace(indexStr))
	if err != nil || index < 1 {
		return ErrSubtitleNotFound
	}
	tracks, err := e.subtitle.DiscoverExternalOnly(ctx, m.ID)
	if err != nil || len(tracks) == 0 {
		return ErrSubtitleNotFound
	}
	first := 1
	if strings.TrimSpace(m.AudioCodec) != "" {
		first = 2
	}
	offset := index - first
	if offset < 0 || offset >= len(tracks) {
		return ErrSubtitleNotFound
	}
	return e.subtitle.ServeRaw(ctx, m.ID, tracks[offset].Path, w)
}

// SubtitleStreamCodec resolves the source codec (ass/subrip/vtt/ssa) for a
// given Emby subtitle index so the handler can set an accurate Content-Type
// header before streaming the raw bytes.
func (e *EmbyService) SubtitleStreamCodec(ctx context.Context, mediaID, indexStr, userID string) string {
	if e == nil || e.subtitle == nil {
		return ""
	}
	m, err := e.playableMedia(ctx, mediaID, userID)
	if err != nil || m == nil {
		return ""
	}
	index, err := strconv.Atoi(strings.TrimSpace(indexStr))
	if err != nil || index < 1 {
		return ""
	}
	tracks, err := e.subtitle.DiscoverExternalOnly(ctx, m.ID)
	if err != nil || len(tracks) == 0 {
		return ""
	}
	first := 1
	if strings.TrimSpace(m.AudioCodec) != "" {
		first = 2
	}
	offset := index - first
	if offset < 0 || offset >= len(tracks) {
		return ""
	}
	return subtitleCodecFromExt(tracks[offset].Codec)
}

var (
	// ErrSubtitleUnavailable is returned when the Emby shim has no subtitle
	// service wired and therefore cannot serve external subtitle streams.
	ErrSubtitleUnavailable = fmt.Errorf("subtitle service unavailable")
	// ErrSubtitleNotFound is returned when a requested subtitle index does not
	// map to a discovered external subtitle track.
	ErrSubtitleNotFound = fmt.Errorf("subtitle not found")
)

// ensureCloudTrackMetadata 在后台补齐云盘媒体的轨道元数据。
//
// 注意必须是异步的：此前这里在 PlaybackInfo 请求路径上同步执行
// CloudResolve + ffprobe(HTTP)（最长 8 秒），既把第三方播放器的起播时间
// 拖长到秒级，又让每一次点开详情/起播都可能触发一次云盘数据下载，是
// Docker 部署下 CPU/带宽长期居高的来源之一。探测结果落库后，下一次
// 请求自然能读到完整元数据。
func mediaTrackMetadataMissing(m *model.Media) bool {
	return m.DurationSec <= 0 ||
		m.Width <= 0 ||
		m.Height <= 0 ||
		strings.TrimSpace(m.VideoCodec) == "" ||
		strings.TrimSpace(m.AudioCodec) == ""
}

func parseCloudMediaPlaybackURL(raw string) (string, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	path := strings.Trim(u.Path, "/")
	const prefix = "api/cloud/play/"
	idx := strings.Index(strings.ToLower(path), prefix)
	if idx < 0 {
		return "", "", false
	}
	typ := strings.TrimSpace(path[idx+len(prefix):])
	ref := strings.TrimSpace(u.Query().Get("ref"))
	return typ, ref, typ != "" && ref != ""
}

func applyProbeResultToMediaValue(m *model.Media, probe *ProbeResult) {
	if m == nil || probe == nil {
		return
	}
	if probe.DurationSec > 0 {
		m.DurationSec = probe.DurationSec
	}
	if probe.Width > 0 {
		m.Width = probe.Width
	}
	if probe.Height > 0 {
		m.Height = probe.Height
	}
	if strings.TrimSpace(probe.VideoCodec) != "" {
		m.VideoCodec = probe.VideoCodec
	}
	if strings.TrimSpace(probe.AudioCodec) != "" {
		m.AudioCodec = probe.AudioCodec
	}
	if strings.TrimSpace(probe.Container) != "" {
		m.Container = probe.Container
	}
}

// directPlayOnly reports whether the admin enabled「客户端直连解码」mode.
// In that mode the host never transcodes; clients must direct-play.
func (e *EmbyService) directPlayOnly(ctx context.Context) bool {
	if e.repo == nil || e.repo.Setting == nil {
		return false
	}
	v, err := e.repo.Setting.Get(ctx, PlaybackDirectOnlySettingKey)
	if err != nil {
		return false
	}
	return parseBoolSetting(v, false)
}

func (e *EmbyService) playableMedia(ctx context.Context, id, userID string) (*model.Media, error) {
	if season, ok, err := e.findSeasonGroup(ctx, id, userID); err != nil {
		return nil, err
	} else if ok && len(season.Episodes) > 0 {
		return &season.Episodes[0], nil
	}
	if series, ok, err := e.findSeriesGroup(ctx, id, userID); err != nil {
		return nil, err
	} else if ok && len(series.Episodes) > 0 {
		return &series.Episodes[0], nil
	}
	m, err := e.repo.Media.FindByID(ctx, id)
	if err != nil || m == nil {
		return m, err
	}
	if !UserDefaultMediaVisibility(ctx, e.repo, userID).Allows(m) {
		return nil, nil
	}
	return m, nil
}

// mediaSource 是 /Items 与 /PlaybackInfo 共享的 MediaSource 结构。
//
// asEmbedded=true：嵌在 /Items 列表里，不包含完整 stream URL（避免暴露
// 直链给搜索接口）。/PlaybackInfo 走 false 路径，URL 指向 Emby 兼容
// /Videos/{id}/stream（客户端会继续携带 X-Emby-Token 或 append api_key）。
func (e *EmbyService) mediaSource(ctx context.Context, m *model.Media, asEmbedded, directOnly bool) map[string]any {
	container := embyMediaContainer(m)
	isCloud := strings.TrimSpace(m.STRMURL) != ""
	playURL := e.embyMediaPlayURL(ctx, m, container, isCloud)
	if isCloud {
		// Cloud/WebDAV media is already a direct/proxy stream. Advertising HLS
		// transcoding makes some Emby clients pick /master.m3u8, forcing this
		// lightweight server to pull remote bytes through ffmpeg and often
		// surfacing as "network/playback failed". Keep cloud media direct-only.
		directOnly = true
	}
	src := e.baseMediaSource(ctx, m, container, isCloud, playURL, directOnly)
	if !asEmbedded && playURL != "" {
		src["DirectStreamUrl"] = playURL
		// 直连解码模式下不下发 TranscodingUrl，迫使客户端本地解码直连，
		// 宿主机不参与转码。
		if !directOnly {
			src["TranscodingUrl"] = "/Videos/" + m.ID + "/master.m3u8"
		}
	}
	if strings.TrimSpace(m.STRMURL) != "" && playURL != "" {
		// STRM / cloud:// media must stay behind a token-aware endpoint. When
		// STRM playback is enabled we expose /api/stream so third-party clients
		// follow the same STRM entry as generated .strm files; when disabled we
		// expose /Videos/{id}/stream so playback uses the Emby 302/proxy path.
		src["IsRemote"] = true
	}
	return src
}

func (e *EmbyService) baseMediaSource(ctx context.Context, m *model.Media, container string, isCloud bool, playURL string, directOnly bool) map[string]any {
	return map[string]any{
		"Id":                    m.ID,
		"Name":                  MediaVersionLabel(*m),
		"Path":                  embyMediaSourcePath(m),
		"Container":             container,
		"Size":                  m.SizeBytes,
		"Protocol":              "Http",
		"Type":                  "Default",
		"IsRemote":              isCloud,
		"RequiresOpening":       false,
		"RequiresClosing":       false,
		"ReadAtNativeFramerate": false,
		"SupportsTranscoding":   !directOnly,
		"SupportsDirectStream":  !isCloud || playURL != "",
		"SupportsDirectPlay":    !isCloud || playURL != "",
		"SupportsProbing":       true,
		"RunTimeTicks":          int64(m.DurationSec) * 10_000_000,
		"MediaStreams":          e.mediaStreams(ctx, m),
	}
}

// embyMediaSourcePath keeps Emby's Path field as a source identity rather
// than a playback endpoint. OpenList and other path-based cloud providers
// carry the real provider path in the internal cloud-play ref query. For
// opaque provider refs, fall back to the display path stored in cloud://.
func embyMediaSourcePath(m *model.Media) string {
	if m == nil {
		return ""
	}
	if _, ref, ok := parseCloudMediaPlaybackURL(m.STRMURL); ok && strings.HasPrefix(ref, "/") {
		return ref
	}
	raw := strings.TrimSpace(m.Path)
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "cloud") {
		return m.Path
	}
	sourcePath := strings.TrimSpace(u.Path)
	if decoded, decodeErr := url.PathUnescape(sourcePath); decodeErr == nil {
		sourcePath = decoded
	}
	if sourcePath == "" {
		return "/"
	}
	if !strings.HasPrefix(sourcePath, "/") {
		sourcePath = "/" + sourcePath
	}
	return sourcePath
}

func embyMediaContainer(m *model.Media) string {
	container := strings.Trim(strings.ToLower(m.Container), ". ")
	if container == "" {
		container = strings.TrimPrefix(strings.ToLower(filepath.Ext(m.Path)), ".")
	}
	if container == "" && strings.TrimSpace(m.STRMURL) != "" {
		return "strm"
	}
	return container
}

func (e *EmbyService) embyMediaPlayURL(ctx context.Context, m *model.Media, container string, isCloud bool) string {
	if !isCloud {
		return embyDirectStreamURL(m.ID, container)
	}
	switch CloudPlaybackMode(ctx, e.repo) {
	case CloudPlaybackModeSTRM:
		return embySTRMStreamURL(m.ID)
	case CloudPlaybackModeRedirectProxy:
		return embyDirectStreamURL(m.ID, container)
	default:
		return ""
	}
}
