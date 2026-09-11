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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

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
	// MergeSources 是当前用户的弹幕合并偏好（按用户存储）。
	MergeSources bool `json:"merge_sources"`
}

// DanmakuFetchOptions 承载单次抓取的调用方偏好。
type DanmakuFetchOptions struct {
	// MergeSources 为真时，同一集的多个来源会被合并去重后一起返回。
	MergeSources bool
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
	SourceType string         `json:"source_type"`
	Raw        string         `json:"raw,omitempty"`
	Candidates []DanmakuAnime `json:"candidates,omitempty"`
	// Alternatives 是「同一集的其它可选来源」。与 Candidates 语义不同：
	// Candidates 表示自动匹配不唯一、必须由用户选择后才能加载弹幕；
	// Alternatives 表示弹幕已经自动加载好了，这里额外提供同集的其它来源
	// （LogVar 聚合了多个视频网站，同一集常有多个库）供用户随时切换，
	// 不必再手动搜索一遍。
	Alternatives []DanmakuAnime `json:"alternatives,omitempty"`
	AnimeTitle   string         `json:"anime_title,omitempty"`
	EpisodeTitle string         `json:"episode_title,omitempty"`
	EpisodeID    int64          `json:"episode_id,omitempty"`
	MatchMode    string         `json:"match_mode,omitempty"`
	// MergedSources 表示本次结果由多少个来源合并而成（未合并时为 0）。
	MergedSources int `json:"merged_sources,omitempty"`
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

// ConfigForUser 在全局渲染设置之外附加当前用户的个性化偏好。
func (s *DanmakuService) ConfigForUser(ctx context.Context, userID string) DanmakuRenderConfig {
	cfg := s.Config(ctx)
	cfg.MergeSources = s.MergeSourcesEnabled(ctx, userID)
	return cfg
}

// MergeSourcesEnabled 返回该用户的弹幕合并偏好，读取失败时回退为关闭。
func (s *DanmakuService) MergeSourcesEnabled(ctx context.Context, userID string) bool {
	if s == nil || s.repo == nil || s.repo.User == nil {
		return false
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	user, err := s.repo.User.FindByID(ctx, userID)
	if err != nil || user == nil {
		return false
	}
	return user.DanmakuMergeSources
}

// SetMergeSources 持久化该用户的弹幕合并偏好。
func (s *DanmakuService) SetMergeSources(ctx context.Context, userID string, enabled bool) error {
	if s == nil || s.repo == nil || s.repo.User == nil {
		return errors.New("danmaku settings unavailable")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("missing user")
	}
	return s.repo.User.UpdateFields(ctx, userID, map[string]any{"danmaku_merge_sources": enabled})
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
	return s.FetchWithOptions(ctx, mediaID, keyword, episodeID, DanmakuFetchOptions{})
}

// FetchWithOptions 是 Fetch 的带偏好版本。
func (s *DanmakuService) FetchWithOptions(ctx context.Context, mediaID, keyword, episodeID string, opts DanmakuFetchOptions) (*DanmakuFetchResult, error) {
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
	// targetBase 是 target 所属的源。各源的 episodeId 空间互相独立，必须用
	// 产生该 ID 的源去请求弹幕，否则会拿到 404；默认沿用「配置源优先」行为。
	targetBase := configured
	// hashOfficialID 记录 hash 层的官方 episodeId。仅当配置源重定位成功时
	// 才填，用于「配置源该集无弹幕」时回官方兜底。
	hashOfficialID := int64(0)

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
				match := matches[0]
				res.AnimeTitle = match.AnimeTitle
				res.EpisodeTitle = match.EpisodeTitle
				res.EpisodeID = match.EpisodeID
				res.MatchMode = "hash"
				// match 返回的是官方 ID 空间的 episodeId，直接拿去问第三方源
				// 只会 404（实测各源 ID 空间独立）。先用官方给到的剧名+集数
				// 在配置源里重定位到它自己的 episodeId；定位不到就整条走官方。
				if configured != "" && !sameDanmakuBase(configured, official) {
					if matched, ok := s.lookupConfiguredEpisodes(ctx, configured, match); ok {
						configuredID := firstDanmakuEpisodeID(matched)
						target = strconv.FormatInt(configuredID, 10)
						targetBase = configured
						res.EpisodeID = configuredID
						hashOfficialID = match.EpisodeID
						// 同集有多个来源时全部带上，供面板里直接切换。
						if len(matched) > 1 {
							res.Alternatives = matched
						}
					}
				}
				if target == "" {
					target = strconv.FormatInt(match.EpisodeID, 10)
					targetBase = official
				}
			}
		}
	}

	// 2) 按播放的文件名 + 集数搜索（keyword 手动覆盖时跳过，直接走第 3 层）。
	if target == "" && !manualKeyword && media != nil && media.Path != "" {
		if fileName := danmakuMatchFileName(media.Path); fileName != "" && fileName != term.name {
			if candidates, base, err := s.searchCandidatesWithSource(ctx, configured, official, fileName, term.episode); err == nil &&
				len(candidates) == 1 && len(candidates[0].Episodes) > 0 {
				target = fmt.Sprintf("%d", candidates[0].Episodes[0].EpisodeID)
				targetBase = base
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
		candidates, base, err := s.searchCandidatesWithSource(ctx, configured, official, term.name, term.episode)
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
		targetBase = base
		res.AnimeTitle = candidates[0].AnimeTitle
		res.EpisodeTitle = candidates[0].Episodes[0].EpisodeTitle
		res.EpisodeID = candidates[0].Episodes[0].EpisodeID
		res.MatchMode = "search"
	}

	raw, st, err := s.fetchCommentWithFallback(ctx, targetBase, official, target)
	if err != nil {
		s.log.Warn("danmaku comment fetch failed", zap.String("media_id", mediaID), zap.String("episode_id", target), zap.Error(err))
		return res, err
	}
	// 第三方目录里存在该集，不代表它真的收录了弹幕（实测部分条目返回
	// count=0）。这种「拿到空库」的情况要用官方 episodeId 再试一次，否则
	// 重定位后反而会静默变成无弹幕 —— 旧写法是靠官方 ID 撞 404 才走到官方
	// 兜底的，改用重定位就必须显式补上这一步。
	if hashOfficialID != 0 && danmakuCommentCount(raw) == 0 {
		officialTarget := strconv.FormatInt(hashOfficialID, 10)
		if officialRaw, officialType, officialErr := s.fetchCommentFromBase(ctx, official, officialTarget); officialErr == nil &&
			danmakuCommentCount(officialRaw) > 0 {
			raw, st = officialRaw, officialType
			res.EpisodeID = hashOfficialID
		} else if officialErr != nil {
			s.log.Debug("danmaku official fallback for empty configured library failed",
				zap.String("episode_id", officialTarget), zap.Error(officialErr))
		}
	}
	// 合并多来源：仅在上游确实返回了多个同集来源时才有意义。
	if opts.MergeSources && len(res.Alternatives) > 1 {
		if mergedRaw, mergedCount, ok := s.mergeAlternativeSources(ctx, targetBase, res.Alternatives, res.EpisodeID, raw); ok {
			raw, st = mergedRaw, "json"
			res.MergedSources = mergedCount
		}
	}
	res.Raw, res.SourceType = raw, st
	return res, nil
}

// mergeAlternativeSources 并发抓取同一集的多个来源，按「时间 + 内容」去重后
// 合并成单个载荷。已有载荷（existingRaw）会被复用，避免重复请求。
// 返回合并后的载荷、实际参与合并的来源数与是否成功。
func (s *DanmakuService) mergeAlternativeSources(ctx context.Context, base string, alternatives []DanmakuAnime, existingID int64, existingRaw string) (string, int, bool) {
	ids := make([]int64, 0, len(alternatives)+1)
	seen := map[int64]bool{}
	if existingID > 0 {
		seen[existingID] = true
		ids = append(ids, existingID)
	}
	for _, anime := range alternatives {
		for _, ep := range anime.Episodes {
			if ep.EpisodeID <= 0 || seen[ep.EpisodeID] {
				continue
			}
			seen[ep.EpisodeID] = true
			ids = append(ids, ep.EpisodeID)
			if len(ids) >= danmakuMergeMaxSources {
				break
			}
		}
		if len(ids) >= danmakuMergeMaxSources {
			break
		}
	}
	// 少于两个来源时无需合并。
	if len(ids) < 2 {
		return "", 0, false
	}

	var (
		mu        sync.Mutex
		collected [][]danmakuComment
		wg        sync.WaitGroup
		sem       = make(chan struct{}, danmakuMergeConcurrency)
	)
	collect := func(raw string) {
		comments := parseDanmakuComments(raw)
		if len(comments) == 0 {
			return
		}
		mu.Lock()
		collected = append(collected, comments)
		mu.Unlock()
	}
	for _, id := range ids {
		if id == existingID && existingRaw != "" {
			collect(existingRaw)
			continue
		}
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			body, _, err := s.fetchCommentFromBase(ctx, base, strconv.FormatInt(id, 10))
			if err != nil {
				// 单个来源失败不影响整体合并，静默跳过。
				return
			}
			collect(body)
		}(id)
	}
	wg.Wait()

	if len(collected) < 2 {
		return "", 0, false
	}
	merged := mergeDanmakuComments(collected)
	if len(merged) == 0 {
		return "", 0, false
	}
	encoded := encodeDanmakuComments(merged)
	if encoded == "" {
		return "", 0, false
	}
	return encoded, len(collected), true
}

