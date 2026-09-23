package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

type segmentPayload struct {
	Segments []struct {
		Kind    string `json:"kind"`
		StartMs int64  `json:"start_ms"`
		EndMs   int64  `json:"end_ms"`
	} `json:"segments"`
	AutoSkip bool `json:"auto_skip"`
}

const segmentsProviderBody = `{"tmdb_id":27205,"type":"movie","intro":[{"start_ms":null,"end_ms":38000}],"credits":[{"start_ms":6480000,"end_ms":null}]}`

func TestPlaybackSegmentsReturnsProviderDataAndAutoSkip(t *testing.T) {
	router, svc, secret := newPlaybackScopeTestRouter(t)
	// 片段数据与播放来源无关，云盘媒体同样适用，只要它能解析出外部 ID。
	// 注意列名是 tm_db_id（GORM 对 TMDbID 的默认命名）。
	if err := svc.Repo.DB.Model(&model.Media{}).
		Where("id = ?", "media-1").Update("tm_db_id", 27205).Error; err != nil {
		t.Fatal(err)
	}
	var calls int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(segmentsProviderBody))
	}))
	defer provider.Close()
	svc.Segments = service.NewMediaSegmentService(zap.NewNop(), svc.Repo).
		SetIntroDB(service.NewIntroDBService(zap.NewNop()).SetBaseURL(provider.URL))

	// 默认档案打开「自动跳过片头」，接口应把开关原样带出来。
	if err := svc.Repo.DB.Create(&model.PlayProfile{
		Base:      model.Base{ID: "profile-1"},
		UserID:    "user-1",
		Name:      "主档案",
		IsDefault: true,
		SkipIntro: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	loginToken := signedTestToken(t, secret)
	fetch := func() segmentPayload {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://nas.local/api/playback/media-1/segments", nil)
		req.Header.Set("Authorization", "Bearer "+loginToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		var payload segmentPayload
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return payload
	}

	first := fetch()
	if !first.AutoSkip {
		t.Fatal("auto_skip should reflect the active profile's skip_intro switch")
	}
	if len(first.Segments) != 2 {
		t.Fatalf("segments = %#v, want 2", first.Segments)
	}
	// start_ms: null -> 0；end_ms: null -> 0（延续到片尾，由客户端按时长补齐）。
	if first.Segments[0].Kind != "intro" || first.Segments[0].StartMs != 0 || first.Segments[0].EndMs != 38_000 {
		t.Fatalf("intro segment = %#v", first.Segments[0])
	}
	if first.Segments[1].Kind != "credits" || first.Segments[1].StartMs != 6_480_000 || first.Segments[1].EndMs != 0 {
		t.Fatalf("credits segment = %#v", first.Segments[1])
	}

	// 第二次播放必须走本地缓存，不再打外网。
	if second := fetch(); len(second.Segments) != 2 {
		t.Fatalf("second fetch segments = %#v", second.Segments)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
}

func TestPlaybackSegmentsAutoSkipIsFalseWithoutProfile(t *testing.T) {
	router, svc, secret := newPlaybackScopeTestRouter(t)
	svc.Segments = service.NewMediaSegmentService(zap.NewNop(), svc.Repo)

	req := httptest.NewRequest(http.MethodGet, "http://nas.local/api/playback/media-1/segments", nil)
	req.Header.Set("Authorization", "Bearer "+signedTestToken(t, secret))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var payload segmentPayload
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.AutoSkip {
		t.Fatal("auto_skip must default to false")
	}
	// 即使一条片段都没有，也必须返回空数组而不是 null，前端才能无条件遍历。
	if payload.Segments == nil {
		t.Fatal("segments must serialise as an empty array, not null")
	}
}

func TestPlaybackSegmentsForUnknownMediaReturnsNotFound(t *testing.T) {
	router, svc, secret := newPlaybackScopeTestRouter(t)
	svc.Segments = service.NewMediaSegmentService(zap.NewNop(), svc.Repo)

	req := httptest.NewRequest(http.MethodGet, "http://nas.local/api/playback/does-not-exist/segments", nil)
	req.Header.Set("Authorization", "Bearer "+signedTestToken(t, secret))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want 404", w.Code, w.Body.String())
	}
}
