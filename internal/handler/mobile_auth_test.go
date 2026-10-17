package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

const mobileTestSecret = "mobile-test-secret"

func newMobileTestEnv(t *testing.T) (*gin.Engine, *service.Container) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// :memory: 只有单连接能看到同一份数据；handler 测试足够，且避免
	// shared cache 名字在并行测试间串库。
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := db.AutoMigrate(
		&model.User{}, &model.RefreshToken{}, &model.AuthSession{}, &model.Setting{},
		&model.UserDevice{}, &model.UserPermission{},
	); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	cfg := &config.Config{Secrets: config.SecretsConfig{JWTSecret: mobileTestSecret}}
	svc := service.NewWithVersion(cfg, zap.NewNop(), repos, "MeBox-test")

	router := gin.New()
	Register(router, cfg, zap.NewNop(), svc)
	return router, svc
}

func seedMobileUser(t *testing.T, svc *service.Container, username, password string) *model.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := &model.User{Username: username, PasswordHash: string(hash), Role: "user", Tier: "free", IsActive: true}
	if err := svc.Repo.User.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func mobileJSONRequest(t *testing.T, router *gin.Engine, method, path, token string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func decodeMobileJSON(t *testing.T, w *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
}

func TestMobileServerInfoAnonymousAndCacheable(t *testing.T) {
	router, _ := newMobileTestEnv(t)
	w := mobileJSONRequest(t, router, http.MethodGet, "/api/v1/server-info", "", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var info service.ServerInfo
	decodeMobileJSON(t, w, &info)
	if info.ServerID == "" || info.ServerVersion != "MeBox-test" || info.APIVersion != "v1" {
		t.Fatalf("unexpected server info: %+v", info)
	}
	if !info.Capabilities.DeviceSessions {
		t.Fatalf("device_sessions capability should be true: %+v", info.Capabilities)
	}
	if info.Capabilities.PlaybackSessions || info.Capabilities.ReaderProgressV1 {
		t.Fatalf("unimplemented capabilities must stay false: %+v", info.Capabilities)
	}
	if info.MinSupportedAppVersionCode < 1 {
		t.Fatalf("min_supported_app_version_code = %d", info.MinSupportedAppVersionCode)
	}

	// 匿名访问不应要求 Authorization，也不应记录 cookie。
	if len(w.Result().Cookies()) != 0 {
		t.Fatalf("server-info should not set cookies")
	}
}

func TestMobileAuthLifecycle(t *testing.T) {
	router, svc := newMobileTestEnv(t)
	user := seedMobileUser(t, svc, "alice", "secret-123")

	// 1. 错误密码：401 + 稳定错误码 + request_id。
	w := mobileJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": "alice", "password": "wrong", "device_id": "device-1",
	}, map[string]string{"X-Request-ID": "mobile-test-0001"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d body=%s", w.Code, w.Body.String())
	}
	var authErr mobileErrorBody
	decodeMobileJSON(t, w, &authErr)
	if authErr.Code != mobileCodeInvalidCredentials || authErr.RequestID != "mobile-test-0001" {
		t.Fatalf("unexpected auth error: %+v", authErr)
	}
	if authErr.Retryable {
		t.Fatalf("invalid credentials must not be retryable: %+v", authErr)
	}

	// 2. 缺少 device_id：400 INVALID_ARGUMENT。
	w = mobileJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": "alice", "password": "secret-123",
	}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing device id status = %d body=%s", w.Code, w.Body.String())
	}
	decodeMobileJSON(t, w, &authErr)
	if authErr.Code != mobileCodeInvalidArgument {
		t.Fatalf("code = %q, want %q", authErr.Code, mobileCodeInvalidArgument)
	}

	// 3. 正常登录。
	w = mobileJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": "alice", "password": "secret-123", "device_id": "device-1",
		"device_name": "Pixel 9", "client_name": "MeBox Android", "client_version": "1.0.0",
	}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", w.Code, w.Body.String())
	}
	var loginResp struct {
		AuthSessionID string            `json:"auth_session_id"`
		User          mobileUserPayload `json:"user"`
		Tokens        service.TokenPair `json:"tokens"`
	}
	decodeMobileJSON(t, w, &loginResp)
	if loginResp.AuthSessionID == "" || loginResp.Tokens.AccessToken == "" || loginResp.Tokens.RefreshToken == "" {
		t.Fatalf("incomplete login response: %+v", loginResp)
	}
	if loginResp.User.ID != user.ID || loginResp.User.Username != "alice" {
		t.Fatalf("unexpected user payload: %+v", loginResp.User)
	}
	claims, err := svc.Token.ValidateAccessToken(loginResp.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("validate access token: %v", err)
	}
	if claims.Sid != loginResp.AuthSessionID {
		t.Fatalf("token sid = %q, want %q", claims.Sid, loginResp.AuthSessionID)
	}
	rt, err := svc.Repo.RefreshToken.FindByHash(t.Context(), repository.HashToken(loginResp.Tokens.RefreshToken))
	if err != nil || rt == nil || rt.AuthSessionID != loginResp.AuthSessionID {
		t.Fatalf("refresh token not bound to session: %+v err=%v", rt, err)
	}

	// 4. 会话读取。
	w = mobileJSONRequest(t, router, http.MethodGet, "/api/v1/auth/session", loginResp.Tokens.AccessToken, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("session status = %d body=%s", w.Code, w.Body.String())
	}
	var sessionResp struct {
		AuthSessionID string                `json:"auth_session_id"`
		User          mobileUserPayload     `json:"user"`
		Session       *mobileSessionPayload `json:"session"`
	}
	decodeMobileJSON(t, w, &sessionResp)
	if sessionResp.AuthSessionID != loginResp.AuthSessionID || sessionResp.Session == nil {
		t.Fatalf("unexpected session response: %+v", sessionResp)
	}
	if sessionResp.Session.DeviceID != "device-1" || sessionResp.Session.DeviceName != "Pixel 9" {
		t.Fatalf("unexpected session payload: %+v", sessionResp.Session)
	}

	// 5. 刷新：新令牌继续归属同一会话，旧刷新令牌被撤销。
	w = mobileJSONRequest(t, router, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{
		"refresh_token": loginResp.Tokens.RefreshToken,
	}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", w.Code, w.Body.String())
	}
	var refreshResp struct {
		AuthSessionID string            `json:"auth_session_id"`
		Tokens        service.TokenPair `json:"tokens"`
	}
	decodeMobileJSON(t, w, &refreshResp)
	if refreshResp.AuthSessionID != loginResp.AuthSessionID || refreshResp.Tokens.AccessToken == "" {
		t.Fatalf("unexpected refresh response: %+v", refreshResp)
	}
	if refreshResp.Tokens.RefreshToken == loginResp.Tokens.RefreshToken {
		t.Fatal("refresh token should rotate")
	}
	oldRT, err := svc.Repo.RefreshToken.FindByHash(t.Context(), repository.HashToken(loginResp.Tokens.RefreshToken))
	if err != nil || oldRT == nil || !oldRT.Revoked {
		t.Fatalf("old refresh token should be revoked: %+v err=%v", oldRT, err)
	}

	// 6. 用刷新后的访问令牌访问旧 API，确认 sid 会话校验在旧入口同样生效。
	w = mobileJSONRequest(t, router, http.MethodGet, "/api/me", refreshResp.Tokens.AccessToken, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("old api with fresh token status = %d body=%s", w.Code, w.Body.String())
	}

	// 7. 退出：成功且可重复。
	for i := 0; i < 2; i++ {
		w = mobileJSONRequest(t, router, http.MethodPost, "/api/v1/auth/logout", refreshResp.Tokens.AccessToken, nil, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("logout #%d status = %d body=%s", i+1, w.Code, w.Body.String())
		}
	}

	// 8. 会话撤销后：v1 会话接口 401 SESSION_REVOKED。
	w = mobileJSONRequest(t, router, http.MethodGet, "/api/v1/auth/session", refreshResp.Tokens.AccessToken, nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("session after logout status = %d body=%s", w.Code, w.Body.String())
	}
	decodeMobileJSON(t, w, &authErr)
	if authErr.Code != mobileCodeSessionRevoked {
		t.Fatalf("session after logout code = %q, want %q", authErr.Code, mobileCodeSessionRevoked)
	}

	// 9. 被踢后的未过期 JWT 不能绕过旧 API。
	w = mobileJSONRequest(t, router, http.MethodGet, "/api/me", refreshResp.Tokens.AccessToken, nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("old api after logout status = %d body=%s", w.Code, w.Body.String())
	}

	// 10. 刷新令牌随会话一起撤销。
	w = mobileJSONRequest(t, router, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{
		"refresh_token": refreshResp.Tokens.RefreshToken,
	}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout status = %d body=%s", w.Code, w.Body.String())
	}
	decodeMobileJSON(t, w, &authErr)
	if authErr.Code != mobileCodeSessionRevoked {
		t.Fatalf("refresh after logout code = %q, want %q", authErr.Code, mobileCodeSessionRevoked)
	}
}

