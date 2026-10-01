package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

// TestStrmAdminRoutes 冒烟测试 STRM 管理端点注册与 401 拦截。
func TestStrmAdminRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	Register(router, &config.Config{
		Secrets: config.SecretsConfig{JWTSecret: "test-secret"},
	}, zap.NewNop(), &service.Container{Log: zap.NewNop()})

	routes := map[string]bool{}
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"GET /api/admin/strm/accounts",
		"POST /api/admin/strm/accounts",
		"PUT /api/admin/strm/accounts/:id",
		"DELETE /api/admin/strm/accounts/:id",
		"POST /api/admin/strm/accounts/:id/test",
		"GET /api/admin/strm/accounts/:id/list",
		"POST /api/admin/strm/accounts/:id/oauth/start",
		"POST /api/admin/strm/accounts/:id/oauth/poll",
		"GET /api/admin/strm/settings",
		"PUT /api/admin/strm/settings",
		"GET /api/admin/strm/paths",
		"POST /api/admin/strm/paths",
		"PUT /api/admin/strm/paths/:id",
		"DELETE /api/admin/strm/paths/:id",
		"POST /api/admin/strm/paths/:id/sync",
		"POST /api/admin/strm/paths/:id/cancel",
		"GET /api/admin/strm/records",
		"GET /api/admin/strm/downloads",
		"POST /api/admin/strm/downloads/:id/cancel",
		"POST /api/admin/strm/downloads/:id/retry",
		"POST /api/admin/strm/downloads/clear-finished",
		"POST /api/admin/strm/downloads/clear-canceled",
		"POST /api/admin/strm/downloads/retry-failed",
		"POST /api/admin/strm/downloads/cancel-pending",
		"GET /api/admin/strm/uploads",
		"POST /api/admin/strm/uploads/:id/cancel",
		"POST /api/admin/strm/uploads/:id/retry",
		"POST /api/admin/strm/uploads/clear-done",
		"POST /api/admin/strm/uploads/clear-finished",
		"POST /api/admin/strm/uploads/clear-canceled",
		"POST /api/admin/strm/uploads/retry-failed",
		"POST /api/admin/strm/uploads/cancel-pending",
		"GET /api/strm/play/:provider/:file",
		"HEAD /api/strm/play/:provider/:file",
	} {
		if !routes[want] {
			t.Fatalf("%s route is not registered", want)
		}
	}
}

// TestStrmAccountsCRUD 用内存库走一遍账号/设置/同步目录接口。
func TestStrmAccountsCRUD(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:strm_handler?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.StrmAccount{}, &model.StrmSyncPath{}, &model.StrmSyncRecord{},
		&model.StrmDownloadTask{}, &model.StrmUploadTask{}, &model.Setting{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	svc := service.NewWithVersion(&config.Config{}, zap.NewNop(), repos, "test")
	router := gin.New()
	Register(router, &config.Config{Secrets: config.SecretsConfig{JWTSecret: "test-secret"}}, zap.NewNop(), svc)

	// 未登录访问应 401
	req := httptest.NewRequest(http.MethodGet, "/api/admin/strm/accounts", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /admin/strm/accounts = %d, want 401", w.Code)
	}

	// 公开播放端点（本地提供方路径校验失败 → 404/400，不 panic）
	req = httptest.NewRequest(http.MethodGet, "/api/strm/play/local/video.mkv?path=%2Ftmp%2Fnope.mkv", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == http.StatusInternalServerError {
		t.Fatalf("strm play endpoint errored: %d", w.Code)
	}
	videoPath := filepath.Join(t.TempDir(), "sample.mkv")
	if err := os.WriteFile(videoPath, []byte("fake-video-bytes"), 0o644); err != nil {
		t.Fatalf("write test video: %v", err)
	}
	if err := repos.StrmSyncPath.Create(t.Context(), &model.StrmSyncPath{
		Name:       "local-test",
		Provider:   model.StrmProviderLocal,
		RemotePath: filepath.Dir(videoPath),
		Enabled:    true,
	}); err != nil {
		t.Fatalf("create local sync path: %v", err)
	}

	// VidHub 等客户端会先用 HEAD 探测 STRM 播放源，不能返回 405/404。
	req = httptest.NewRequest(http.MethodHead, "/api/strm/play/local/video.mkv?path="+url.QueryEscape(videoPath), nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HEAD strm play endpoint = %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("HEAD strm play endpoint returned body: %q", w.Body.String())
	}
}
