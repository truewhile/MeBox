package service

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// Danmaku setting keys, managed through the admin settings UI (PUT
// /admin/settings). They are stored in the Setting table so the playback
// page can pull them without admin privileges.
const (
	DanmakuEnabledKey  = "danmaku.enabled"
	DanmakuSourceKey   = "danmaku.source"
	DanmakuOpacityKey  = "danmaku.opacity"
	DanmakuFontSizeKey = "danmaku.font_size"
	DanmakuAreaKey     = "danmaku.area"
	// DanmakuAppIDKey / DanmakuAppKeyKey hold the optional dandanplay
	// DevCenter application credentials. When both are set they override the
	// built-in fallback pair (see danmaku_credentials.go).
	DanmakuAppIDKey  = "danmaku.app_id"
	DanmakuAppKeyKey = "danmaku.app_key"
)

// DanmakuDefaultSource is the official dandanplay endpoint used when the
// admin leaves the source address empty. Any server implementing the
// dandanplay protocol (search/episodes + comment/{episodeId}) may be used.
const DanmakuDefaultSource = "https://api.dandanplay.net"

// danmakuOfficialBase is where identification (/api/v2/match) and the
// comment/search fallback always go, regardless of the configured source.
// A package var (not a const) so tests can point it at a local server.
var danmakuOfficialBase = DanmakuDefaultSource

// DanmakuRenderConfig carries the renderer knobs to the web player.
type DanmakuRenderConfig struct {
	Enabled  bool   `json:"enabled"`
	Source   string `json:"source,omitempty"`
	Opacity  string `json:"opacity"`
	FontSize string `json:"font_size"`
	Area     string `json:"area"`
}

// DanmakuFetchResult is what /api/danmaku/:id returns. Raw holds the upstream
// comment payload; parsing happens client-side. The dandanplay protocol has
// two payload shapes in the wild — the classic Bilibili-style XML and the
// newer dandanplay JSON ({"count":N,"comments":[{p,m,t,...}]}) — so
// source_type is sniffed from the body rather than fixed.
//
// Candidates is non-nil when multiple anime matched the search and the player
// must ask the user which one to use (disambiguation); Raw is empty then.
// AnimeTitle, EpisodeTitle, EpisodeID and MatchMode provide matched danmaku
// metadata so the player UI can display which episode was loaded.
type DanmakuFetchResult struct {
	DanmakuRenderConfig
	SourceType   string         `json:"source_type"`
	Raw          string         `json:"raw,omitempty"`
	Candidates   []DanmakuAnime `json:"candidates,omitempty"`
	AnimeTitle   string         `json:"anime_title,omitempty"`
	EpisodeTitle string         `json:"episode_title,omitempty"`
	EpisodeID    int64          `json:"episode_id,omitempty"`
	MatchMode    string         `json:"match_mode,omitempty"`
}

// DanmakuAnime is one search hit (an anime) with its episode list, mirroring
// the dandanplay SearchEpisodesResponse shape.
type DanmakuAnime struct {
	AnimeID    int64            `json:"animeId"`
	AnimeTitle string           `json:"animeTitle"`
	Episodes   []DanmakuEpisode `json:"episodes"`
}

// DanmakuEpisode is one selectable danmaku library inside an anime.
type DanmakuEpisode struct {
	EpisodeID    int64  `json:"episodeId"`
	EpisodeTitle string `json:"episodeTitle"`
}

// DanmakuRemoteMediaResolver resolves an Emby remote pseudo-ID (e.g. embyremote~mount~id)
// into a memory model.Media and a direct stream URL.
type DanmakuRemoteMediaResolver func(ctx context.Context, encodedID string) (*model.Media, string, error)

// DanmakuService fetches danmaku for a media item through the dandanplay
// protocol: match by 16MB-prefix hash, then search for an episode id by the
// video's name, then fetch the comment library XML. The React player parses
// and renders it.
type DanmakuService struct {
	log    *zap.Logger
	repo   *repository.Container
	client *http.Client

	// strmResolve resolves a .strm play indirection into a fetchable target
	// (local path / redirect URL / proxied link). Wired by the builder to
	// StrmService.ResolvePlay; nil means strm sources are skipped.
	strmResolve func(ctx context.Context, provider string, q url.Values) (*StrmPlayResult, error)

	// remoteResolve resolves an Emby remote pseudo-ID into *model.Media and
	// direct stream URL for range hashing.
	remoteResolve DanmakuRemoteMediaResolver

	hashCacheMu sync.Mutex
	hashCache   map[string]string // stamp → 16MB-prefix MD5
}

func danmakuHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}

func NewDanmakuService(log *zap.Logger, repo *repository.Container) *DanmakuService {
	if log == nil {
		log = zap.NewNop()
	}
	return &DanmakuService{
		log:       log,
		repo:      repo,
		client:    danmakuHTTPClient(),
		hashCache: make(map[string]string),
	}
}

// SetStrmResolver wires the strm play resolver used to fetch cloud video
// bytes for hash computation.
func (s *DanmakuService) SetStrmResolver(resolve func(ctx context.Context, provider string, q url.Values) (*StrmPlayResult, error)) {
	if s != nil {
		s.strmResolve = resolve
	}
}

// SetRemoteMediaResolver wires the resolver used to fetch metadata and direct
// stream URLs for Emby remote mounted media.
func (s *DanmakuService) SetRemoteMediaResolver(resolve DanmakuRemoteMediaResolver) {
	if s != nil {
		s.remoteResolve = resolve
	}
}

// Config reads danmaku settings from the runtime settings table.
func (s *DanmakuService) Config(ctx context.Context) DanmakuRenderConfig {
	cfg := DanmakuRenderConfig{
		Opacity:  "1",
		FontSize: "24",
		Area:     "1",
	}
	if s == nil || s.repo == nil || s.repo.Setting == nil {
		return cfg
	}
	read := func(key, fallback string) string {
		v, err := s.repo.Setting.Get(ctx, key)
		if err != nil || v == "" {
			return fallback
		}
		return v
	}
	cfg.Enabled = ParseBoolSetting(read(DanmakuEnabledKey, "true"), true)
	cfg.Source = read(DanmakuSourceKey, "")
	cfg.Opacity = read(DanmakuOpacityKey, "1")
	cfg.FontSize = read(DanmakuFontSizeKey, "24")
	cfg.Area = read(DanmakuAreaKey, "1")
	return cfg
}