func TestMobileV1RejectsLegacyTokenWithoutSid(t *testing.T) {
	router, svc := newMobileTestEnv(t)
	user := seedMobileUser(t, svc, "legacy", "secret-123")
	pair, err := svc.Token.IssuePair(t.Context(), user.ID, user.Role, user.Tier)
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := svc.Token.ValidateAccessToken(pair.AccessToken); err != nil || claims.Sid != "" {
		t.Fatalf("legacy token unexpectedly has sid: %+v err=%v", claims, err)
	}

	w := mobileJSONRequest(t, router, http.MethodGet, "/api/v1/auth/session", pair.AccessToken, nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("legacy token status = %d body=%s", w.Code, w.Body.String())
	}
	var body mobileErrorBody
	decodeMobileJSON(t, w, &body)
	if body.Code != mobileCodeInvalidCredentials {
		t.Fatalf("legacy token code = %q, want %q", body.Code, mobileCodeInvalidCredentials)
	}

	// 旧令牌仍可访问旧 API（兼容路径不能被新协议破坏）。
	w = mobileJSONRequest(t, router, http.MethodGet, "/api/me", pair.AccessToken, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("legacy token on old api status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestMobileRefreshRejectsUnboundLegacyToken(t *testing.T) {
	router, svc := newMobileTestEnv(t)
	user := seedMobileUser(t, svc, "bob", "secret-123")
	pair, err := svc.Token.IssuePair(t.Context(), user.ID, user.Role, user.Tier)
	if err != nil {
		t.Fatal(err)
	}
	w := mobileJSONRequest(t, router, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{
		"refresh_token": pair.RefreshToken,
	}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("legacy refresh status = %d body=%s", w.Code, w.Body.String())
	}
	var body mobileErrorBody
	decodeMobileJSON(t, w, &body)
	if body.Code != mobileCodeInvalidCredentials {
		t.Fatalf("legacy refresh code = %q, want %q", body.Code, mobileCodeInvalidCredentials)
	}
}

func TestMobileDeviceKickRevokesSession(t *testing.T) {
	router, svc := newMobileTestEnv(t)
	user := seedMobileUser(t, svc, "carol", "secret-123")

	w := mobileJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": "carol", "password": "secret-123", "device_id": "device-kick",
		"device_name": "Kick Me", "client_name": "MeBox Android",
	}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", w.Code, w.Body.String())
	}
	var loginResp struct {
		AuthSessionID string            `json:"auth_session_id"`
		Tokens        service.TokenPair `json:"tokens"`
	}
	decodeMobileJSON(t, w, &loginResp)

	if err := svc.Device.KickDevice(t.Context(), user.ID, "device-kick"); err != nil {
		t.Fatalf("kick device: %v", err)
	}
	session, err := svc.Repo.AuthSession.FindByID(t.Context(), loginResp.AuthSessionID)
	if err != nil || session == nil || !session.Revoked {
		t.Fatalf("session should be revoked after device kick: %+v err=%v", session, err)
	}
	w = mobileJSONRequest(t, router, http.MethodGet, "/api/v1/auth/session", loginResp.Tokens.AccessToken, nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("session after kick status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestMobileKickOneDeviceKeepsOtherSessions(t *testing.T) {
	router, svc := newMobileTestEnv(t)
	seedMobileUser(t, svc, "erin", "secret-123")

	login := func(deviceID string) string {
		w := mobileJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
			"username": "erin", "password": "secret-123", "device_id": deviceID,
			"device_name": deviceID, "client_name": "MeBox Android",
		}, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("login %s status = %d body=%s", deviceID, w.Code, w.Body.String())
		}
		var resp struct {
			Tokens service.TokenPair `json:"tokens"`
		}
		decodeMobileJSON(t, w, &resp)
		return resp.Tokens.AccessToken
	}

	tokenA := login("device-a")
	tokenB := login("device-b")

	if err := svc.Device.KickDevice(t.Context(), svcUserID(t, svc, "erin"), "device-a"); err != nil {
		t.Fatalf("kick device a: %v", err)
	}

	w := mobileJSONRequest(t, router, http.MethodGet, "/api/v1/auth/session", tokenA, nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("kicked session status = %d body=%s", w.Code, w.Body.String())
	}
	w = mobileJSONRequest(t, router, http.MethodGet, "/api/v1/auth/session", tokenB, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("other device status = %d body=%s", w.Code, w.Body.String())
	}
}

