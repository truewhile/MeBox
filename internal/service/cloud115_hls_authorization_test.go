package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCloud115HLSChildRejectsMismatchedSessionMediaBeforeFetch(t *testing.T) {
	fetched := false
	proxy := &Cloud115HLSProxy{sessions: map[string]*cloud115HLSSession{}}
	proxy.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		fetched = true
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"video/mp2t"}}, Body: io.NopCloser(strings.NewReader("private-segment")), Request: req}, nil
	})}
	proxy.sessions["session-b"] = &cloud115HLSSession{
		ID: "session-b", MediaID: "media-b", ExpiresAt: time.Now().Add(time.Hour),
		entries: map[string]string{"segment": "https://cpats01.115.com/seg.ts"},
	}
	for _, query := range []string{"?media_id=media-a", ""} {
		fetched = false
		req := httptest.NewRequest(http.MethodGet, "/api/cloud115/hls/session-b/segment"+query, nil)
		rec := httptest.NewRecorder()
		err := proxy.ServeChild(context.Background(), rec, req, "session-b", "segment")
		if err == nil || fetched {
			t.Errorf("query %q must reject the wrong/missing media before fetch; err=%v fetched=%v", query, err, fetched)
		}
	}
}
