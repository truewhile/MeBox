package service

import (
	"bytes"
	"context"
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
	if !strings.Contains(rewritten, "/api/cloud115/hls/sess/e1?") {
		t.Fatalf("proxy URL missing: %s", rewritten)
	}
	if !strings.Contains(rewritten, "media_id=media-1") || !strings.Contains(rewritten, "token=t") {
		t.Fatalf("auth/media query missing: %s", rewritten)
	}
	if len(session.entries) != 1 {
		t.Fatalf("session entries = %d, want 1", len(session.entries))
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
		next:      1,
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
	if !strings.Contains(rec.Body.String(), "/api/cloud115/hls/sess/e2?") {
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
		next:      1,
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