// fetchCommentFromBase 从单个源拉取弹幕，不做任何回退。
func (s *DanmakuService) fetchCommentFromBase(ctx context.Context, base, target string) (raw, sourceType string, err error) {
	raw, err = s.fetchBody(ctx, fmt.Sprintf("%s/api/v2/comment/%s?withRelated=true", base, target), true)
	if err != nil {
		return "", "auto", err
	}
	return raw, detectDanmakuSourceType(raw), nil
}

// danmakuCommentCount 估算弹幕条数，用于判断某个源是否真的返回了内容。
// 同时兼容 dandanplay JSON 与 Bilibili XML 两种载荷。
func danmakuCommentCount(raw string) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	switch {
	case strings.HasPrefix(trimmed, "{"):
		var payload struct {
			Count    int               `json:"count"`
			Comments []json.RawMessage `json:"comments"`
		}
		if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
			return 0
		}
		if payload.Count > 0 {
			return payload.Count
		}
		return len(payload.Comments)
	case strings.HasPrefix(trimmed, "<"):
		return strings.Count(trimmed, "<d ")
	default:
		return 0
	}
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

// fetchCommentWithFallback fetches a comment library from primary first (when
// set and different from official), falling back to the official endpoint on
// failure. primary must be the source that issued target: episode ids are only
// meaningful inside the source that produced them.
func (s *DanmakuService) fetchCommentWithFallback(ctx context.Context, primary, official, target string) (raw, sourceType string, err error) {
	var bases []string
	if primary != "" && !sameDanmakuBase(primary, official) {
		bases = append(bases, primary)
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

// searchCandidatesWithSource searches the configured source first (when set
// and different from official), falling back to the official endpoint on
// failure. It also reports which base produced the hits, because the caller
// must fetch comments from that same base — episode ids are source-local.
func (s *DanmakuService) searchCandidatesWithSource(ctx context.Context, configured, official, name, episode string) ([]DanmakuAnime, string, error) {
	if configured != "" && !sameDanmakuBase(configured, official) {
		candidates, err := s.searchCandidates(ctx, configured, name, episode)
		if err == nil {
			return candidates, configured, nil
		}
		s.log.Warn("danmaku search failed on configured source, falling back to official", zap.String("source", configured), zap.Error(err))
	}
	candidates, err := s.searchCandidates(ctx, official, name, episode)
	return candidates, official, err
}

// danmakuEpisodeNumberRE 从「第N话 / 第N話 / 第N集」里取出集数。各源标题格式
// 不一（有的只有集数、有的带副标题），正则只认集数标记本身。
var danmakuEpisodeNumberRE = regexp.MustCompile(`第\s*(\d+(?:\.\d+)?)\s*[话話集]`)

// danmakuEpisodeNumber 返回标题中的集数，取不到时返回空串。
func danmakuEpisodeNumber(title string) string {
	m := danmakuEpisodeNumberRE.FindStringSubmatch(title)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// danmakuEpisodeSubtitle 返回「第N话」之后的副标题，用于区分同名前作/续作。
func danmakuEpisodeSubtitle(title string) string {
	loc := danmakuEpisodeNumberRE.FindStringIndex(title)
	if loc == nil {
		return ""
	}
	return strings.TrimSpace(title[loc[1]:])
}

// normalizeDanmakuText 去掉标点与空白，便于跨源比对副标题。注意它只做字符
// 归一化，不剥离集数标记 —— 调用方传入的可能已经是剥离后的副标题。
func normalizeDanmakuText(s string) []rune {
	out := make([]rune, 0, 32)
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, r)
		}
	}
	return out
}

