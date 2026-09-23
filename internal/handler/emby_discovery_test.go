package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// YamBy 等客户端进入剧集详情会带 SeriesId 调 NextUp；必须只返回该剧的下一集，
// 不能回落成全站「继续观看」第一条，否则详情页播放会串到别的片子。
func TestEmbyNextUpFiltersBySeriesID(t *testing.T) {
	router, svc, userID := newEmbyDiscoveryEnv(t)
	libID := seedEmbyLibrary(t, svc, "tv")
	recent := time.Now().Add(-time.Minute)
	older := time.Now().Add(-2 * time.Hour)

	seedSeries := func(seriesID, title string, watchedAt time.Time) (watchedID, nextID string) {
		t.Helper()
		for ep := 1; ep <= 3; ep++ {
			m := &model.Media{
				LibraryID: libID, SeriesID: seriesID, Title: title,
				SeasonNum: 1, EpisodeNum: ep,
				Path: "/media/tv/" + seriesID + "/S1E" + strconv.Itoa(ep) + ".mkv",
			}
			if err := svc.Repo.DB.Create(m).Error; err != nil {
				t.Fatal(err)
			}
			switch ep {
			case 1:
				watchedID = m.ID
				h := &model.PlaybackHistory{
					UserID: userID, MediaID: m.ID, PositionMs: 1000, DurationMs: 2000,
					WatchedAt: watchedAt, Completed: false,
				}
				if err := svc.Repo.DB.Create(h).Error; err != nil {
					t.Fatal(err)
				}
			case 2:
				nextID = m.ID
			}
		}
		return watchedID, nextID
	}

	_, _ = seedSeries("series-hot", "热门剧", recent)
	_, wantNext := seedSeries("series-cold", "目标剧", older)

	token := signedTestToken(t, "test-secret")
	global := embyGet(t, router, "/emby/Shows/NextUp?Limit=10", token)
	if global.Code != http.StatusOK {
		t.Fatalf("global status = %d body=%s", global.Code, global.Body.String())
	}
	if items := decodeItemsEnvelope(t, global.Body.Bytes()); len(items) < 2 {
		t.Fatalf("global items = %d, want >= 2 (body=%s)", len(items), global.Body.String())
	}

	scoped := embyGet(t, router, "/emby/Shows/NextUp?SeriesId=series-cold&Limit=10", token)
	if scoped.Code != http.StatusOK {
		t.Fatalf("scoped status = %d body=%s", scoped.Code, scoped.Body.String())
	}
	items := decodeItemsEnvelope(t, scoped.Body.Bytes())
	if len(items) != 1 {
		t.Fatalf("scoped items = %d, want 1 (body=%s)", len(items), scoped.Body.String())
	}
	if id, _ := items[0]["Id"].(string); id != wantNext {
		t.Fatalf("scoped Id = %q, want %q (body=%s)", id, wantNext, scoped.Body.String())
	}
	if seriesID, _ := items[0]["SeriesId"].(string); seriesID != "series-cold" {
		t.Fatalf("scoped SeriesId = %q, want series-cold", seriesID)
	}

	empty := embyGet(t, router, "/emby/Shows/NextUp?SeriesId=series-never-watched", token)
	if empty.Code != http.StatusOK {
		t.Fatalf("empty status = %d body=%s", empty.Code, empty.Body.String())
	}
	if items := decodeItemsEnvelope(t, empty.Body.Bytes()); len(items) != 0 {
		t.Fatalf("never-watched items = %d, want 0 (body=%s)", len(items), empty.Body.String())
	}

	pathScoped := embyGet(t, router, "/emby/Shows/series-cold/NextUp?Limit=10", token)
	if pathScoped.Code != http.StatusOK {
		t.Fatalf("path scoped status = %d body=%s", pathScoped.Code, pathScoped.Body.String())
	}
	pathItems := decodeItemsEnvelope(t, pathScoped.Body.Bytes())
	if len(pathItems) != 1 {
		t.Fatalf("path scoped items = %d, want 1 (body=%s)", len(pathItems), pathScoped.Body.String())
	}
	if id, _ := pathItems[0]["Id"].(string); id != wantNext {
		t.Fatalf("path scoped Id = %q, want %q", id, wantNext)
	}
}