// Fetch retrieves danmaku for the given media. keyword overrides the
// media-derived search term (empty = use the video's own name); pass it from
// the player when the user searches for a custom title. episodeID forces a
// specific danmaku library chosen by the user (from a previous disambiguation
// response); empty means auto-resolution through the dandanplay protocol:
//
//  1. match: MD5 of the first 16MB of the video (local file read directly,
//     .strm resolved to a direct link and range-fetched) → /api/v2/match
//     against the official endpoint, which yields the episode library id.
//  2. search by the playing file's name + episode number.
//  3. current auto-identification (original name → title → file name + episode,
//     single hit used, several hits returned as candidates for the player).
//  4. manual: the player picks from the returned candidates (episodeID /
//     keyword override).
//
// Comments are always fetched from the configured source first (when set)
// and fall back to the official endpoint on failure; identification itself
// always goes to the official endpoint.
//
// When danmaku is disabled the result carries Enabled=false so the player can
// silently skip rendering.
func (s *DanmakuService) Fetch(ctx context.Context, mediaID, keyword, episodeID string) (*DanmakuFetchResult, error) {
	res := &DanmakuFetchResult{DanmakuRenderConfig: s.Config(ctx), SourceType: "auto"}
	if !res.Enabled {
		return res, nil
	}
	configured := strings.TrimRight(strings.TrimSpace(res.Source), "/")
	official := danmakuOfficialBase

	// 手动指定弹幕库：跳过识别，直接拉取该库（自定义源失败回退官方）。
	if target := strings.TrimSpace(episodeID); target != "" {
		raw, st, err := s.fetchCommentWithFallback(ctx, configured, official, target)
		if err != nil {
			s.log.Warn("danmaku comment fetch failed", zap.String("media_id", mediaID), zap.String("episode_id", target), zap.Error(err))
			return res, err
		}
		res.Raw, res.SourceType = raw, st
		if id, parseErr := strconv.ParseInt(target, 10, 64); parseErr == nil {
			res.EpisodeID = id
		}
		res.MatchMode = "manual"
		return res, nil
	}

	term, media, err := s.searchTerms(ctx, mediaID)
	if err != nil {
		return res, err
	}
	manualKeyword := strings.TrimSpace(keyword) != ""
	if kw := strings.TrimSpace(keyword); kw != "" {
		term.name = kw
	}
	if strings.TrimSpace(term.name) == "" {
		return res, nil
	}

	target := ""

	// 1) hash 识别：始终走官方 /api/v2/match（keyword 手动覆盖时跳过，直接走第 3 层）。
	if target == "" && !manualKeyword && media != nil && (media.Path != "" || IsEmbyRemoteID(media.ID)) {
		if hash, ok := s.mediaHash(ctx, media); ok {
			fileSize := media.SizeBytes
			if media.Path != "" && strings.EqualFold(filepath.Ext(media.Path), ".strm") {
				fileSize = 0 // strm 行的 SizeBytes 是文本大小，不是视频大小
			}
			matchName := danmakuMatchFileName(media.Path)
			if matchName == "" {
				matchName = term.name
			}
			matches, err := s.matchOfficial(ctx, matchName, hash, fileSize, media.DurationSec)
			if err != nil {
				s.log.Warn("danmaku hash match failed", zap.String("media_id", mediaID), zap.Error(err))
			} else if len(matches) > 0 {
				target = fmt.Sprintf("%d", matches[0].EpisodeID)
				res.AnimeTitle = matches[0].AnimeTitle
				res.EpisodeTitle = matches[0].EpisodeTitle
				res.EpisodeID = matches[0].EpisodeID
				res.MatchMode = "hash"
			}
		}
	}

	// 2) 按播放的文件名 + 集数搜索（keyword 手动覆盖时跳过，直接走第 3 层）。
	if target == "" && !manualKeyword && media != nil && media.Path != "" {
		if fileName := danmakuMatchFileName(media.Path); fileName != "" && fileName != term.name {
			if candidates, err := s.searchCandidatesWithFallback(ctx, configured, official, fileName, term.episode); err == nil &&
				len(candidates) == 1 && len(candidates[0].Episodes) > 0 {
				target = fmt.Sprintf("%d", candidates[0].Episodes[0].EpisodeID)
				res.AnimeTitle = candidates[0].AnimeTitle
				res.EpisodeTitle = candidates[0].Episodes[0].EpisodeTitle
				res.EpisodeID = candidates[0].Episodes[0].EpisodeID
				res.MatchMode = "filename"
			}
		}
	}

	// 3) 现有自动识别：标题层级（original_name → title → 文件名）+ 集数，
	//    多结果返回候选列表交给播放器（歧义处理）。
	if target == "" {
		candidates, err := s.searchCandidatesWithFallback(ctx, configured, official, term.name, term.episode)
		if err != nil {
			s.log.Warn("danmaku search failed", zap.String("media_id", mediaID), zap.String("name", term.name), zap.String("episode", term.episode), zap.Error(err))
			return res, err
		}
		if len(candidates) != 1 {
			res.Candidates = candidates
			return res, nil
		}
		if len(candidates[0].Episodes) == 0 {
			return res, errors.New("no danmaku library found for this video")
		}
		target = fmt.Sprintf("%d", candidates[0].Episodes[0].EpisodeID)
		res.AnimeTitle = candidates[0].AnimeTitle
		res.EpisodeTitle = candidates[0].Episodes[0].EpisodeTitle
		res.EpisodeID = candidates[0].Episodes[0].EpisodeID
		res.MatchMode = "search"
	}

	raw, st, err := s.fetchCommentWithFallback(ctx, configured, official, target)
	if err != nil {
		s.log.Warn("danmaku comment fetch failed", zap.String("media_id", mediaID), zap.String("episode_id", target), zap.Error(err))
		return res, err
	}
	res.Raw, res.SourceType = raw, st
	return res, nil
}

// detectDanmakuSourceType guesses the comment payload format from its body.
// The dandanplay protocol historically returned Bilibili-style XML, but newer
// and self-hosted implementations return the dandanplay JSON shape
// ({"count":N,"comments":[{p,m,t,...}]}); sniff the leading byte instead of
// trusting the source. Unknown/empty bodies fall back to "auto" so the player
// can try both parsers.
func detectDanmakuSourceType(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "auto"
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return "json"
	}
	return "xml"
}

type danmakuSearchTerms struct {
	name    string // 作品标题（original_name → title → 文件名）
	episode string // 集数编号（dandanplay episode 参数，正整数）
}

