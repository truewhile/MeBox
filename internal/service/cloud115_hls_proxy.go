package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/truewhile/MeBox/internal/service/cloud115"
)

var (
	ErrCloud115HLSSessionNotFound = errors.New("115 hls session not found")
	ErrCloud115HLSUpstreamExpired = errors.New("115 hls upstream expired")
)

const (
	cloud115HLSSessionTTL  = 30 * time.Minute
	cloud115HLSMaxSessions = 256
	cloud115HLSMaxManifest = 8 << 20
)

type cloud115HLSSession struct {
	ID         string
	MediaID    string
	Definition int
	CreatedAt  time.Time
	ExpiresAt  time.Time

	mu      sync.Mutex
	entries map[string]string
	next    int
}

// Cloud115HLSProxy 把 115 云端 HLS 转成 MeBox 同源 HLS。
//
// 浏览器不能直接请求 115 的 m3u8：master/variant/分片的 CORS 只允许
// https://115.com，且 master 还是 HTTP。代理在服务端拉取并重写播放列表，
// 分片按 Range 流式转发。
type Cloud115HLSProxy struct {
	service *Cloud115PlaybackService
	client  *http.Client

	mu       sync.Mutex
	sessions map[string]*cloud115HLSSession
}

func newCloud115HLSProxy(service *Cloud115PlaybackService) *Cloud115HLSProxy {
	return &Cloud115HLSProxy{
		service: service,
		client: &http.Client{
			Timeout: 0,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 6 {
					return errors.New("stopped after 6 redirects")
				}
				return nil
			},
		},
		sessions: make(map[string]*cloud115HLSSession),
	}
}

// ServeMaster 解析指定清晰度并返回重写后的 master.m3u8。
func (p *Cloud115HLSProxy) ServeMaster(ctx context.Context, w http.ResponseWriter, r *http.Request, mediaID string, definition int) error {
	if p == nil || p.service == nil {
		return ErrCloud115NotApplicable
	}
	upstream, _, err := p.service.ResolveCloud115URL(ctx, mediaID, definition)
	if err != nil {
		return err
	}
	session := p.newSession(mediaID, definition)
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := p.fetchUpstream(fetchCtx, upstream, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("115 云端播放列表返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cloud115HLSMaxManifest))
	if err != nil {
		return err
	}
	baseURL := upstream
	if resp.Request != nil && resp.Request.URL != nil {
		baseURL = resp.Request.URL.String()
	}
	rewritten := p.rewriteManifest(session, string(body), baseURL, r.URL.RawQuery)
	p.storeSession(session)

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
	if r.Method != http.MethodHead {
		_, err = io.WriteString(w, rewritten)
	}
	return err
}

// ServeChild 代理 variant/分片；variant 播放列表会继续重写为同源地址。
func (p *Cloud115HLSProxy) ServeChild(ctx context.Context, w http.ResponseWriter, r *http.Request, sessionID, key string) error {
	session, upstream, ok := p.lookup(sessionID, key)
	if !ok {
		return ErrCloud115HLSSessionNotFound
	}
	resp, err := p.fetchUpstream(ctx, upstream, r.Header.Get("Range"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusGone {
		p.deleteSession(sessionID)
		return ErrCloud115HLSUpstreamExpired
	}

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	isPlaylist := strings.Contains(contentType, "mpegurl") ||
		strings.Contains(contentType, "application/vnd.apple.mpegurl")

	if !isPlaylist {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			return readErr
		}
		if strings.HasPrefix(strings.TrimSpace(string(body)), "#EXTM3U") {
			isPlaylist = true
			resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(string(body)), resp.Body))
		} else {
			resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(string(body)), resp.Body))
		}
	}

	copyUpstreamHeaders(w, resp, isPlaylist)
	if isPlaylist {
		body, err := io.ReadAll(io.LimitReader(resp.Body, cloud115HLSMaxManifest))
		if err != nil {
			return err
		}
		baseURL := upstream
		if resp.Request != nil && resp.Request.URL != nil {
			baseURL = resp.Request.URL.String()
		}
		rewritten := p.rewriteManifest(session, string(body), baseURL, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
		w.WriteHeader(resp.StatusCode)
		if r.Method != http.MethodHead {
			_, err = io.WriteString(w, rewritten)
		}
		return err
	}

	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		_, err = io.Copy(w, resp.Body)
	}
	return err
}

func (p *Cloud115HLSProxy) newSession(mediaID string, definition int) *cloud115HLSSession {
	now := time.Now()
	return &cloud115HLSSession{
		ID:         cloud115.RandomString(24),
		MediaID:    mediaID,
		Definition: definition,
		CreatedAt:  now,
		ExpiresAt:  now.Add(cloud115HLSSessionTTL),
		entries:    make(map[string]string),
	}
}

