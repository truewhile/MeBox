package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/service/cloud115"
)

func TestCloud115ConcurrentStartsDoNotDuplicateInFlightPush(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	var pushCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pushCalls.Add(1) == 1 {
			close(started)
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"state": true, "code": 0, "data": []any{}})
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })
	oldBase := cloud115.ProAPIBase
	cloud115.ProAPIBase = server.URL
	defer func() { cloud115.ProAPIBase = oldBase }()
	client := cloud115.NewOpenClient("review-app", "access", "refresh")
	svc := NewCloud115PlaybackService(nil, zap.NewNop(), nil, nil)
	quality := PlaybackQuality{ID: "4", Label: "1080P", Source: "cloud"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	first := make(chan struct{})
	go func() { defer close(first); svc.ensureCloudTranscode(ctx, "account", "pick", quality, client) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first push did not reach the local server")
	}
	second := make(chan Cloud115TranscodeState, 1)
	go func() { second <- svc.ensureCloudTranscode(ctx, "account", "pick", quality, client) }()
	secondReceived := false
	select {
	case state := <-second:
		secondReceived = true
		if state.State != "transcoding" {
			t.Errorf("in-flight request should be reported as transcoding: %#v", state)
		}
	case <-time.After(250 * time.Millisecond):
		t.Error("second start issued another request instead of reusing the in-flight push")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal("first push did not finish")
	}
	// The duplicate path is awaited before restoring the package-wide fake API.
	if !secondReceived {
		select {
		case <-second:
		case <-ctx.Done():
			t.Fatal("second start did not finish")
		}
	}
	if got := pushCalls.Load(); got != 1 {
		t.Fatalf("pushes=%d, want 1", got)
	}
}