// searchTerms resolves the name and episode number used to look up the
// dandanplay library (and the media row for hash identification). Episode 0
// (movies / unknown) is left empty so the search does not filter by episode.
func (s *DanmakuService) searchTerms(ctx context.Context, mediaID string) (danmakuSearchTerms, *model.Media, error) {
	var term danmakuSearchTerms
	if IsEmbyRemoteID(mediaID) {
		if s == nil || s.remoteResolve == nil {
			return term, nil, errors.New("remote emby resolver unavailable")
		}
		m, _, err := s.remoteResolve(ctx, mediaID)
		if err != nil || m == nil {
			if err != nil {
				return term, nil, err
			}
			return term, nil, errors.New("media not found")
		}
		if name := strings.TrimSpace(m.OriginalName); name != "" {
			term.name = name
		} else if name := strings.TrimSpace(m.Title); name != "" {
			term.name = name
		} else {
			term.name = danmakuMatchFileName(m.Path)
		}
		if m.EpisodeNum > 0 {
			term.episode = strconv.Itoa(m.EpisodeNum)
		}
		return term, m, nil
	}
	if s == nil || s.repo == nil || s.repo.Media == nil {
		return term, nil, errors.New("media repository unavailable")
	}
	m, err := s.repo.Media.FindByID(ctx, mediaID)
	if err != nil {
		return term, nil, err
	}
	if m == nil {
		return term, nil, errors.New("media not found")
	}
	if name := strings.TrimSpace(m.OriginalName); name != "" {
		term.name = name
	} else if name := strings.TrimSpace(m.Title); name != "" {
		term.name = name
	} else {
		term.name = mediaSidecarBase(m.Path)
		if term.name == "" {
			term.name = strings.TrimSuffix(filepath.Base(m.Path), filepath.Ext(m.Path))
		}
	}
	if m.EpisodeNum > 0 {
		term.episode = strconv.Itoa(m.EpisodeNum)
	}
	return term, m, nil
}

