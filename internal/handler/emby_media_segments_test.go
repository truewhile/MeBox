package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

type embySegmentsPayload struct {
	Items []struct {
		ID         string `json:"Id"`
		ItemID     string `json:"ItemId"`
		Type       string `json:"Type"`
		StartTicks int64  `json:"StartTicks"`
		EndTicks   int64  `json:"EndTicks"`
	} `json:"Items"`
	TotalRecordCount int `json:"TotalRecordCount"`
}

// 电影：intro 有明确结束点；credits 的 end_ms 为 null（库内落成 0），
// 必须用媒体时长补齐 —— 这是最容易写错的一处。
const embySegmentsProviderBody = `{"tmdb_id":27205,"type":"movie","intro":[{"start_ms":null,"end_ms":38000}],"credits":[{"start_ms":6480000,"end_ms":null}]}`

const (
	embySegmentsMovieDurationSec = 8880
	embySegmentsTicksPerSecond   = 10_000_000
)

func newEmbySegmentsTestRouter(t *testing.T, durationSec int) (*gin.Engine, string, *int32) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	if err := repos.User.Create(t.Context(), &model.User{
		Base:         model.Base{ID: "user-1"},
		Username:     "tester",
		PasswordHash: "x",
		Role:         "admin",
		Tier:         "plus",
		IsActive:     true,
	}); err != nil {
		t.Fatal(err)
	}
	lib := model.Library{Base: model.Base{ID: "lib-movies"}, Name: "电影", Path: "D:\\media\\movies", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Media{
		Base:        model.Base{ID: "movie-1"},
		LibraryID:   lib.ID,
		Title:       "Inception",
		Path:        "D:\\media\\movies\\Inception.mkv",
		DurationSec: durationSec,
		TMDbID:      27205,
	}).Error; err != nil {
		t.Fatal(err)
	}

	var calls int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(embySegmentsProviderBody))
	}))
	t.Cleanup(provider.Close)

	segments := service.NewMediaSegmentService(zap.NewNop(), repos).
		SetIntroDB(service.NewIntroDBService(zap.NewNop()).SetBaseURL(provider.URL))

	const secret = "test-secret"
	router := gin.New()
	registerEmbyRoutes(router, secret, &service.Container{
		Repo:     repos,
		Emby:     service.NewEmbyService(&config.Config{}, zap.NewNop(), repos),
		Segments: segments,
		Log:      zap.NewNop(),
	})
	return router, secret, &calls
}

func embySegmentsRequest(t *testing.T, router *gin.Engine, secret, path string) embySegmentsPayload {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Emby-Token", signedTestToken(t, secret))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%s status = %d body=%s", path, w.Code, w.Body.String())
	}
	var payload embySegmentsPayload
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("%s decode: %v", path, err)
	}
	return payload
}

func TestEmbyMediaSegmentsReturnsTicksAndMapsCreditsToOutro(t *testing.T) {
	router, secret, _ := newEmbySegmentsTestRouter(t, embySegmentsMovieDurationSec)

	payload := embySegmentsRequest(t, router, secret, "/MediaSegments/movie-1")
	if payload.TotalRecordCount != 2 || len(payload.Items) != 2 {
		t.Fatalf("segments = %#v (total %d), want 2", payload.Items, payload.TotalRecordCount)
	}

	intro := payload.Items[0]
	if intro.Type != "Intro" || intro.ItemID != "movie-1" || intro.ID == "" {
		t.Fatalf("intro segment = %#v", intro)
	}
	// start_ms null 表示从片头开始。
	if intro.StartTicks != 0 || intro.EndTicks != 38*embySegmentsTicksPerSecond {
		t.Fatalf("intro ticks = %d..%d, want 0..%d",
			intro.StartTicks, intro.EndTicks, 38*embySegmentsTicksPerSecond)
	}

	// 库内 credits 在 Emby 一侧是 Outro；end_ms = 0 必须按媒体时长补齐，
	// 否则客户端会拿到一个零长度区间。
	outro := payload.Items[1]
	if outro.Type != "Outro" {
		t.Fatalf("credits should map to Outro, got %q", outro.Type)
	}
	if outro.StartTicks != 6480*embySegmentsTicksPerSecond {
		t.Fatalf("outro StartTicks = %d, want %d", outro.StartTicks, 6480*embySegmentsTicksPerSecond)
	}
	if outro.EndTicks != embySegmentsMovieDurationSec*embySegmentsTicksPerSecond {
		t.Fatalf("outro EndTicks = %d, want the media duration %d",
			outro.EndTicks, embySegmentsMovieDurationSec*embySegmentsTicksPerSecond)
	}
}

