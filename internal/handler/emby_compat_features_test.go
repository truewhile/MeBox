package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestNormalizeEmbyPath(t *testing.T) {
	tests := []struct {
		input    string
		wantPath string
		changed  bool
	}{
		{
			input:    "/emby/System/Info",
			wantPath: "/emby/system/info",
			changed:  true,
		},
		{
			input:    "/emby/emby/System/Info",
			wantPath: "/emby/system/info",
			changed:  true,
		},
		{
			input:    "/emby/emby/emby/items/123/playbackInfo",
			wantPath: "/emby/items/123/playbackinfo",
			changed:  true,
		},
		{
			input:    "//emby//System//Info//Public",
			wantPath: "/emby/system/info/public",
			changed:  true,
		},
		{
			input:    "/Items/msgo-series-1/PlaybackInfo",
			wantPath: "/items/msgo-series-1/playbackinfo",
			changed:  true,
		},
		{
			input:    "/Videos/m-123/Master.m3u8",
			wantPath: "/videos/m-123/master.m3u8",
			changed:  true,
		},
		{
			input:    "/api/unknown/other",
			wantPath: "/api/unknown/other",
			changed:  false,
		},
	}

	for _, tt := range tests {
		gotPath, changed := NormalizeEmbyPath(tt.input)
		if gotPath != tt.wantPath || changed != tt.changed {
			t.Errorf("NormalizeEmbyPath(%q) = (%q, %v), want (%q, %v)", tt.input, gotPath, changed, tt.wantPath, tt.changed)
		}
	}
}

func TestEmbyDuplicatePrefixHandling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	svc := &service.Container{
		Repo: repos,
		Emby: service.NewEmbyService(nil, nil, repos),
	}

	router := gin.New()
	registerEmbyRoutes(router, "secret", svc)

	// 模拟重复拼接前缀的客户端请求: /emby/emby/System/Info/Public
	req := httptest.NewRequest(http.MethodGet, "/emby/emby/System/Info/Public", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /emby/emby/System/Info/Public, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ServerName") {
		t.Fatalf("expected server info body, got: %s", w.Body.String())
	}
}