// searchCandidates returns every anime hit for a name via the dandanplay
// search endpoint, with the episode list per anime. The optional episode
// number filters the result to that specific episode. Responses follow
// SearchEpisodesResponse: { animes: [{ animeId, animeTitle, episodes:
// [{ episodeId, episodeTitle }] }] }.
func (s *DanmakuService) searchCandidates(ctx context.Context, base, name, episode string) ([]DanmakuAnime, error) {
	u := fmt.Sprintf("%s/api/v2/search/episodes?anime=%s&v2=true", base, url.QueryEscape(name))
	if episode != "" {
		u += "&episode=" + url.QueryEscape(episode)
	}
	body, err := s.fetchBody(ctx, u, false)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Animes []struct {
			AnimeID    int64  `json:"animeId"`
			AnimeTitle string `json:"animeTitle"`
			Episodes   []struct {
				EpisodeID    int64  `json:"episodeId"`
				EpisodeTitle string `json:"episodeTitle"`
			} `json:"episodes"`
		} `json:"animes"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("danmaku search returned invalid JSON: %w", err)
	}
	var out []DanmakuAnime
	for _, anime := range resp.Animes {
		item := DanmakuAnime{AnimeID: anime.AnimeID, AnimeTitle: anime.AnimeTitle}
		for _, ep := range anime.Episodes {
			if ep.EpisodeID <= 0 {
				continue
			}
			item.Episodes = append(item.Episodes, DanmakuEpisode{
				EpisodeID:    ep.EpisodeID,
				EpisodeTitle: ep.EpisodeTitle,
			})
		}
		if len(item.Episodes) == 0 {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *DanmakuService) fetchBody(ctx context.Context, sourceURL string, followRedirect bool) (string, error) {
	client := s.client
	if !followRedirect {
		// Search returns plain JSON; the comment endpoint 302-redirects to a
		// danmaku accelerator CDN which the default client follows.
		copied := *s.client
		copied.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
		client = &copied
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "MeBox/danmaku (+https://github.com/truewhile/MeBox)")
	req.Header.Set("Accept", "application/json, application/xml, */*")
	if appID, appKey, ok := s.danmakuCredentials(ctx, sourceURL); ok {
		// 签名认证：base64(sha256(AppId+Timestamp+Path+Secret))，密钥不出服务器。
		ts := time.Now().Unix()
		path := "/"
		if u, err := url.Parse(sourceURL); err == nil && u.Path != "" {
			path = u.Path
		}
		req.Header.Set("X-AppId", appID)
		req.Header.Set("X-Timestamp", strconv.FormatInt(ts, 10))
		req.Header.Set("X-Signature", dandanplaySignature(appID, appKey, ts, path))
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("danmaku source returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// danmakuCredentials resolves the application credentials for the official
// dandanplay API. Admin-configured values (danmaku.app_id / danmaku.app_key)
// win; otherwise the built-in obfuscated fallback pair is used. Returns
// ok=false for any other host so credentials — including the built-in pair —
// are never sent to third-party dandanplay protocol mirrors. The "official"
// host follows danmakuOfficialBase (overridable in tests).
func (s *DanmakuService) danmakuCredentials(ctx context.Context, sourceURL string) (appID, appKey string, ok bool) {
	u, err := url.Parse(sourceURL)
	if err != nil {
		return "", "", false
	}
	official, err := url.Parse(danmakuOfficialBase)
	if err != nil || !strings.EqualFold(u.Hostname(), official.Hostname()) {
		return "", "", false
	}
	var id, key string
	if s != nil && s.repo != nil && s.repo.Setting != nil {
		id, _ = s.repo.Setting.Get(ctx, DanmakuAppIDKey)
		key, _ = s.repo.Setting.Get(ctx, DanmakuAppKeyKey)
	}
	id, key = strings.TrimSpace(id), strings.TrimSpace(key)
	if id != "" && key != "" {
		return id, key, true
	}
	if id != "" || key != "" {
		s.log.Warn("danmaku credentials incomplete, using built-in fallback",
			zap.Bool("has_app_id", id != ""), zap.Bool("has_app_key", key != ""))
	}
	embedID, embedKey := danmakuEmbeddedCredentials()
	return embedID, embedKey, true
}

// danmakuHashPrefixBytes 是 dandanplay match 规格要求的前 16MB 数据。
const danmakuHashPrefixBytes = 16 << 20

const danmakuHashCacheMax = 256

func (s *DanmakuService) hashCacheGet(stamp string) (string, bool) {
	s.hashCacheMu.Lock()
	defer s.hashCacheMu.Unlock()
	h, ok := s.hashCache[stamp]
	return h, ok
}

func (s *DanmakuService) hashCachePut(stamp, hash string) {
	s.hashCacheMu.Lock()
	defer s.hashCacheMu.Unlock()
	if len(s.hashCache) >= danmakuHashCacheMax {
		// 简单淘汰：满了整体清空；哈希只用于重复播放时的缓存命中。
		s.hashCache = make(map[string]string)
	}
	s.hashCache[stamp] = hash
}

// danmakuMatchFileName derives the /api/v2/match fileName: base name without
// the final extension. .strm items are covered too — MeBox strm files drop the
// video extension ("xxx.strm") while pre-existing ones may keep it
// ("xxx.mkv.strm") — so a second strip removes a real video extension only
// (filepath.Ext would misread names like "xxx.第01话" as having an extension).
func danmakuMatchFileName(path string) string {
	return mediaSidecarBase(path)
}

// mediaHash returns the dandanplay match hash (MD5 of the first 16MB of the
// video). Local videos are hashed straight from disk; .strm indirections and
// remote Emby streams are range-fetched and only the 16MB prefix is downloaded.
func (s *DanmakuService) mediaHash(ctx context.Context, media *model.Media) (string, bool) {
	if media == nil {
		return "", false
	}
	if IsEmbyRemoteID(media.ID) {
		return s.hashEmbyRemote(ctx, media)
	}
	if media.Path == "" {
		return "", false
	}
	if strings.EqualFold(filepath.Ext(media.Path), ".strm") {
		target := media.STRMURL
		if target == "" {
			parsed, err := readLocalSTRMTarget(media.Path)
			if err != nil || parsed == "" {
				return "", false
			}
			target = parsed
		}
		return s.hashStrmTarget(ctx, target)
	}
	return s.hashLocalFile(media.Path)
}

// hashEmbyRemote computes the 16MB-prefix MD5 of a remote Emby stream via HTTP Range.
func (s *DanmakuService) hashEmbyRemote(ctx context.Context, media *model.Media) (string, bool) {
	if media == nil || media.ID == "" {
		return "", false
	}
	if h, ok := s.hashCacheGet("e|" + media.ID); ok {
		return h, true
	}
	if s.remoteResolve == nil {
		return "", false
	}
	_, streamURL, err := s.remoteResolve(ctx, media.ID)
	if err != nil || strings.TrimSpace(streamURL) == "" {
		if err != nil {
			s.log.Warn("danmaku emby stream url resolve failed, hash layer skipped",
				zap.String("media_id", media.ID), zap.Error(err))
		}
		return "", false
	}
	body, err := s.openRangeBody(ctx, streamURL, nil)
	if err != nil || body == nil {
		if err != nil {
			s.log.Warn("danmaku emby range fetch failed, hash layer skipped",
				zap.String("media_id", media.ID), zap.Error(err))
		}
		return "", false
	}
	defer body.Close()
	h := md5.New()
	if _, err := io.Copy(h, io.LimitReader(body, danmakuHashPrefixBytes)); err != nil {
		return "", false
	}
	hash := hex.EncodeToString(h.Sum(nil))
	s.hashCachePut("e|"+media.ID, hash)
	return hash, true
}

// hashLocalFile computes the MD5 of the first 16MB of a local video, cached
// by path+size+mtime so repeated danmaku loads skip the disk read.
func (s *DanmakuService) hashLocalFile(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	stamp := fmt.Sprintf("f|%s|%d|%d", path, info.Size(), info.ModTime().UnixNano())
	if h, ok := s.hashCacheGet(stamp); ok {
		return h, true
	}
	f, err := os.Open(path) // #nosec G304 -- path 来自已入库的媒体行
	if err != nil {
		return "", false
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, io.LimitReader(f, danmakuHashPrefixBytes)); err != nil {
		return "", false
	}
	hash := hex.EncodeToString(h.Sum(nil))
	s.hashCachePut(stamp, hash)
	return hash, true
}

// hashStrmTarget computes the video hash behind a .strm indirection:
// MeBox-internal /api/strm/play URLs are resolved through strmResolve (local
// path read directly, cloud links range-fetched); plain http(s) links are
// fetched directly. Only the 16MB prefix is ever downloaded.
func (s *DanmakuService) hashStrmTarget(ctx context.Context, raw string) (string, bool) {
	if h, ok := s.hashCacheGet("s|" + raw); ok {
		return h, true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	var (
		src  *StrmPlayResult
		body io.ReadCloser
	)
	switch {
	case strings.HasPrefix(u.Path, "/api/strm/play/"):
		// /api/strm/play/{provider}/video{ext}?acct=..&pickcode=..
		segs := strings.Split(strings.TrimPrefix(u.Path, "/api/strm/play/"), "/")
		if len(segs) < 2 || s.strmResolve == nil {
			return "", false
		}
		src, err = s.strmResolve(ctx, segs[0], u.Query())
		if err != nil || src == nil {
			s.log.Warn("danmaku strm resolve failed, hash layer skipped",
				zap.String("provider", segs[0]), zap.Error(err))
			return "", false
		}
	case u.Scheme == "http" || u.Scheme == "https":
		src = &StrmPlayResult{RedirectURL: raw}
	default:
		// webdav/alist 等协议无法直接用标准 HTTP 拉取，交给搜索层兜底。
		return "", false
	}
	switch {
	case src.LocalPath != "":
		return s.hashLocalFile(src.LocalPath)
	case src.RedirectURL != "":
		var headers map[string]string
		if src.Link != nil {
			headers = src.Link.Headers // 115 直链防盗链要求携带绑定 UA
		}
		body, err = s.openRangeBody(ctx, src.RedirectURL, headers)
	case src.Link != nil && src.Link.URL != "":
		body, err = s.openRangeBody(ctx, src.Link.URL, src.Link.Headers)
	default:
		return "", false
	}
	if err != nil || body == nil {
		if err != nil {
			s.log.Warn("danmaku hash prefix fetch failed, hash layer skipped", zap.String("target", raw), zap.Error(err))
		}
		return "", false
	}
	defer body.Close()
	h := md5.New()
	if _, err := io.Copy(h, io.LimitReader(body, danmakuHashPrefixBytes)); err != nil {
		return "", false
	}
	hash := hex.EncodeToString(h.Sum(nil))
	s.hashCachePut("s|"+raw, hash)
	return hash, true
}

// openRangeBody issues a Range request for the 16MB video prefix. Range is a
// suggestion — servers that ignore it are capped by the caller's LimitReader.
func (s *DanmakuService) openRangeBody(ctx context.Context, target string, headers map[string]string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MeBox/danmaku (+https://github.com/truewhile/MeBox)")
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", danmakuHashPrefixBytes-1))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := s.client
	if client == nil {
		client = danmakuHTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("hash range fetch returned HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// danmakuMatch mirrors one hit of the /api/v2/match response.
type danmakuMatch struct {
	EpisodeID    int64  `json:"episodeId"`
	AnimeID      int64  `json:"animeId"`
	AnimeTitle   string `json:"animeTitle"`
	EpisodeTitle string `json:"episodeTitle"`
}

// matchOfficial identifies the video via POST /api/v2/match on the official
// endpoint (always official, signed with the app credentials). Returns the
// candidate list; empty means nothing matched.
//
// fileName must be URL-escaped: the official API rejects raw non-ASCII file
// names with errorCode 2 (verified against the live API — QueryEscape's
// percent-encoding with "+" for space is accepted).
func (s *DanmakuService) matchOfficial(ctx context.Context, fileName, fileHash string, fileSize int64, durationSec int) ([]danmakuMatch, error) {
	appID, appKey, ok := s.danmakuCredentials(ctx, danmakuOfficialBase)
	if !ok {
		return nil, errors.New("danmaku credentials unavailable")
	}
	payload := struct {
		FileName      string `json:"fileName"`
		FileHash      string `json:"fileHash"`
		FileSize      int64  `json:"fileSize"`
		VideoDuration int    `json:"videoDuration"`
		MatchMode     string `json:"matchMode"`
	}{
		FileName:      url.QueryEscape(fileName),
		FileHash:      fileHash,
		FileSize:      fileSize,
		VideoDuration: durationSec,
		MatchMode:     "hashAndFileName",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	path := "/api/v2/match"
	ts := time.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, danmakuOfficialBase+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AppId", appID)
	req.Header.Set("X-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Signature", dandanplaySignature(appID, appKey, ts, path))
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("danmaku match returned HTTP %d", resp.StatusCode)
	}
	var out struct {
		Success bool           `json:"success"`
		Matches []danmakuMatch `json:"matches"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("danmaku match returned invalid JSON: %w", err)
	}
	if !out.Success {
		return nil, nil
	}
	return out.Matches, nil
}

