package handler

import (
	"context"
	"encoding/json"
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

// newEmbyDiscoveryEnv 搭一个跑在内存库上的 Emby 路由环境。
func newEmbyDiscoveryEnv(t *testing.T) (*gin.Engine, *service.Container, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Library{}, &model.Media{}, &model.PlaybackHistory{},
		&model.Setting{}, &model.Favorite{}, &model.UserDevice{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	cfg := &config.Config{}
	cfg.Secrets.JWTSecret = "test-secret"

	svc := &service.Container{Repo: repos, Log: zap.NewNop()}
	svc.Emby = service.NewEmbyService(cfg, zap.NewNop(), repos).
		SetDiscovery(service.NewMediaDiscoveryService(zap.NewNop(), repos))

	const userID = "user-1"
	if err := repos.User.Create(context.Background(), &model.User{
		Base: model.Base{ID: userID}, Username: "tester", PasswordHash: "x",
		Role: "user", IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	registerEmbyRoutes(router, cfg.Secrets.JWTSecret, svc)
	return router, svc, userID
}

func seedEmbyLibrary(t *testing.T, svc *service.Container, typ string) string {
	t.Helper()
	lib := &model.Library{Name: "库-" + typ, Path: "/media/" + typ, Type: typ, Enabled: true}
	if err := svc.Repo.Library.Create(context.Background(), lib); err != nil {
		t.Fatal(err)
	}
	return lib.ID
}

func embyGet(t *testing.T, router *gin.Engine, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func decodeItemsEnvelope(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var payload struct {
		Items            []map[string]any `json:"Items"`
		TotalRecordCount int64            `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode %s: %v", string(body), err)
	}
	return payload.Items
}

// NextUp 必须真的返回下一集，而不是空数组。
func TestEmbyNextUpReturnsNextEpisode(t *testing.T) {
	router, svc, userID := newEmbyDiscoveryEnv(t)
	libID := seedEmbyLibrary(t, svc, "tv")
	watchedAt := time.Now().Add(-time.Hour)

	for episode, watched := range map[int]bool{1: true, 2: false, 3: false} {
		m := &model.Media{
			LibraryID: libID, SeriesID: "series-1", Title: "剧一",
			SeasonNum: 1, EpisodeNum: episode,
			Path: "/media/tv/S1E" + string(rune('0'+episode)) + ".mkv",
		}
		if err := svc.Repo.DB.Create(m).Error; err != nil {
			t.Fatal(err)
		}
		if watched {
			h := &model.PlaybackHistory{
				UserID: userID, MediaID: m.ID, PositionMs: 1000, DurationMs: 2000,
				WatchedAt: watchedAt, Completed: false,
			}
			if err := svc.Repo.DB.Create(h).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	w := embyGet(t, router, "/emby/Shows/NextUp", signedTestToken(t, "test-secret"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	items := decodeItemsEnvelope(t, w.Body.Bytes())
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (body=%s)", len(items), w.Body.String())
	}
	if index, ok := items[0]["IndexNumber"].(float64); !ok || int(index) != 2 {
		t.Fatalf("IndexNumber = %v, want 2 (body=%s)", items[0]["IndexNumber"], w.Body.String())
	}
}

// 没有历史时必须返回合法空信封，不能 404/500。
func TestEmbyNextUpEmptyWithoutHistory(t *testing.T) {
	router, _, _ := newEmbyDiscoveryEnv(t)

	w := embyGet(t, router, "/emby/Shows/NextUp", signedTestToken(t, "test-secret"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if items := decodeItemsEnvelope(t, w.Body.Bytes()); len(items) != 0 {
		t.Fatalf("items = %d, want 0", len(items))
	}
}

// 小写别名路由同样要走到真实实现（客户端路径大小写并不统一）。
func TestEmbyNextUpLowercaseAlias(t *testing.T) {
	router, svc, _ := newEmbyDiscoveryEnv(t)
	_ = seedEmbyLibrary(t, svc, "tv")

	w := embyGet(t, router, "/emby/shows/nextup", signedTestToken(t, "test-secret"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

// Similar 对不存在的条目返回空列表（客户端详情页会无条件请求）。
func TestEmbySimilarUnknownItemReturnsEmpty(t *testing.T) {
	router, _, _ := newEmbyDiscoveryEnv(t)

	w := embyGet(t, router, "/emby/Items/does-not-exist/Similar", signedTestToken(t, "test-secret"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if items := decodeItemsEnvelope(t, w.Body.Bytes()); len(items) != 0 {
		t.Fatalf("items = %d, want 0", len(items))
	}
}

func TestEmbySimilarReturnsCandidates(t *testing.T) {
	router, svc, _ := newEmbyDiscoveryEnv(t)
	libID := seedEmbyLibrary(t, svc, "movie")

	source := &model.Media{
		LibraryID: libID, Title: "源片", Genres: "Action", Year: 2010, Rating: 8,
		Path: "/media/movie/source.mkv",
	}
	if err := svc.Repo.DB.Create(source).Error; err != nil {
		t.Fatal(err)
	}
	other := &model.Media{
		LibraryID: libID, Title: "同类片", Genres: "Action", Year: 2011, Rating: 8,
		Path: "/media/movie/other.mkv",
	}
	if err := svc.Repo.DB.Create(other).Error; err != nil {
		t.Fatal(err)
	}

	w := embyGet(t, router, "/emby/Items/"+source.ID+"/Similar", signedTestToken(t, "test-secret"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	items := decodeItemsEnvelope(t, w.Body.Bytes())
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (body=%s)", len(items), w.Body.String())
	}
	if name, _ := items[0]["Name"].(string); name != "同类片" {
		t.Fatalf("Name = %q, want 同类片", name)
	}
}

// Genres 必须返回真实类型与计数。
func TestEmbyGenresReturnsCounts(t *testing.T) {
	router, svc, _ := newEmbyDiscoveryEnv(t)
	libID := seedEmbyLibrary(t, svc, "movie")

	for i, genres := range []string{"Action,Drama", "Action"} {
		m := &model.Media{
			LibraryID: libID, Title: "片" + string(rune('A'+i)), Genres: genres,
			Path: "/media/movie/m" + string(rune('0'+i)) + ".mkv",
		}
		if err := svc.Repo.DB.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}

	w := embyGet(t, router, "/emby/Genres", signedTestToken(t, "test-secret"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	items := decodeItemsEnvelope(t, w.Body.Bytes())
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (body=%s)", len(items), w.Body.String())
	}
	// 排序按计数降序：Action(2) 在前。
	if name, _ := items[0]["Name"].(string); name != "Action" {
		t.Fatalf("first Name = %q, want Action", name)
	}
	if count, ok := items[0]["ItemCount"].(float64); !ok || int(count) != 2 {
		t.Fatalf("ItemCount = %v, want 2", items[0]["ItemCount"])
	}
	if id, _ := items[0]["Id"].(string); len(id) == 0 {
		t.Fatal("genre item must carry a stable Id")
	}
}

// 按别人的 userId 请求 NextUp 不允许泄露他人历史。
func TestEmbyNextUpRejectsForeignUserID(t *testing.T) {
	router, _, _ := newEmbyDiscoveryEnv(t)

	w := embyGet(t, router, "/emby/Users/someone-else/Shows/NextUp", signedTestToken(t, "test-secret"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if items := decodeItemsEnvelope(t, w.Body.Bytes()); len(items) != 0 {
		t.Fatalf("items = %d, want 0", len(items))
	}
}
