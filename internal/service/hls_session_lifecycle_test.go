package service

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func cachedScopedHLS(t *testing.T, scopes ...HLSPlaybackScope) (*StreamService, *TranscoderService, map[string]string) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Cache.CacheDir = t.TempDir()
	cfg.Transcoder.Enabled = true
	db := newServiceTestDB(t)
	if err := db.Create(&model.Media{Base: model.Base{ID: "movie"}, Path: "unused-cached-source", DurationSec: 60}).Error; err != nil {
		t.Fatal(err)
	}
	transcoder := NewTranscoderService(cfg, zap.NewNop(), repository.New(db), nil)
	paths := map[string]string{}
	for _, scope := range scopes {
		key := scope.jobKey("movie")
		dir := filepath.Join(transcoder.HLSDir(key), "run-cached")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		playlist := filepath.Join(dir, "index.m3u8")
		if err := os.WriteFile(playlist, []byte("#EXTM3U\n#EXTINF:4,\nseg_00000.ts\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "seg_00000.ts"), []byte(scope.UserID+"/"+scope.SessionID), 0o600); err != nil {
			t.Fatal(err)
		}
		transcoder.jobs[key] = &hlsJob{mediaID: "movie", jobKey: key, outputDir: dir, subtitleStream: -1, startedAt: time.Now(), cancel: func() {}}
		paths[key] = playlist
	}
	return NewStreamService(cfg, zap.NewNop(), repository.New(db), transcoder), transcoder, paths
}

func TestHLSPlaybackEnsureReusesOnlyMatchingOwnerAndSession(t *testing.T) {
	a := HLSPlaybackScope{UserID: "viewer", SessionID: "tab-a"}
	b := HLSPlaybackScope{UserID: "viewer", SessionID: "tab-b"}
	_, transcoder, paths := cachedScopedHLS(t, a, b)
	for _, scope := range []HLSPlaybackScope{a, b} {
		playlist, err := transcoder.EnsureJobFromSubtitleQuality(t.Context(), "movie", 0, 0, -1, "", scope)
		if err != nil {
			t.Fatal(err)
		}
		if playlist != paths[scope.jobKey("movie")] {
			t.Fatalf("scope %#v reused another session's playlist %q", scope, playlist)
		}
	}
}

func TestHLSPlaybackStopRemovesOnlyItsSessionOutput(t *testing.T) {
	a := HLSPlaybackScope{UserID: "viewer", SessionID: "tab-a"}
	b := HLSPlaybackScope{UserID: "viewer", SessionID: "tab-b"}
	_, transcoder, paths := cachedScopedHLS(t, a, b)
	transcoder.StopPlayback("movie", a)
	if _, err := os.Stat(paths[a.jobKey("movie")]); !os.IsNotExist(err) {
		t.Fatalf("stopped session output must be cleaned up, err=%v", err)
	}
	if _, err := os.Stat(paths[b.jobKey("movie")]); err != nil {
		t.Fatalf("another player's output was removed: %v", err)
	}
}

func TestHLSPlaybackIdleCleanupRemovesExpiredSessionOutput(t *testing.T) {
	a := HLSPlaybackScope{UserID: "viewer", SessionID: "tab-a"}
	b := HLSPlaybackScope{UserID: "viewer", SessionID: "tab-b"}
	_, transcoder, paths := cachedScopedHLS(t, a, b)
	job := transcoder.jobs[a.jobKey("movie")]
	job.lastAccess = time.Now().Add(-2 * time.Minute)
	if !transcoder.stopIdleJob(job, time.Minute) {
		t.Fatal("expired session was not stopped")
	}
	if _, err := os.Stat(paths[a.jobKey("movie")]); !os.IsNotExist(err) {
		t.Fatalf("expired output was retained: %v", err)
	}
	if _, err := os.Stat(paths[b.jobKey("movie")]); err != nil {
		t.Fatalf("active session output was removed: %v", err)
	}
}

func TestHLSPlaybackPropagatesHeaderDeviceIdentityToSegments(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/hls/movie/index.m3u8?token=test", nil)
	req.Header.Set("X-Emby-Device-Id", "legacy-device")
	scope, err := HLSPlaybackScopeForRequest(req, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	svc, _, _ := cachedScopedHLS(t, scope)
	rec := httptest.NewRecorder()
	if err := svc.ServeHLSPlaylist(rec, req, "movie", scope); err != nil {
		t.Fatal(err)
	}
	var segment string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "seg_") {
			segment = line
			break
		}
	}
	u, err := url.Parse(segment)
	if err != nil || u.Query().Get("session_id") == "" {
		t.Fatalf("playlist must carry the header-only device scope to child URLs: %q err=%v", segment, err)
	}
	child := httptest.NewRequest(http.MethodGet, "/api/hls/movie/"+segment, nil)
	childScope, err := HLSPlaybackScopeForRequest(child, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if childScope.jobKey("movie") != scope.jobKey("movie") {
		t.Fatal("child request changed its owner's device scope")
	}
	childRec := httptest.NewRecorder()
	if err := svc.ServeHLSSegment(childRec, child, "movie", "seg_00000.ts", childScope); err != nil {
		t.Fatal(err)
	}
	if childRec.Body.String() != "viewer/"+scope.SessionID {
		t.Fatalf("wrong scoped segment: %s", childRec.Body.String())
	}
}
