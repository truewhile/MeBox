package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

var ErrInvalidHLSPlaybackScope = errors.New("invalid HLS playback scope")
var ErrHLSGenerationSuperseded = errors.New("HLS playback generation superseded")

// HLSPlaybackScope separates a media's independent users and player instances.
// UserID must come from server authentication, never from a request parameter.
type HLSPlaybackScope struct {
	UserID    string
	SessionID string
}

func HLSPlaybackScopeForRequest(r *http.Request, userID string) (HLSPlaybackScope, error) {
	scope := HLSPlaybackScope{UserID: strings.TrimSpace(userID)}
	if scope.UserID == "" || r == nil {
		return scope, ErrInvalidHLSPlaybackScope
	}
	for _, key := range []string{"session_id", "PlaySessionId", "playSessionId", "DeviceId", "deviceId"} {
		if value := strings.TrimSpace(r.URL.Query().Get(key)); value != "" {
			scope.SessionID = value
			break
		}
	}
	if scope.SessionID == "" {
		if device := strings.TrimSpace(r.Header.Get("X-Emby-Device-Id")); device != "" {
			scope.SessionID = device
		}
	}
	if len(scope.SessionID) > 256 || strings.IndexFunc(scope.SessionID, unicode.IsControl) >= 0 {
		return scope, ErrInvalidHLSPlaybackScope
	}
	// Clients without a session/device identifier remain compatible but only
	// share a legacy task with the same authenticated user, never other users.
	return scope, nil
}

func (scope HLSPlaybackScope) jobKey(mediaID string) string {
	if scope.UserID == "" && scope.SessionID == "" {
		return mediaID // Existing internal callers/tests retain their legacy API.
	}
	sum := sha256.Sum256([]byte(scope.UserID + "\x00" + mediaID + "\x00" + scope.SessionID))
	return "player-" + hex.EncodeToString(sum[:])
}

func hlsPlaybackJobKey(mediaID string, scopes []HLSPlaybackScope) string {
	if len(scopes) == 0 {
		return mediaID
	}
	return scopes[0].jobKey(mediaID)
}

func (job *hlsJob) key() string {
	if job.jobKey != "" {
		return job.jobKey
	}
	return job.mediaID
}

func (t *TranscoderService) jobPlaylistPath(job *hlsJob) string {
	dir := job.outputDir
	if dir == "" {
		dir = t.HLSDir(job.key())
	}
	return filepath.Join(dir, "index.m3u8")
}

// hlsOutputDir captures one immutable run directory. Even if a new run starts
// after this check, the request cannot accidentally open its replacement files.
func (t *TranscoderService) hlsOutputDir(key string, generation int64) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	job := t.jobs[key]
	if job == nil {
		return "", ErrMediaNotFound
	}
	if job.seekGen != generation {
		return "", ErrHLSGenerationSuperseded
	}
	t.touchJobLocked(key)
	if job.outputDir != "" {
		return job.outputDir, nil
	}
	return t.HLSDir(key), nil
}

func (t *TranscoderService) StopPlayback(mediaID string, scope HLSPlaybackScope) {
	key := scope.jobKey(mediaID)
	unlock := t.lockJobStart(key)
	defer unlock()
	t.mu.Lock()
	job := t.detachJobLocked(key)
	t.mu.Unlock()
	waitJobExit(job, 12*time.Second)
	// Keep cleanup under the same gate as start: a delayed stop must not wipe
	// an output directory created by a concurrent replacement request.
	_ = os.RemoveAll(t.HLSDir(key))
}
