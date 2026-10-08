package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCloud115HLSProxyRewriteManifest(t *testing.T) {
	proxy := &Cloud115HLSProxy{sessions: map[string]*cloud115HLSSession{}}
	session := &cloud115HLSSession{
		ID:      "sess",
		MediaID: "media-1",
		entries: map[string]string{},
	}
	manifest := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1800000,RESOLUTION=1280x720\nhttps://cpats01.115.com/a.m3u8?u=1&se=2\n"
	rewritten := proxy.rewriteManifest(session, manifest, "http://videoplay.115.com/m3u8/pc", "token=t&media_id=media-1")
	if strings.Contains(rewritten, "cpats01.115.com") {
		t.Fatalf("upstream URL leaked into rewritten manifest: %s", rewritten)
	}
	wantKey := cloud115HLSKeyForURL("https://cpats01.115.com/a.m3u8?u=1&se=2")
	if !strings.Contains(rewritten, "/api/cloud115/hls/sess/"+wantKey+"?") {
		t.Fatalf("proxy URL missing: %s", rewritten)
	}
	if !strings.Contains(rewritten, "media_id=media-1") || !strings.Contains(rewritten, "token=t") {
		t.Fatalf("auth/media query missing: %s", rewritten)
	}
	if len(session.entries) != 1 {
		t.Fatalf("session entries = %d, want 1", len(session.entries))
	}
}

func TestCloud115HLSProxyRewriteIsStableAcrossRewrites(t *testing.T) {
	proxy := &Cloud115HLSProxy{sessions: map[string]*cloud115HLSSession{}}
	session := &cloud115HLSSession{
		ID:      "sess",
		MediaID: "media-1",
		entries: map[string]string{},
	}
	manifest := "#EXTM3U\n#EXT-X-TARGETDURATION:10\n" +
		"https://cpats01.115.com/seg0.ts?x=0\nhttps://cpats01.115.com/seg1.ts?x=1\n"

	first := proxy.rewriteManifest(session, manifest, "https://cpats01.115.com/v.m3u8", "media_id=media-1")
	entriesAfterFirst := len(session.entries)
	second := proxy.rewriteManifest(session, manifest, "https://cpats01.115.com/v.m3u8", "media_id=media-1")

	// 同一分片在每次重写里都必须是同一个 key，客户端拿到的地址才不会无谓漂移。
	if first != second {
		t.Fatalf("rewrite is not stable:\nfirst  = %s\nsecond = %s", first, second)
	}
	if len(session.entries) != entriesAfterFirst {
		t.Fatalf("entries grew on rewrite: %d -> %d", entriesAfterFirst, len(session.entries))
	}
	if entriesAfterFirst != 2 {
		t.Fatalf("session entries = %d, want 2", entriesAfterFirst)
	}
}

func TestCloud115HLSProxyRewriteKeyURI(t *testing.T) {
	proxy := &Cloud115HLSProxy{sessions: map[string]*cloud115HLSSession{}}
	session := &cloud115HLSSession{
		ID:      "sess",
		MediaID: "media-1",
		entries: map[string]string{},
	}
	manifest := `#EXTM3U
#EXT-X-KEY:METHOD=AES-128,URI="https://cpats01.115.com/key?k=1"
#EXTINF:10.0,
https://cpats01.115.com/seg.ts?x=1
`
	rewritten := proxy.rewriteManifest(session, manifest, "https://cpats01.115.com/v.m3u8", "token=t&media_id=media-1")
	if strings.Contains(rewritten, "cpats01.115.com") {
		t.Fatalf("upstream URL leaked into rewritten key manifest: %s", rewritten)
	}
	if len(session.entries) != 2 {
		t.Fatalf("session entries = %d, want 2", len(session.entries))
	}
}

// 长视频 VOD 播放期间只有分片请求经过 lookup，会话必须随活动滑动续期，
// 否则固定 30 分钟 TTL 一到就会在中途 404 断流。
func TestCloud115HLSProxyLookupRenewsSession(t *testing.T) {
	proxy := &Cloud115HLSProxy{sessions: map[string]*cloud115HLSSession{}}
	session := &cloud115HLSSession{
		ID:      "sess",
		MediaID: "media-1",
		entries: map[string]string{"e1": "https://cpats01.115.com/v.m3u8"},
	}
	proxy.storeSession(session)
	// 模拟播放进行到第 29 分钟：距过期只剩 1 分钟。
	session.ExpiresAt = time.Now().Add(time.Minute)

	if _, _, ok := proxy.lookup(session.ID, "e1"); !ok {
		t.Fatal("active session should still resolve before expiry")
	}
	if remaining := time.Until(session.ExpiresAt); remaining < cloud115HLSSessionTTL-10*time.Second {
		t.Fatalf("session not renewed on activity, remaining = %v", remaining)
	}

	// 真正过期（期间无任何活动）的会话仍要被清理。
	session.ExpiresAt = time.Now().Add(-time.Minute)
	if _, _, ok := proxy.lookup(session.ID, "e1"); ok {
		t.Fatal("expired session should not resolve")
	}
	proxy.mu.Lock()
	_, exists := proxy.sessions[session.ID]
	proxy.mu.Unlock()
	if exists {
		t.Fatal("expired session should be evicted from the map")
	}
}

