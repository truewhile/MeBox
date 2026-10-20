package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// 首页轮播/剧集卡片交给播放器的是「整部剧」的伪装 ID，它自身没有媒体源：远程 Emby
// 对它的取流请求会回 500（Unable to cast 'Series' to 'IHasMediaSources'）。这里锁定
// 播放接口的行为——改投第一集，且客户端原有的查询参数（token / profile / proxy）
// 必须原样带过去，否则播放器跟过去之后会掉鉴权。
func TestStreamRedirectsRemoteContainerToPlayableEpisode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/emby/Users/uid-1/Items/series-100":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": "series-100", "Name": "某部新番", "Type": "Series", "IsFolder": true,
			})
		case "/emby/Users/uid-1/Items":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					map[string]any{
						"Id": "ep-1", "Name": "第1集", "Type": "Episode", "SeriesId": "series-100",
						"ParentIndexNumber": 1, "IndexNumber": 1,
						"MediaSources": []any{map[string]any{"Id": "ep-1-src"}},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.UserPermission{}, &model.Setting{}, &model.Library{}, &model.Media{},
		&model.StrmAccount{}, &model.EmbyMount{},
	); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	cfg := &config.Config{}
	cfg.Secrets.JWTSecret = "stream-redirect-secret"
	log := zap.NewNop()

	remote := service.NewEmbyRemoteService(cfg, log, repos, service.NewCryptoService("", log))
	svc := &service.Container{Cfg: cfg, Repo: repos, EmbyRemote: remote}

	rawConfig, _ := json.Marshal(map[string]string{
		"url":            upstream.URL,
		"api_key":        "test-api-key",
		"remote_user_id": "uid-1",
	})
	if err := repos.StrmAccount.Create(t.Context(), &model.StrmAccount{
		Base:     model.Base{ID: "acct-1"},
		Name:     "远程 Emby",
		Provider: model.StrmProviderEmbyRemote,
		Config:   string(rawConfig),
		Enabled:  true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.EmbyMount.Create(t.Context(), &model.EmbyMount{
		Base:           model.Base{ID: "mount-1"},
		AccountID:      "acct-1",
		RemoteViewID:   "view-1",
		RemoteViewName: "新番连载",
		CollectionType: "tvshows",
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.GET("/api/stream/:id", streamHandler(svc))

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/stream/"+service.EncodeEmbyRemoteID("mount-1", "series-100")+"?token=tok-1&profile=p-1",
		nil,
	)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d body=%s, want 302", w.Code, w.Body.String())
	}
	want := "/api/stream/" + service.EncodeEmbyRemoteID("mount-1", "ep-1") + "?token=tok-1&profile=p-1"
	if got := w.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

// 本身可播的远程条目（单集/电影）不能被改写，必须照原样进入原来的代理/直连分支。
func TestStreamKeepsPlayableRemoteItem(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var streamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/emby/Users/uid-1/Items/ep-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": "ep-1", "Name": "第1集", "Type": "Episode", "SeriesId": "series-100",
				"ParentIndexNumber": 1, "IndexNumber": 1,
				"MediaSources": []any{map[string]any{"Id": "ep-1-src"}},
			})
		case "/emby/Videos/ep-1/stream":
			streamCalls++
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bytes"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.UserPermission{}, &model.Setting{}, &model.Library{}, &model.Media{},
		&model.StrmAccount{}, &model.EmbyMount{},
	); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	cfg := &config.Config{}
	cfg.Secrets.JWTSecret = "stream-keep-secret"
	log := zap.NewNop()

	remote := service.NewEmbyRemoteService(cfg, log, repos, service.NewCryptoService("", log))
	emby := service.NewEmbyService(cfg, log, repos).SetEmbyRemote(remote)
	svc := &service.Container{Cfg: cfg, Repo: repos, EmbyRemote: remote, Emby: emby}

	rawConfig, _ := json.Marshal(map[string]string{
		"url":            upstream.URL,
		"api_key":        "test-api-key",
		"remote_user_id": "uid-1",
	})
	if err := repos.StrmAccount.Create(t.Context(), &model.StrmAccount{
		Base:     model.Base{ID: "acct-1"},
		Name:     "远程 Emby",
		Provider: model.StrmProviderEmbyRemote,
		Config:   string(rawConfig),
		Enabled:  true,
	}); err != nil {
		t.Fatal(err)
	}
	// proxy_play=true：直接走反向代理分支，便于断言确实取到了流。
	if err := repos.EmbyMount.Create(t.Context(), &model.EmbyMount{
		Base:           model.Base{ID: "mount-1"},
		AccountID:      "acct-1",
		RemoteViewID:   "view-1",
		RemoteViewName: "新番连载",
		CollectionType: "tvshows",
		ProxyPlay:      true,
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.GET("/api/stream/:id", streamHandler(svc))

	req := httptest.NewRequest(http.MethodGet, "/api/stream/"+service.EncodeEmbyRemoteID("mount-1", "ep-1"), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "bytes" {
		t.Fatalf("body = %q, want the proxied stream", got)
	}
	if streamCalls != 1 {
		t.Fatalf("upstream stream calls = %d, want 1", streamCalls)
	}
}