func TestEmbyMediaSegmentsIsServedFromTheSameCacheAsTheWebPlayer(t *testing.T) {
	router, secret, calls := newEmbySegmentsTestRouter(t, embySegmentsMovieDurationSec)

	// 多条路径 + 大小写变体都应命中同一份缓存，而不是各自再打一次外网。
	for _, path := range []string{
		"/MediaSegments/movie-1",
		"/mediasegments/movie-1",
		"/Items/movie-1/MediaSegments",
		"/items/movie-1/mediasegments",
		"/Users/user-1/Items/movie-1/MediaSegments",
	} {
		payload := embySegmentsRequest(t, router, secret, path)
		if len(payload.Items) != 2 {
			t.Fatalf("%s returned %#v, want 2 segments", path, payload.Items)
		}
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (all routes share the cached rows)", got)
	}
}

func TestEmbyMediaSegmentsHonoursIncludeSegmentTypes(t *testing.T) {
	router, secret, _ := newEmbySegmentsTestRouter(t, embySegmentsMovieDurationSec)

	payload := embySegmentsRequest(t, router, secret, "/MediaSegments/movie-1?includeSegmentTypes=Intro")
	if payload.TotalRecordCount != 1 || len(payload.Items) != 1 {
		t.Fatalf("filtered segments = %#v (total %d), want only Intro", payload.Items, payload.TotalRecordCount)
	}
	if payload.Items[0].Type != "Intro" {
		t.Fatalf("filtered type = %q, want Intro", payload.Items[0].Type)
	}

	// 认不出的枚举名按「不过滤」处理：返回超集比返回空集安全。
	payload = embySegmentsRequest(t, router, secret, "/MediaSegments/movie-1?includeSegmentTypes=NotAType")
	if payload.TotalRecordCount != 2 {
		t.Fatalf("unknown filter returned %d segments, want the unfiltered set", payload.TotalRecordCount)
	}
}

func TestEmbyMediaSegmentsDropsOpenEndedRangeWhenDurationUnknown(t *testing.T) {
	// 时长未知（STRM/云盘媒体探测前）时，credits 无法换算成真实结束点，
	// 只能丢弃；有明确结束点的 intro 必须保留。
	router, secret, _ := newEmbySegmentsTestRouter(t, 0)

	payload := embySegmentsRequest(t, router, secret, "/MediaSegments/movie-1")
	if payload.TotalRecordCount != 1 || len(payload.Items) != 1 {
		t.Fatalf("segments = %#v (total %d), want only the intro", payload.Items, payload.TotalRecordCount)
	}
	if payload.Items[0].Type != "Intro" {
		t.Fatalf("kept segment = %#v, want Intro", payload.Items[0])
	}
}

func TestEmbyMediaSegmentsReturnsEmptyInsteadOfNotFound(t *testing.T) {
	router, secret, calls := newEmbySegmentsTestRouter(t, embySegmentsMovieDurationSec)

	// 未知条目必须 200 + 空数组：客户端会把 404 判成「条目损坏」。
	payload := embySegmentsRequest(t, router, secret, "/MediaSegments/does-not-exist")
	if payload.Items == nil || len(payload.Items) != 0 || payload.TotalRecordCount != 0 {
		t.Fatalf("unknown item payload = %#v", payload)
	}
	// 远程 Emby 条目同理（本地没有可查询的外部 ID 关联）。
	payload = embySegmentsRequest(t, router, secret, "/MediaSegments/embyremote~acct1~item1")
	if len(payload.Items) != 0 {
		t.Fatalf("remote emby item payload = %#v, want empty", payload.Items)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("provider calls = %d, want 0 for unresolvable items", got)
	}
}
