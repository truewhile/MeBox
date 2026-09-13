package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

// newDanmakuSettingsContext 构造带登录用户的最小 gin 上下文。
func newDanmakuSettingsContext(t *testing.T, svc *service.Container, method, path, body string, userID string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if userID != "" {
		c.Set(middleware.CtxUserID, userID)
	}
	return c, w
}

func newDanmakuSettingsService(t *testing.T) *service.Container {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Setting{}, &model.Media{}, &model.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	user := model.User{Username: "settings-user", PasswordHash: "x", Role: "user", IsActive: true}
	user.ID = "user-1"
	if err := repos.User.Create(t.Context(), &user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return &service.Container{
		Repo:    repos,
		Danmaku: service.NewDanmakuService(zap.NewNop(), repos),
	}
}

func TestUpdateDanmakuSettingsPersistsMergeSources(t *testing.T) {
	svc := newDanmakuSettingsService(t)

	c, w := newDanmakuSettingsContext(t, svc, http.MethodPut, "/danmaku/settings",
		`{"merge_sources":true}`, "user-1")
	updateDanmakuSettingsHandler(svc)(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		MergeSources bool `json:"merge_sources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.MergeSources {
		t.Fatal("response should echo merge_sources=true")
	}
	// 落库校验：重新读取应为 true。
	if !svc.Danmaku.MergeSourcesEnabled(t.Context(), "user-1") {
		t.Fatal("merge preference was not persisted")
	}
}

func TestUpdateDanmakuSettingsRejectsMissingField(t *testing.T) {
	svc := newDanmakuSettingsService(t)

	c, w := newDanmakuSettingsContext(t, svc, http.MethodPut, "/danmaku/settings", `{}`, "user-1")
	updateDanmakuSettingsHandler(svc)(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
}

func TestUpdateDanmakuSettingsRequiresAuthentication(t *testing.T) {
	svc := newDanmakuSettingsService(t)

	c, w := newDanmakuSettingsContext(t, svc, http.MethodPut, "/danmaku/settings",
		`{"merge_sources":true}`, "")
	updateDanmakuSettingsHandler(svc)(c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", w.Code, w.Body.String())
	}
}

// config 接口应把当前用户的合并偏好带出去，供面板初始化。
func TestGetDanmakuConfigIncludesPerUserMergePreference(t *testing.T) {
	svc := newDanmakuSettingsService(t)

	c, w := newDanmakuSettingsContext(t, svc, http.MethodGet, "/danmaku/config", "", "user-1")
	getDanmakuConfigHandler(svc)(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var cfg struct {
		MergeSources bool `json:"merge_sources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.MergeSources {
		t.Fatal("default merge preference should be false")
	}

	if err := svc.Danmaku.SetMergeSources(t.Context(), "user-1", true); err != nil {
		t.Fatalf("set: %v", err)
	}
	c2, w2 := newDanmakuSettingsContext(t, svc, http.MethodGet, "/danmaku/config", "", "user-1")
	getDanmakuConfigHandler(svc)(c2)
	if err := json.Unmarshal(w2.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cfg.MergeSources {
		t.Fatal("config should reflect the persisted merge preference")
	}
}

func TestUpdateDanmakuSettingsPersistsAllPlayerPreferences(t *testing.T) {
	svc := newDanmakuSettingsService(t)

	body := "{\"enabled\":false,\"opacity\":0.6,\"font_size\":32,\"area\":0.7,\"merge_sources\":true,\"volume\":0.35,\"source\":\"https://dm.example/base/\",\"app_id\":\"my-app-id\",\"app_key\":\"my-app-secret\"}"
	c, w := newDanmakuSettingsContext(t, svc, http.MethodPut, "/danmaku/settings", body, "user-1")
	updateDanmakuSettingsHandler(svc)(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("my-app-secret")) {
		t.Fatal("response must never expose the application secret")
	}
	var cfg service.DanmakuRenderConfig
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Enabled || cfg.Opacity != "0.6" || cfg.FontSize != "32" || cfg.Area != "0.7" {
		t.Fatalf("unexpected render config: %+v", cfg)
	}
	if !cfg.MergeSources || cfg.Volume != 0.35 {
		t.Fatalf("unexpected user preferences: %+v", cfg)
	}
	if cfg.Source != "https://dm.example/base" || cfg.AppID != "my-app-id" || !cfg.AppKeyConfigured {
		t.Fatalf("unexpected service config: %+v", cfg)
	}

	user, err := svc.Repo.User.FindByID(t.Context(), "user-1")
	if err != nil || user == nil {
		t.Fatalf("read persisted user: %v", err)
	}
	if user.DanmakuAppKey != "my-app-secret" || user.PlayerVolume != 0.35 || user.DanmakuSource != "https://dm.example/base" {
		t.Fatalf("preferences not persisted: %+v", user)
	}
}

func TestUpdateDanmakuSettingsRejectsInvalidSource(t *testing.T) {
	svc := newDanmakuSettingsService(t)

	c, w := newDanmakuSettingsContext(t, svc, http.MethodPut, "/danmaku/settings",
		`{"source":"ftp://dm.example.com"}`, "user-1")
	updateDanmakuSettingsHandler(svc)(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	user, err := svc.Repo.User.FindByID(t.Context(), "user-1")
	if err != nil || user == nil {
		t.Fatalf("read user: %v", err)
	}
	if user.DanmakuSource != "" {
		t.Fatalf("invalid source was persisted: %q", user.DanmakuSource)
	}
}
