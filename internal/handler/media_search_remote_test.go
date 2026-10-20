package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

// TestSearchMediaRemoteSearchBoundedAndCached 防止远程搜索再次退化：
// 1. 每个挂载点的 Limit 必须是 remoteSearchItemLimit，而不是历史全库查询的 50000；
// 2. 同一关键词的重复搜索应命中缓存，不再打上游；
// 3. 指定本地媒体库时不应枚举远程挂载点。
func TestSearchMediaRemoteSearchBoundedAndCached(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var mu sync.Mutex
	searchCalls := 0
	searchLimits := make([]string, 0, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if term := r.URL.Query().Get("SearchTerm"); term != "" {
			mu.Lock()
			searchCalls++
			searchLimits = append(searchLimits, r.URL.Query().Get("Limit"))
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"TotalRecordCount": 1,
			"Items": []map[string]any{{
				"Id":             "remote-1",
				"Name":           "远方剧集",
				"Type":           "Series",
				"ProductionYear": 2024,
			}},
		})
	}))
	defer upstream.Close()

	// 唯一库名 + cache=shared：远程搜索与本地查询会并发读库，
	// 而 :memory: 下每个连接各自一个空库，并发连接会读到「no such table」。
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}, &model.StrmAccount{}, &model.EmbyMount{}, &model.Setting{}, &model.User{}, &model.PlayProfile{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	if err := repos.DB.Create(&model.User{Base: model.Base{ID: "user-1"}, Username: "admin", Role: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	lib := model.Library{Name: "本地电影", Path: "/media/movies", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Media{
		Base: model.Base{ID: "local-1"}, LibraryID: lib.ID, Title: "流浪地球", Path: "/media/movies/流浪地球.mkv",
	}).Error; err != nil {
		t.Fatal(err)
	}
	rawConfig, _ := json.Marshal(map[string]string{"url": upstream.URL, "token": "fake-token"})
	acct := model.StrmAccount{
		Base: model.Base{ID: "acct"}, Name: "远程Emby", Provider: model.StrmProviderEmbyRemote,
		Config: string(rawConfig), Enabled: true,
	}
	if err := repos.StrmAccount.Create(t.Context(), &acct); err != nil {
		t.Fatal(err)
	}
	mount := model.EmbyMount{
		Base: model.Base{ID: "mount-1"}, AccountID: acct.ID, RemoteViewID: "view-1",
		RemoteViewName: "远程剧集", CollectionType: "tvshows", Enabled: true,
	}
	if err := repos.EmbyMount.Create(t.Context(), &mount); err != nil {
		t.Fatal(err)
	}

	svc := &service.Container{
		Repo:       repos,
		Media:      service.NewMediaService(&config.Config{}, zap.NewNop(), repos),
		EmbyRemote: service.NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, service.NewCryptoService("", zap.NewNop())),
		Cache:      service.NewRuntimeCacheService(&config.Config{}, zap.NewNop()),
	}

	search := func(params string) []service.MediaItem {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(middleware.CtxUserID, "user-1")
		c.Set(middleware.CtxUserRole, "admin")
		c.Request = httptest.NewRequest(http.MethodGet, "/api/media?"+params, nil)
		searchMediaHandler(svc)(c)
		if w.Code != http.StatusOK {
			t.Fatalf("search status=%d, body=%s", w.Code, w.Body.String())
		}
		var response struct {
			Items []service.MediaItem `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Items
	}
	callCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return searchCalls
	}

	if items := search("q=远方&page=1&page_size=48"); len(items) != 1 || items[0].Title != "远方剧集" {
		t.Fatalf("expected remote result, got %#v", items)
	}
	if calls := callCount(); calls != 1 {
		t.Fatalf("expected exactly one upstream search, got %d", calls)
	}
	mu.Lock()
	limit := searchLimits[0]
	mu.Unlock()
	if limit != "100" {
		t.Fatalf("remote search Limit=%q, want bounded 100 (50000 means a full-library query)", limit)
	}

	// 同一关键词再来一次（例如刷新/重复搜索）应复用缓存，不再请求上游。
	if items := search("q=远方&page=1&page_size=48"); len(items) != 1 || items[0].Title != "远方剧集" {
		t.Fatalf("cached search lost the remote result: %#v", items)
	}
	if calls := callCount(); calls != 1 {
		t.Fatalf("expected cached remote search to skip upstream, got %d calls", calls)
	}

	// 指定本地媒体库时不应枚举远程挂载点。
	if items := search("q=流浪&page=1&page_size=48&library_id=" + lib.ID); len(items) != 1 || items[0].ID != "local-1" {
		t.Fatalf("expected local-only result, got %#v", items)
	}
	if calls := callCount(); calls != 1 {
		t.Fatalf("local library filter must not hit upstream, got %d calls", calls)
	}
}