// danmakuSubtitleMatches 判断两个源的副标题是否指向同一集。
//
// 完全一致直接判定相同，且不设长度门槛 —— 中文副标题常常只有两三个字
// （「辛」「序曲」），但它们本身就是很强的标识。只有在做包含/前缀这类模糊
// 比对时才要求足够长度，避免短串误配到别的集。同一集在不同源的副标题长度
// 也可能不同（一侧带 "-Fractal Androgynous-" 之类后缀），故保留模糊分支。
func danmakuSubtitleMatches(candidateTitle, subtitle string) bool {
	// candidateTitle 是完整标题，subtitle 已经剥离过集数标记，两者处理方式不同。
	a := normalizeDanmakuText(danmakuEpisodeSubtitle(candidateTitle))
	b := normalizeDanmakuText(subtitle)
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if string(a) == string(b) {
		return true
	}
	const fuzzyMinRunes = 4
	if len(a) < fuzzyMinRunes || len(b) < fuzzyMinRunes {
		return false
	}
	if strings.Contains(string(a), string(b)) || strings.Contains(string(b), string(a)) {
		return true
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n > 12 {
		n = 12
	}
	return string(a[:n]) == string(b[:n])
}

// lookupConfiguredEpisodes 用官方 match 给出的「剧名 + 集数」在配置源里重新
// 定位同一集，返回配置源里所有指向该集的结果。
//
// 必须重定位：各源 episodeId 空间互相独立，官方 ID（如 135500001）在第三方
// 源上只是一个不存在的编号，直接请求必然 404。官方 match 返回的 animeTitle /
// episodeTitle 正好提供了跨源检索所需的剧名、集数与副标题。
//
// 返回列表而非单条：LogVar 这类源聚合了多个视频网站，同一集常有多个库，
// 调用方取第一条自动加载，其余作为可切换来源交给用户。
func (s *DanmakuService) lookupConfiguredEpisodes(ctx context.Context, configured string, m danmakuMatch) ([]DanmakuAnime, bool) {
	episodeNum := danmakuEpisodeNumber(m.EpisodeTitle)
	if episodeNum == "" || strings.TrimSpace(m.AnimeTitle) == "" {
		return nil, false
	}
	candidates, err := s.searchCandidates(ctx, configured, m.AnimeTitle, episodeNum)
	if err != nil {
		s.log.Debug("danmaku configured lookup failed",
			zap.String("source", configured),
			zap.String("anime", m.AnimeTitle),
			zap.Error(err))
		return nil, false
	}
	matched := matchDanmakuEpisodes(candidates, episodeNum, m.EpisodeTitle)
	if len(matched) == 0 {
		return nil, false
	}
	return matched, true
}

// firstDanmakuEpisodeID 返回匹配列表里的第一条 episodeId，作为自动选中的库。
func firstDanmakuEpisodeID(matched []DanmakuAnime) int64 {
	for _, anime := range matched {
		for _, ep := range anime.Episodes {
			if ep.EpisodeID > 0 {
				return ep.EpisodeID
			}
		}
	}
	return 0
}

// pickDanmakuEpisodeID 从配置源的搜索结果里挑出与目标集最匹配的一条。
//
// 搜索按「剧名+集数」返回，但同名不同季/不同版本会同时命中，且顺序不保证
// 正确：实测「命运石之门 第18话」首条是《命运石之门 0(2018)》、《战区88 OVA
// 第1话》首条是《战区88(2004) TV》，两者内容都不对，只有副标题能区分。因此：
//   - 目标带副标题时，必须找到副标题一致的候选，否则放弃（宁可回退官方，
//     也不能给用户放错番的弹幕）；
//   - 目标没有副标题（如「第11话」）时，退而要求集数一致。
func pickDanmakuEpisodeID(candidates []DanmakuAnime, episodeNum, episodeTitle string) (int64, bool) {
	if id := firstDanmakuEpisodeID(matchDanmakuEpisodes(candidates, episodeNum, episodeTitle)); id != 0 {
		return id, true
	}
	return 0, false
}

// matchDanmakuEpisodes 在搜索结果里筛出所有指向目标集的结果，保留原有的番剧
// 分组结构（只留下命中的集数），因此调用方既能取第一条自动加载，也能把整个
// 列表作为可切换来源展示。
func matchDanmakuEpisodes(candidates []DanmakuAnime, episodeNum, episodeTitle string) []DanmakuAnime {
	subtitle := danmakuEpisodeSubtitle(episodeTitle)
	out := make([]DanmakuAnime, 0, len(candidates))
	for _, anime := range candidates {
		hits := make([]DanmakuEpisode, 0, len(anime.Episodes))
		for _, ep := range anime.Episodes {
			if ep.EpisodeID <= 0 || danmakuEpisodeNumber(ep.EpisodeTitle) != episodeNum {
				continue
			}
			// 目标带副标题时必须副标题一致；没有副标题时仅凭集数匹配。
			if subtitle != "" && !danmakuSubtitleMatches(ep.EpisodeTitle, subtitle) {
				continue
			}
			hits = append(hits, ep)
		}
		if len(hits) == 0 {
			continue
		}
		anime.Episodes = hits
		out = append(out, anime)
	}
	return out
}
