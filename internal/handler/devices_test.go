package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// newDeviceTestEnv 搭一个只挂设备/Telegram 路由的最小环境，并预置两个用户，
// 用于验证「只能操作自己的设备」这条边界。
func newDeviceTestEnv(t *testing.T) (*gin.Engine, *service.Container) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.UserDevice{}, &model.Setting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	svc := &service.Container{Repo: repos, Log: zap.NewNop()}
	svc.Device = service.NewDeviceService(zap.NewNop(), repos)
	svc.Device.SetSessionTracker(service.NewSessionTrackerService(zap.NewNop()))
	svc.Telegram = service.NewTelegramService(zap.NewNop(), repos)

	const secret = "test-secret"
	router := gin.New()
	authed := router.Group("/api", func(c *gin.Context) {
		// 测试里直接注入会话身份，绕开真实 JWT 解析。
		if uid := c.GetHeader("X-Test-User"); uid != "" {
			c.Set(middleware.CtxUserID, uid)
			c.Set(middleware.CtxUserRole, c.GetHeader("X-Test-Role"))
		}
		c.Next()
	})
	authed.GET("/me/devices", myDevicesHandler(svc))
	authed.POST("/me/devices/kick-all", myKickAllDevicesHandler(svc))
	authed.POST("/me/devices/:deviceID/kick", myKickDeviceHandler(svc))
	authed.GET("/me/telegram", getTelegramStatusHandler(svc))
	authed.POST("/me/telegram/bind-code", startTelegramBindHandler(svc))
	authed.DELETE("/me/telegram", unbindTelegramHandler(svc))
	authed.GET("/admin/users/:id/devices", adminUserDevicesHandler(svc))
	authed.POST("/admin/users/:id/devices/:deviceID/kick", adminKickUserDeviceHandler(svc))
	_ = secret
	return router, svc
}

func seedDeviceUsers(t *testing.T, svc *service.Container) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{"user-a", "user-b"} {
		if err := svc.Repo.User.Create(ctx, &model.User{
			Base: model.Base{ID: id}, Username: id, PasswordHash: "x", Role: "user", IsActive: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func doJSON(t *testing.T, router *gin.Engine, method, path, userID, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Test-User", userID)
	if role != "" {
		req.Header.Set("X-Test-Role", role)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// /me/devices 只能返回调用者自己的设备。
func TestMyDevicesScopedToCaller(t *testing.T) {
	router, svc := newDeviceTestEnv(t)
	seedDeviceUsers(t, svc)
	ctx := context.Background()

	svc.Device.RecordLogin(ctx, "user-a", "dev-a", "A-Phone", "Infuse", "1.1.1.1")
	svc.Device.RecordLogin(ctx, "user-b", "dev-b", "B-Phone", "Infuse", "2.2.2.2")

	w := doJSON(t, router, http.MethodGet, "/api/me/devices", "user-a", "user")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Devices []struct {
			DeviceID string `json:"device_id"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Devices) != 1 || payload.Devices[0].DeviceID != "dev-a" {
		t.Fatalf("devices = %+v, want only dev-a", payload.Devices)
	}
	// 设备指纹属于内部判定标识，不能下发。
	if strings.Contains(w.Body.String(), "fingerprint") {
		t.Fatalf("response must not expose fingerprint: %s", w.Body.String())
	}
}

// 踢别人的设备必须失败：/me 路由用会话身份，deviceID 属于他人时查不到。
func TestKickForeignDeviceFails(t *testing.T) {
	router, svc := newDeviceTestEnv(t)
	seedDeviceUsers(t, svc)
	svc.Device.RecordLogin(context.Background(), "user-b", "dev-b", "B-Phone", "Infuse", "2.2.2.2")

	w := doJSON(t, router, http.MethodPost, "/api/me/devices/dev-b/kick", "user-a", "user")
	if w.Code == http.StatusNoContent {
		t.Fatal("user-a must not be able to kick user-b's device")
	}
}

func TestMyKickOwnDeviceSucceeds(t *testing.T) {
	router, svc := newDeviceTestEnv(t)
	seedDeviceUsers(t, svc)
	svc.Device.RecordLogin(context.Background(), "user-a", "dev-a", "A-Phone", "Infuse", "1.1.1.1")

	w := doJSON(t, router, http.MethodPost, "/api/me/devices/dev-a/kick", "user-a", "user")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

// 管理员接口对不存在的用户返回 404，避免把「用户不存在」和「用户没有设备」
// 混成同一个空列表。
func TestAdminDevicesUnknownUserReturns404(t *testing.T) {
	router, svc := newDeviceTestEnv(t)
	seedDeviceUsers(t, svc)

	w := doJSON(t, router, http.MethodGet, "/api/admin/users/nope/devices", "admin-1", "admin")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestTelegramStatusAndBindCode(t *testing.T) {
	router, svc := newDeviceTestEnv(t)
	seedDeviceUsers(t, svc)

	w := doJSON(t, router, http.MethodGet, "/api/me/telegram", "user-a", "user")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"bound":false`) {
		t.Fatalf("body = %s, want bound=false", w.Body.String())
	}

	w = doJSON(t, router, http.MethodPost, "/api/me/telegram/bind-code", "user-a", "user")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var code struct {
		Code      string `json:"code"`
		ExpiresIn int    `json:"expires_in_seconds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &code); err != nil {
		t.Fatal(err)
	}
	if len(code.Code) != 6 {
		t.Fatalf("code = %q, want 6 chars", code.Code)
	}
	if code.ExpiresIn <= 0 {
		t.Fatalf("expires_in_seconds = %d, want > 0", code.ExpiresIn)
	}
}

func TestAdminSettingsMasksBotToken(t *testing.T) {
	router, svc := newDeviceTestEnv(t)
	ctx := context.Background()
	if err := svc.Repo.Setting.Set(ctx, service.SettingTelegramBotToken, "123456:AAHsecretTOKEN"); err != nil {
		t.Fatal(err)
	}
	router.GET("/api/admin/settings", listSettingsHandler(svc))
	router.PUT("/api/admin/settings", updateSettingHandler(svc))

	w := doJSON(t, router, http.MethodGet, "/api/admin/settings", "admin-1", "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "AAHsecretTOKEN") {
		t.Fatalf("bot token leaked: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "***") {
		t.Fatalf("bot token should be masked: %s", w.Body.String())
	}
}

// 把脱敏值原样提交回来时，必须保留库里真实 Token —— 否则一次保存就把凭据毁掉。
func TestSavingMaskedTokenKeepsRealValue(t *testing.T) {
	router, svc := newDeviceTestEnv(t)
	ctx := context.Background()
	const real = "123456:AAHsecretTOKEN"
	if err := svc.Repo.Setting.Set(ctx, service.SettingTelegramBotToken, real); err != nil {
		t.Fatal(err)
	}
	router.PUT("/api/admin/settings", updateSettingHandler(svc))

	body := `{"key":"telegram.bot_token","value":"12***EN"}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", "admin-1")
	req.Header.Set("X-Test-Role", "admin")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	got, err := svc.Repo.Setting.Get(ctx, service.SettingTelegramBotToken)
	if err != nil {
		t.Fatal(err)
	}
	if got != real {
		t.Fatalf("stored token = %q, want the original value preserved", got)
	}
}