// 满员驱逐必须淘汰最久未活跃的会话（ExpiresAt = 最近活跃 + TTL，最小者即
// 最久未活跃），而不是随机挑一个——随机驱逐可能正好杀掉正在播放的会话。
func TestCloud115HLSProxyEvictsLeastRecentlyActiveSession(t *testing.T) {
	proxy := &Cloud115HLSProxy{sessions: map[string]*cloud115HLSSession{}}
	first := &cloud115HLSSession{ID: "active", MediaID: "media-1", entries: map[string]string{}}
	proxy.storeSession(first)
	for i := 0; i < cloud115HLSMaxSessions-1; i++ {
		proxy.storeSession(&cloud115HLSSession{ID: fmt.Sprintf("s-%03d", i), MediaID: "media-1", entries: map[string]string{}})
	}
	// 人为拉开活跃时间差，避免依赖时钟精度：active 最近活跃，s-000 最久未活跃。
	base := time.Now()
	first.ExpiresAt = base.Add(cloud115HLSSessionTTL)
	proxy.sessions["s-000"].ExpiresAt = base.Add(cloud115HLSSessionTTL - time.Hour)

	proxy.storeSession(&cloud115HLSSession{ID: "newcomer", MediaID: "media-1", entries: map[string]string{}})

	proxy.mu.Lock()
	_, activeExists := proxy.sessions["active"]
	_, staleExists := proxy.sessions["s-000"]
	proxy.mu.Unlock()
	if !activeExists {
		t.Fatal("recently active session must survive eviction")
	}
	if staleExists {
		t.Fatal("least recently active session should have been evicted")
	}
}

func TestIsAllowed115UpstreamHost(t *testing.T) {
	for _, host := range []string{"videoplay.115.com", "cpats01.115.com", "cdn.115cdn.net"} {
		if !isAllowed115UpstreamHost(host) {
			t.Fatalf("host %q should be allowed", host)
		}
	}
	for _, host := range []string{"", "evil.example.com", "115.com.evil.example.com"} {
		if isAllowed115UpstreamHost(host) {
			t.Fatalf("host %q should be rejected", host)
		}
	}
}

func TestCloud115HLSProxyServeChildRewritesVariant(t *testing.T) {
	proxy := &Cloud115HLSProxy{
		sessions: map[string]*cloud115HLSSession{},
	}
	proxy.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := "#EXTM3U\n#EXT-X-TARGETDURATION:10\nhttps://cpats01.115.com/seg.ts?x=1\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/vnd.apple.mpegurl"},
			},
			Body:    io.NopCloser(strings.NewReader(body)),
			Request: req,
		}, nil
	})}
	session := &cloud115HLSSession{
		ID:        "sess",
		MediaID:   "media-1",
		ExpiresAt: time.Now().Add(time.Hour),
		entries:   map[string]string{"e1": "https://cpats01.115.com/v.m3u8"},
	}
	proxy.sessions[session.ID] = session
	req := httptest.NewRequest(http.MethodGet, "/api/cloud115/hls/sess/e1?media_id=media-1&token=t", nil)
	rec := httptest.NewRecorder()
	if err := proxy.ServeChild(context.Background(), rec, req, session.ID, "e1"); err != nil {
		t.Fatalf("ServeChild: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	wantKey := cloud115HLSKeyForURL("https://cpats01.115.com/seg.ts?x=1")
	if !strings.Contains(rec.Body.String(), "/api/cloud115/hls/sess/"+wantKey+"?") {
		t.Fatalf("rewritten variant missing proxy segment: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "cpats01.115.com") {
		t.Fatalf("upstream URL leaked in variant: %s", rec.Body.String())
	}
}

func TestCloud115HLSProxyServeChildStreamsRange(t *testing.T) {
	payload := []byte("segment-bytes")
	proxy := &Cloud115HLSProxy{
		sessions: map[string]*cloud115HLSSession{},
	}
	proxy.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Content-Type":   []string{"video/mp2t"},
				"Content-Range":  []string{"bytes 0-12/100"},
				"Content-Length": []string{strconv.Itoa(len(payload))},
			},
			Body:    io.NopCloser(bytes.NewReader(payload)),
			Request: req,
		}, nil
	})}
	session := &cloud115HLSSession{
		ID:        "sess",
		MediaID:   "media-1",
		ExpiresAt: time.Now().Add(time.Hour),
		entries:   map[string]string{"e1": "https://cpats01.115.com/seg.ts?x=1"},
	}
	proxy.sessions[session.ID] = session
	req := httptest.NewRequest(http.MethodGet, "/api/cloud115/hls/sess/e1?media_id=media-1&token=t", nil)
	req.Header.Set("Range", "bytes=0-12")
	rec := httptest.NewRecorder()
	if err := proxy.ServeChild(context.Background(), rec, req, session.ID, "e1"); err != nil {
		t.Fatalf("ServeChild: %v", err)
	}
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if rec.Header().Get("Content-Range") != "bytes 0-12/100" {
		t.Fatalf("content-range = %q", rec.Header().Get("Content-Range"))
	}
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("payload = %q, want %q", rec.Body.Bytes(), payload)
	}
}
