package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service/cloud115"
)

func TestCloud115PlaybackInfoTriggersTranscodeOnce(t *testing.T) {
	var playCalls int32
	var pushCalls int32
	var ready int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open/video/play":
			atomic.AddInt32(&playCalls, 1)
			if atomic.LoadInt32(&ready) == 0 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"state":   false,
					"code":    0,
					"message": "转码中",
					"data":    map[string]any{},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"state":   true,
				"code":    0,
				"message": "",
				"data": map[string]any{
					"file_name": "test.mkv",
					"definition_list_new": map[string]string{
						"4": "1080P",
					},
					"video_url": []map[string]any{
						{
							"url":          "http://cdn.example/master.m3u8",
							"definition":   4,
							"definition_n": 4,
							"width":        1920,
							"height":       1080,
							"title":        "1080P",
						},
					},
				},
			})
		case "/open/video/video_push":
			atomic.AddInt32(&pushCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"state": true, "code": 0, "data": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldBase := cloud115.ProAPIBase
	cloud115.ProAPIBase = server.URL
	defer func() { cloud115.ProAPIBase = oldBase }()

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.Media{})
	repos := repository.New(db)
	strm := NewStrmService(nil, zap.NewNop(), repos, NewCryptoService("test-secret", zap.NewNop()))
	ctx := context.Background()
	account, err := strm.CreateStrmAccount(ctx, "115", model.StrmProvider115, map[string]string{
		"app_id":        "100195129",
		"access_token":  "at",
		"refresh_token": "rt",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	media := &model.Media{
		Base:      model.Base{ID: "media-1"},
		Title:     "test",
		Path:      "/media/test.strm",
		Container: "strm",
		Height:    1080,
		STRMURL:   "/api/strm/play/cloud115/video.mkv?acct=" + account.ID + "&pickcode=pc",
	}
	if err := db.Create(media).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}
	svc := NewCloud115PlaybackService(nil, zap.NewNop(), repos, strm)

	info, err := svc.PlaybackInfo(ctx, media.ID, 0)
	if err != nil {
		t.Fatalf("first playback info: %v", err)
	}
	if info.Provider != model.StrmProvider115 || info.DefaultQuality != "4" {
		t.Fatalf("unexpected playback info: %#v", info)
	}
	if info.Transcode.State != "transcoding" {
		t.Fatalf("transcode state = %q, want transcoding", info.Transcode.State)
	}
	if got := atomic.LoadInt32(&pushCalls); got != 1 {
		t.Fatalf("push calls = %d, want 1", got)
	}

	if _, err := svc.PlaybackInfo(ctx, media.ID, 0); err != nil {
		t.Fatalf("second playback info: %v", err)
	}
	if got := atomic.LoadInt32(&pushCalls); got != 1 {
		t.Fatalf("push calls after polling = %d, want 1", got)
	}

	atomic.StoreInt32(&ready, 1)
	info, err = svc.PlaybackInfo(ctx, media.ID, 0)
	if err != nil {
		t.Fatalf("ready playback info: %v", err)
	}
	if info.Transcode.State != "ready" {
		t.Fatalf("transcode state = %q, want ready", info.Transcode.State)
	}
	url, _, err := svc.ResolveCloud115URL(ctx, media.ID, 4)
	if err != nil {
		t.Fatalf("resolve cloud url: %v", err)
	}
	if url != "http://cdn.example/master.m3u8" {
		t.Fatalf("url = %q", url)
	}
	if got := atomic.LoadInt32(&playCalls); got < 3 {
		t.Fatalf("play calls = %d, want >= 3", got)
	}
}

func TestCloud115PlaybackInfoTranscodePendingBeforeReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open/video/play":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": false, "code": 0, "message": "转码中", "data": map[string]any{}})
		case "/open/video/video_push":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": true, "code": 0, "data": []any{}})
		}
	}))
	defer server.Close()
	oldBase := cloud115.ProAPIBase
	cloud115.ProAPIBase = server.URL
	defer func() { cloud115.ProAPIBase = oldBase }()

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.Media{})
	repos := repository.New(db)
	strm := NewStrmService(nil, zap.NewNop(), repos, NewCryptoService("test-secret", zap.NewNop()))
	ctx := context.Background()
	account, err := strm.CreateStrmAccount(ctx, "115", model.StrmProvider115, map[string]string{
		"app_id":        "100195129",
		"access_token":  "at",
		"refresh_token": "rt",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	media := &model.Media{
		Base:      model.Base{ID: "media-2"},
		Title:     "test",
		Path:      "/media/test2.strm",
		Container: "strm",
		Height:    1080,
		STRMURL:   "/api/strm/play/cloud115/video.mkv?acct=" + account.ID + "&pickcode=pc2",
	}
	if err := db.Create(media).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}
	svc := NewCloud115PlaybackService(nil, zap.NewNop(), repos, strm)
	if _, _, err := svc.ResolveCloud115URL(ctx, media.ID, 4); !errors.Is(err, ErrCloud115TranscodePending) {
		t.Fatalf("resolve pending err = %v, want ErrCloud115TranscodePending", err)
	}
}

func TestMediaPlaybackProvider(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"115", "/api/strm/play/cloud115/video.mkv?acct=1&pickcode=pc", model.StrmProvider115},
		{"openlist", "/api/strm/play/openlist/video.mkv?acct=1&ref=/a.mkv", model.StrmProviderOpenList},
		{"local", "/media/local.mkv", model.StrmProviderLocal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			media := &model.Media{Path: tc.raw}
			if tc.name != "local" {
				media.STRMURL = tc.raw
			}
			got := MediaPlaybackProvider(media)
			if got != tc.want {
				t.Fatalf("provider = %q, want %q", got, tc.want)
			}
		})
	}
}
