package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

func TestHLSPlaybackScopesIsolateSegmentReadsAndStop(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cache.CacheDir = t.TempDir()
	transcoder := NewTranscoderService(cfg, zap.NewNop(), nil, nil)
	svc := NewStreamService(cfg, zap.NewNop(), nil, transcoder)
	a := HLSPlaybackScope{UserID: "user-a", SessionID: "tab-a"}
	b := HLSPlaybackScope{UserID: "user-b", SessionID: "tab-a"}
	c := HLSPlaybackScope{UserID: "user-a", SessionID: "tab-c"}
	for _, scope := range []HLSPlaybackScope{a, b, c} {
		key := scope.jobKey("movie")
		dir := filepath.Join(cfg.Cache.CacheDir, key, "run-current")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "seg_00000.ts"), []byte(scope.UserID+"/"+scope.SessionID), 0o600); err != nil {
			t.Fatal(err)
		}
		transcoder.jobs[key] = &hlsJob{mediaID: "movie", jobKey: key, outputDir: dir, seekGen: 100, cancel: func() {}}
	}
	for _, scope := range []HLSPlaybackScope{a, b, c} {
		req := httptest.NewRequest(http.MethodGet, "/api/hls/movie/seg_00000.ts?_seek=100", nil)
		rec := httptest.NewRecorder()
		if err := svc.ServeHLSSegment(rec, req, "movie", "seg_00000.ts", scope); err != nil {
			t.Fatal(err)
		}
		if got, want := rec.Body.String(), scope.UserID+"/"+scope.SessionID; got != want {
			t.Fatalf("segment=%q, want %q", got, want)
		}
	}
	transcoder.StopPlayback("movie", a)
	if transcoder.jobs[a.jobKey("movie")] != nil {
		t.Fatal("stopped scope is still active")
	}
	if transcoder.jobs[b.jobKey("movie")] == nil || transcoder.jobs[c.jobKey("movie")] == nil {
		t.Fatal("stopping one player affected another user/tab")
	}
	// A delayed stop from a previous local-playback session must not affect a replacement.
	transcoder.StopPlayback("movie", a)
	if transcoder.jobs[c.jobKey("movie")] == nil {
		t.Fatal("old stop cancelled a new session")
	}
}

func TestHLSPlaybackRejectsStaleSegmentGeneration(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cache.CacheDir = t.TempDir()
	transcoder := NewTranscoderService(cfg, zap.NewNop(), nil, nil)
	svc := NewStreamService(cfg, zap.NewNop(), nil, transcoder)
	scope := HLSPlaybackScope{UserID: "viewer", SessionID: "tab"}
	key := scope.jobKey("movie")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seg_00000.ts"), []byte("new-generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcoder.jobs[key] = &hlsJob{mediaID: "movie", jobKey: key, outputDir: dir, seekGen: 200, cancel: func() {}}
	req := httptest.NewRequest(http.MethodGet, "/api/hls/movie/seg_00000.ts?_seek=100", nil)
	rec := httptest.NewRecorder()
	if err := svc.ServeHLSSegment(rec, req, "movie", "seg_00000.ts", scope); err == nil {
		t.Fatal("stale segment request read a replacement generation")
	}
	if rec.Body.Len() != 0 {
		t.Fatal("stale request exposed replacement bytes")
	}
}

func TestHLSPlaybackScopeRequiresServerOwner(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?session_id=tab&user_id=attacker-controlled", nil)
	if _, err := HLSPlaybackScopeForRequest(req, ""); err == nil {
		t.Fatal("missing authenticated owner was accepted")
	}
	a, err := HLSPlaybackScopeForRequest(req, "real-owner")
	if err != nil {
		t.Fatal(err)
	}
	if a.UserID != "real-owner" {
		t.Fatal("client parameter supplied the owner")
	}
	req = httptest.NewRequest(http.MethodGet, "/?PlaySessionId=emby-session", nil)
	b, err := HLSPlaybackScopeForRequest(req, "real-owner")
	if err != nil || b.SessionID == "" {
		t.Fatalf("Emby session must stay scoped: %#v %v", b, err)
	}
}