// sameDanmakuBase reports whether two source bases point at the same origin
// (host, including port) so the fallback does not call the same server twice.
func sameDanmakuBase(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil && strings.EqualFold(ua.Host, ub.Host)
}

// fetchCommentWithFallback fetches a comment library from the configured
// source first (when set and different from official), falling back to the
// official endpoint on failure.
func (s *DanmakuService) fetchCommentWithFallback(ctx context.Context, configured, official, target string) (raw, sourceType string, err error) {
	var bases []string
	if configured != "" && !sameDanmakuBase(configured, official) {
		bases = append(bases, configured)
	}
	bases = append(bases, official)
	var lastErr error
	for i, base := range bases {
		raw, err = s.fetchBody(ctx, fmt.Sprintf("%s/api/v2/comment/%s?withRelated=true", base, target), true)
		if err == nil {
			return raw, detectDanmakuSourceType(raw), nil
		}
		lastErr = err
		if i < len(bases)-1 {
			s.log.Warn("danmaku comment fetch failed on configured source, falling back to official", zap.String("source", base), zap.Error(err))
		}
	}
	return "", "auto", lastErr
}

// searchCandidatesWithFallback searches the configured source first (when
// set and different from official), falling back to the official endpoint on
// failure.
func (s *DanmakuService) searchCandidatesWithFallback(ctx context.Context, configured, official, name, episode string) ([]DanmakuAnime, error) {
	if configured != "" && !sameDanmakuBase(configured, official) {
		candidates, err := s.searchCandidates(ctx, configured, name, episode)
		if err == nil {
			return candidates, nil
		}
		s.log.Warn("danmaku search failed on configured source, falling back to official", zap.String("source", configured), zap.Error(err))
	}
	return s.searchCandidates(ctx, official, name, episode)
}
