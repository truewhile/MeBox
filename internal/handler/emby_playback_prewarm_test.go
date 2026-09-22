package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

func newPrewarmTestContainer(t *testing.T) *service.Container {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Media{}, &model.Setting{}, &model.StrmAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		// 内存库 + 后台预热协程：限制单连接，避免新连接拿到空白的 :memory:。
		sqlDB.SetMaxOpenConns(1)
	}
	repos := repository.New(db)
	return &service.Container{
		Log:  zap.NewNop(),
		Repo: repos,
		Strm: service.NewStrmService(&config.Config{}, zap.NewNop(), repos, nil),
	}
}

func newPrewarmTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/emby/Items/media-1/PlaybackInfo", nil)
	c.Request.Header.Set("User-Agent", "RodelPlayer/2.2607.7.0")
	return c
}

func TestEmbyPrewarmMediaIDsExtractsDeduplicates(t *testing.T) {
	out := map[string]any{
		"MediaSources": []map[string]any{
			{"Id": "src-1"},
			{"Id": " src-1 "},
			{"Id": "src-2"},
			{"Id": ""},
			{"Name": "no id"},
		},
	}
	got := embyPrewarmMediaIDs(out)
	if len(got) != 2 || got[0] != "src-1" || got[1] != "src-2" {
		t.Fatalf("ids = %v, want [src-1 src-2]", got)
	}
	if got := embyPrewarmMediaIDs(map[string]any{}); len(got) != 0 {
		t.Fatalf("missing MediaSources should yield no ids, got %v", got)
	}
	if got := embyPrewarmMediaIDs(map[string]any{"MediaSources": []any{}}); len(got) != 0 {
		t.Fatalf("foreign payload shape should yield no ids, got %v", got)
	}
}

// 预热是异步的：调用必须立即返回，并且协程结束后不能残留去重标记。
func TestEmbyPrewarmPlaybackTargetsRunsAsyncAndCleansUp(t *testing.T) {
	svc := newPrewarmTestContainer(t)
	if err := svc.Repo.DB.Create(&model.Media{
		Base:      model.Base{ID: "media-1"},
		Title:     "Cloud",
		Path:      "cloud://cloud115/Movie.mkv",
		Container: "strm",
		STRMURL:   "/api/strm/play/cloud115/video.mkv?acct=missing&pickcode=pc1",
	}).Error; err != nil {
		t.Fatal(err)
	}

	embyPrewarmInFlight.Delete("media-1")
	out := map[string]any{"MediaSources": []map[string]any{{"Id": "media-1"}}}

	done := make(chan struct{})
	go func() {
		embyPrewarmPlaybackTargets(svc, newPrewarmTestContext(), out)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("embyPrewarmPlaybackTargets blocked the caller")
	}

	// 后台协程应很快跑完并释放去重标记，否则同一条目后续再也预热不了。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, busy := embyPrewarmInFlight.Load("media-1"); !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("prewarm in-flight marker leaked")
}

// 各种缺数据的情况都不允许 panic 或阻塞：预热只是尽力而为的优化。
func TestEmbyPrewarmPlaybackTargetsIsNilSafe(t *testing.T) {
	svc := newPrewarmTestContainer(t)
	c := newPrewarmTestContext()
	out := map[string]any{"MediaSources": []map[string]any{{"Id": "media-1"}}}

	cases := []struct {
		name string
		svc  *service.Container
		out  map[string]any
	}{
		{name: "空容器", svc: &service.Container{}, out: out},
		{name: "无 Strm", svc: &service.Container{Repo: svc.Repo}, out: out},
		{name: "无 Repo", svc: &service.Container{Strm: svc.Strm}, out: out},
		{name: "nil 载荷", svc: svc, out: nil},
		{name: "无 MediaSources", svc: svc, out: map[string]any{}},
		{name: "条目不存在", svc: svc, out: out},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			embyPrewarmPlaybackTargets(tc.svc, c, tc.out)
		})
	}
}

// 本地文件条目不该触发换链预热（没有云盘直链可预热）。
func TestEmbyPrewarmSkipsLocalMedia(t *testing.T) {
	svc := newPrewarmTestContainer(t)
	if err := svc.Repo.DB.Create(&model.Media{
		Base:      model.Base{ID: "local-1"},
		Title:     "Local",
		Path:      "/media/movies/Local.mkv",
		LibraryID: "lib-1",
	}).Error; err != nil {
		t.Fatal(err)
	}

	embyPrewarmInFlight.Delete("local-1")
	embyPrewarmPlaybackTargets(svc, newPrewarmTestContext(),
		map[string]any{"MediaSources": []map[string]any{{"Id": "local-1"}}})

	// 协程要么已经跑完（标记被清掉），要么根本没起；两种都不该留下标记。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, busy := embyPrewarmInFlight.Load("local-1"); !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("local media must not leave a prewarm marker")
}