func (p *Cloud115HLSProxy) storeSession(session *cloud115HLSSession) {
	if p == nil || session == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if len(p.sessions) >= cloud115HLSMaxSessions {
		for id, existing := range p.sessions {
			if now.After(existing.ExpiresAt) {
				delete(p.sessions, id)
			}
		}
	}
	if len(p.sessions) >= cloud115HLSMaxSessions {
		for id := range p.sessions {
			delete(p.sessions, id)
			break
		}
	}
	session.ExpiresAt = now.Add(cloud115HLSSessionTTL)
	p.sessions[session.ID] = session
}

func (p *Cloud115HLSProxy) lookup(sessionID, key string) (*cloud115HLSSession, string, bool) {
	if p == nil {
		return nil, "", false
	}
	p.mu.Lock()
	session := p.sessions[sessionID]
	if session != nil && time.Now().After(session.ExpiresAt) {
		delete(p.sessions, sessionID)
		session = nil
	}
	p.mu.Unlock()
	if session == nil {
		return nil, "", false
	}
	session.mu.Lock()
	upstream, ok := session.entries[key]
	session.mu.Unlock()
	return session, upstream, ok
}

func (p *Cloud115HLSProxy) deleteSession(sessionID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.sessions, sessionID)
	p.mu.Unlock()
}

func (p *Cloud115HLSProxy) fetchUpstream(ctx context.Context, rawURL, rangeHeader string) (*http.Response, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || !isAllowed115UpstreamHost(parsed.Hostname()) {
		return nil, fmt.Errorf("115 hls upstream host not allowed: %s", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", cloud115.DefaultUA)
	if strings.TrimSpace(rangeHeader) != "" {
		req.Header.Set("Range", rangeHeader)
	}
	return p.client.Do(req)
}

func isAllowed115UpstreamHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, suffix := range []string{".115.com", ".115cdn.com", ".115cdn.net"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return host == "115.com" || host == "115cdn.com" || host == "115cdn.net"
}

func (p *Cloud115HLSProxy) rewriteManifest(session *cloud115HLSSession, text, baseURL, rawQuery string) string {
	if session == nil {
		return text
	}
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if strings.HasPrefix(trimmed, "#EXT-X-MEDIA:") ||
				strings.HasPrefix(trimmed, "#EXT-X-KEY:") ||
				strings.HasPrefix(trimmed, "#EXT-X-MAP:") {
				lines[i] = replaceManifestURI(line, func(uri string) string {
					return p.proxyURL(session, resolveManifestURL(baseURL, uri), rawQuery)
				})
			}
			continue
		}
		lines[i] = p.proxyURL(session, resolveManifestURL(baseURL, trimmed), rawQuery) + lineEnding(line)
	}
	return strings.Join(lines, "")
}

func (p *Cloud115HLSProxy) proxyURL(session *cloud115HLSSession, upstream, rawQuery string) string {
	session.mu.Lock()
	session.next++
	key := "e" + strconv.Itoa(session.next)
	session.entries[key] = upstream
	mediaID := session.MediaID
	session.mu.Unlock()

	query := childProxyQuery(rawQuery, mediaID)
	return "/api/cloud115/hls/" + url.PathEscape(session.ID) + "/" + url.PathEscape(key) + "?" + query
}

func childProxyQuery(rawQuery, mediaID string) string {
	values, _ := url.ParseQuery(rawQuery)
	keep := url.Values{}
	for _, key := range []string{"token", "api_key", "apiKey", "ApiKey", "profile_id", "profile_pin_token"} {
		if value := strings.TrimSpace(values.Get(key)); value != "" {
			keep.Set(key, value)
		}
	}
	if strings.TrimSpace(mediaID) != "" {
		keep.Set("media_id", mediaID)
	}
	return keep.Encode()
}

func replaceManifestURI(line string, replace func(string) string) string {
	const marker = `URI="`
	idx := strings.Index(line, marker)
	if idx < 0 {
		return line
	}
	start := idx + len(marker)
	end := strings.Index(line[start:], `"`)
	if end < 0 {
		return line
	}
	end += start
	return line[:start] + replace(line[start:end]) + line[end:]
}

func resolveManifestURL(baseURL, raw string) string {
	base, baseErr := url.Parse(strings.TrimSpace(baseURL))
	ref, refErr := url.Parse(strings.TrimSpace(raw))
	if baseErr != nil || refErr != nil || base == nil || ref == nil {
		return raw
	}
	return base.ResolveReference(ref).String()
}

func lineEnding(line string) string {
	if strings.HasSuffix(line, "\r\n") {
		return "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return "\n"
	}
	return ""
}

func copyUpstreamHeaders(w http.ResponseWriter, resp *http.Response, playlist bool) {
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if value := resp.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	if playlist {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	} else if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
}