func svcUserID(t *testing.T, svc *service.Container, username string) string {
	t.Helper()
	u, err := svc.Repo.User.FindByUsername(t.Context(), username)
	if err != nil || u == nil {
		t.Fatalf("find user %s: %v", username, err)
	}
	return u.ID
}

func TestMobileAuthDefaultsAndSessionExpiryShape(t *testing.T) {
	router, svc := newMobileTestEnv(t)
	seedMobileUser(t, svc, "dave", "secret-123")

	w := mobileJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": "dave", "password": "secret-123", "device_id": "device-default",
	}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", w.Code, w.Body.String())
	}
	var loginResp struct {
		AuthSessionID string            `json:"auth_session_id"`
		Tokens        service.TokenPair `json:"tokens"`
	}
	decodeMobileJSON(t, w, &loginResp)
	session, err := svc.Repo.AuthSession.FindByID(t.Context(), loginResp.AuthSessionID)
	if err != nil || session == nil {
		t.Fatalf("load session: %v", err)
	}
	if session.ClientName != "MeBox Android" {
		t.Fatalf("client_name default = %q", session.ClientName)
	}
	if session.ExpiresAt.Before(time.Now()) {
		t.Fatalf("session expiry in the past: %v", session.ExpiresAt)
	}
	if loginResp.Tokens.TokenType != "Bearer" || loginResp.Tokens.ExpiresIn <= 0 {
		t.Fatalf("unexpected token metadata: %+v", loginResp.Tokens)
	}
}