func TestEmbyMixedCaseHandling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	svc := &service.Container{
		Repo: repos,
		Emby: service.NewEmbyService(nil, nil, repos),
	}

	router := gin.New()
	registerEmbyRoutes(router, "secret", svc)

	// 混合大小写驼峰: /emby/system/Info/Public
	req := httptest.NewRequest(http.MethodGet, "/emby/system/Info/Public", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /emby/system/Info/Public, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEmbyClientIdentification(t *testing.T) {
	tests := []struct {
		name       string
		ua         string
		query      string
		headerAuth string
		wantClient string
	}{
		{
			name:       "CapyPlayer via UA",
			ua:         "CapyPlayer/1.2.0 (iOS)",
			wantClient: "CapyPlayer",
		},
		{
			name:       "SenPlayer via UA",
			ua:         "SenPlayer/2.1",
			wantClient: "SenPlayer",
		},
		{
			name:       "Fileball via UA",
			ua:         "Fileball/1.0.0",
			wantClient: "Fileball",
		},
		{
			name:       "Kodi via UA",
			ua:         "Kodi/20.2",
			wantClient: "Kodi",
		},
		{
			name:       "Client in query",
			ua:         "CustomApp/1.0",
			query:      "?X-Emby-Client=CapyPlayer",
			wantClient: "CapyPlayer",
		},
		{
			name:       "Client in auth header",
			ua:         "Custom/1.0",
			headerAuth: `MediaBrowser Client="SenPlayer", Device="AppleTV", DeviceId="abc"`,
			wantClient: "SenPlayer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			url := "/test"
			if tt.query != "" {
				url += tt.query
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if tt.ua != "" {
				req.Header.Set("User-Agent", tt.ua)
			}
			if tt.headerAuth != "" {
				req.Header.Set("X-Emby-Authorization", tt.headerAuth)
			}
			c.Request = req

			info := embyClientInfoFromRequest(c)
			if info.Client != tt.wantClient {
				t.Fatalf("embyClientInfoFromRequest Client = %q, want %q", info.Client, tt.wantClient)
			}
		})
	}
}

func TestEmbyAdaptivePrefixPlaybackInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	svc := &service.Container{
		Repo: repos,
		Emby: service.NewEmbyService(nil, nil, repos),
	}
	const secret = "test-secret"
	router := gin.New()
	registerEmbyRoutes(router, secret, svc)

	token := signedTestToken(t, secret)
	if err := repos.User.Create(t.Context(), &model.User{
		Base:         model.Base{ID: "user-1"},
		Username:     "tester",
		PasswordHash: "x",
		Role:         "admin",
		Tier:         "plus",
		IsActive:     true,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.Media{
		Base:      model.Base{ID: "m-adaptive-1"},
		Title:     "测试媒体",
		Path:      "D:\\media\\test.mkv",
		LibraryID: "lib-1",
	}).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}

	// 1. 从 /emby 前缀请求 PlaybackInfo
	req1 := httptest.NewRequest(http.MethodGet, "/emby/Items/m-adaptive-1/PlaybackInfo", nil)
	req1.Header.Set("X-Emby-Token", token)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("playbackinfo /emby code = %d: %s", w1.Code, w1.Body.String())
	}
	var res1 struct {
		MediaSources []struct {
			DirectStreamURL string `json:"DirectStreamUrl"`
		} `json:"MediaSources"`
	}
	if err := json.Unmarshal(w1.Body.Bytes(), &res1); err != nil || len(res1.MediaSources) == 0 {
		t.Fatalf("unmarshal /emby response: %v, body: %s", err, w1.Body.String())
	}
	if !strings.Contains(res1.MediaSources[0].DirectStreamURL, "/Videos/m-adaptive-1/stream") {
		t.Fatalf("DirectStreamUrl should point to video stream endpoint, got: %s", res1.MediaSources[0].DirectStreamURL)
	}
	if !strings.Contains(res1.MediaSources[0].DirectStreamURL, "api_key="+token) {
		t.Fatalf("DirectStreamUrl should carry api_key token, got: %s", res1.MediaSources[0].DirectStreamURL)
	}

	// 2. 从重复前缀 /emby/emby 请求 PlaybackInfo (模拟客户端再次追加 BaseUrl 场景)
	req2 := httptest.NewRequest(http.MethodGet, "/emby/emby/Items/m-adaptive-1/PlaybackInfo", nil)
	req2.Header.Set("X-Emby-Token", token)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("playbackinfo /emby/emby code = %d: %s", w2.Code, w2.Body.String())
	}
	var res2 struct {
		MediaSources []struct {
			DirectStreamURL string `json:"DirectStreamUrl"`
		} `json:"MediaSources"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &res2); err != nil || len(res2.MediaSources) == 0 {
		t.Fatalf("unmarshal /emby/emby response: %v, body: %s", err, w2.Body.String())
	}

	// 3. 从根路径 /Items 请求 PlaybackInfo
	req3 := httptest.NewRequest(http.MethodGet, "/Items/m-adaptive-1/PlaybackInfo", nil)
	req3.Header.Set("X-Emby-Token", token)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Fatalf("playbackinfo root code = %d: %s", w3.Code, w3.Body.String())
	}
}

func TestEmbyImageClearNoStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	svc := &service.Container{
		Repo: repos,
		Emby: service.NewEmbyService(nil, nil, repos),
	}

	router := gin.New()
	registerEmbyRoutes(router, "secret", svc)

	req := httptest.NewRequest(http.MethodGet, "/emby/Items/non-existent-item/Images/Primary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("placeholder image should return 200, got %d", w.Code)
	}
	cacheControl := w.Header().Get("Cache-Control")
	if strings.Contains(cacheControl, "no-store") {
		t.Fatalf("image response should not have no-store, got: %s", cacheControl)
	}
		if !strings.Contains(cacheControl, "public") {
			t.Fatalf("image response should have public cache-control, got: %s", cacheControl)
		}
	}

func TestEmbyTemporaryPasswordLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	cfg := &config.Config{}
	cfg.Secrets.JWTSecret = "test-jwt-secret-very-secure-key-12345"
	log := zap.NewNop()
	permissions := service.NewPermissionService(log, repos)
	tokenSvc := service.NewTokenService(cfg, log, repos)
	authSvc := service.NewAuthService(cfg, log, repos, tokenSvc, permissions)
	embySvc := service.NewEmbyService(nil, nil, repos)
	svc := &service.Container{
		Repo:  repos,
		Auth:  authSvc,
		Token: tokenSvc,
		Emby:  embySvc,
	}

	user, _, err := authSvc.Register(t.Context(), "tvuser", "strongpassword123")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// 1. 生成 6 位纯数字临时密码 (OTP)
	code, expireSec, err := authSvc.CreateTemporaryPassword(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("create temp password: %v", err)
	}
	if len(code) != 6 || expireSec <= 0 {
		t.Fatalf("invalid temp password format: %s, expire: %d", code, expireSec)
	}

	router := gin.New()
	registerEmbyRoutes(router, "test-jwt-secret-very-secure-key-12345", svc)

	// 2. 使用临时密码在 Emby 接口登录
	body := `{"Username":"tvuser","Pw":"` + code + `"}`
	req := httptest.NewRequest(http.MethodPost, "/emby/Users/AuthenticateByName", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("login with temp password code = %d: %s", w.Code, w.Body.String())
	}
	var loginResp struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"User"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("unmarshal login resp: %v", err)
	}
	if loginResp.AccessToken == "" || loginResp.User.ID != user.ID {
		t.Fatalf("unexpected login payload: %#v", loginResp)
	}

	// 3. 验证阅后即焚：第二次使用同一临时密码应登录失败 (401)
	req2 := httptest.NewRequest(http.MethodPost, "/emby/Users/AuthenticateByName", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("second login with consumed temp password should fail, got %d", w2.Code)
	}
}

func TestEmbySeriesArtworkInheritanceAndRunTimeTicksFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	cfg := &config.Config{}
	cfg.Secrets.JWTSecret = "test-secret-compat"
	log := zap.NewNop()
	permissions := service.NewPermissionService(log, repos)
	tokenSvc := service.NewTokenService(cfg, log, repos)
	authSvc := service.NewAuthService(cfg, log, repos, tokenSvc, permissions)
	embySvc := service.NewEmbyService(nil, nil, repos)
	svc := &service.Container{
		Repo:  repos,
		Auth:  authSvc,
		Token: tokenSvc,
		Emby:  embySvc,
	}

	user, _, err := authSvc.Register(t.Context(), "artworkuser", "password123")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	token, err := authSvc.IssueEmbyToken(user)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	// 创建 TV Library
	lib := &model.Library{
		Name: "电视剧",
		Path: "/media/电视剧",
		Type: "tv",
	}
	lib.ID = "lib-tv-1"
	if err := db.Create(lib).Error; err != nil {
		t.Fatalf("create lib: %v", err)
	}

	// 创建 Series
	series := &model.Series{
		LibraryID:   "lib-tv-1",
		Title:       "Test Drama",
		PosterURL:   "https://example.com/series_poster.jpg",
		BackdropURL: "https://example.com/series_backdrop.jpg",
	}
	series.ID = "s-test-1"
	if err := db.Create(series).Error; err != nil {
		t.Fatalf("create series: %v", err)
	}

	// 创建单集 Episode（DurationSec 为 0，但有播放进度 posMs，用于测试 RunTimeTicks 兜底）
	ep := &model.Media{
		LibraryID:   "lib-tv-1",
		SeriesID:    "s-test-1",
		Title:       "Test Episode 1",
		Path:        "/media/电视剧/Test Drama/Season 1/S01E01.mp4",
		SeasonNum:   1,
		EpisodeNum:  1,
		DurationSec: 0, // 未知时长
		PosterURL:   "https://example.com/ep1_still.jpg",
	}
	ep.ID = "ep-test-1"
	if err := db.Create(ep).Error; err != nil {
		t.Fatalf("create ep: %v", err)
	}

	router := gin.New()
	registerEmbyRoutes(router, cfg.Secrets.JWTSecret, svc)

	// 1. 获取 Series 详情
	reqSeries := httptest.NewRequest(http.MethodGet, "/emby/Items/s-test-1", nil)
	reqSeries.Header.Set("X-Emby-Token", token)
	wSeries := httptest.NewRecorder()
	router.ServeHTTP(wSeries, reqSeries)
	if wSeries.Code != http.StatusOK {
		t.Fatalf("get series code = %d: %s", wSeries.Code, wSeries.Body.String())
	}
	var seriesPayload map[string]any
	_ = json.Unmarshal(wSeries.Body.Bytes(), &seriesPayload)
	if seriesPayload["PrimaryImageTag"] != "s-test-1" {
		t.Fatalf("series PrimaryImageTag should match series ID, got %v", seriesPayload["PrimaryImageTag"])
	}
	if _, ok := seriesPayload["People"]; !ok {
		t.Fatalf("series payload should include People array")
	}

	// 2. 获取 Episode 详情，验证继承 SeriesPrimaryImageTag 和 ParentBackdropItemId
	// 添加一条播放进度记录 (posMs = 60000)
	hist := &model.PlaybackHistory{
		UserID:     user.ID,
		MediaID:    ep.ID,
		PositionMs: 60000,
	}
	_ = db.Create(hist).Error

	reqEp := httptest.NewRequest(http.MethodGet, "/emby/Users/"+user.ID+"/Items/ep-test-1", nil)
	reqEp.Header.Set("X-Emby-Token", token)
	wEp := httptest.NewRecorder()
	router.ServeHTTP(wEp, reqEp)
	if wEp.Code != http.StatusOK {
		t.Fatalf("get ep code = %d: %s", wEp.Code, wEp.Body.String())
	}
		var epPayload map[string]any
		_ = json.Unmarshal(wEp.Body.Bytes(), &epPayload)
		t.Logf("epPayload: %#v", epPayload)

	// 验证图片继承
	if epPayload["SeriesPrimaryImageTag"] != "s-test-1" {
		t.Fatalf("ep SeriesPrimaryImageTag should inherit series ID, got %v", epPayload["SeriesPrimaryImageTag"])
	}
	if epPayload["ParentBackdropItemId"] != "s-test-1" {
		t.Fatalf("ep ParentBackdropItemId should inherit series ID, got %v", epPayload["ParentBackdropItemId"])
	}
	if _, ok := epPayload["People"]; !ok {
		t.Fatalf("ep payload should include People array")
	}

	// 验证 RunTimeTicks 兜底
	runTimeTicks, _ := epPayload["RunTimeTicks"].(float64)
	if runTimeTicks <= 0 {
		t.Fatalf("ep RunTimeTicks should be safely fallback to positive value, got %v", runTimeTicks)
	}
	userData, _ := epPayload["UserData"].(map[string]any)
	playedPct, _ := userData["PlayedPercentage"].(float64)
	if playedPct <= 0 {
		t.Fatalf("ep PlayedPercentage should be > 0, got %v", playedPct)
	}
}

func TestMeTemporaryPasswordEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	cfg := &config.Config{}
	cfg.Secrets.JWTSecret = "test-secret-temp"
	log := zap.NewNop()
	permissions := service.NewPermissionService(log, repos)
	tokenSvc := service.NewTokenService(cfg, log, repos)
	authSvc := service.NewAuthService(cfg, log, repos, tokenSvc, permissions)
	svc := &service.Container{
		Repo:  repos,
		Auth:  authSvc,
		Token: tokenSvc,
	}

	user, tokens, err := authSvc.Register(t.Context(), "optuser", "password123")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	router := gin.New()
	api := router.Group("/api")
	authed := api.Group("")
	authed.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, user.ID)
		c.Next()
	})
	registerAuthedUserAndLicenseRoutes(authed, svc)

	req := httptest.NewRequest(http.MethodPost, "/api/me/temporary-password", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("generate temp password code = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code      string `json:"code"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if len(resp.Code) != 6 || resp.ExpiresIn <= 0 {
		t.Fatalf("invalid temp password resp: %#v", resp)
	}

	// 验证使用生成的临时密码能登录
	loginResp, err := authSvc.LoginWithTemporaryPassword(t.Context(), "optuser", resp.Code)
	if err != nil || loginResp == nil || loginResp.User.ID != user.ID {
		t.Fatalf("login with temp pass failed: %v", err)
	}
	_ = tokens
}
