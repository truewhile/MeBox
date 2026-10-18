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

	"github.com/truewhile/MeBox/internal/database"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

// TestSetEmbyMountsEnabledHandler 覆盖「一键停用/启用某账号下全部挂载」：
// 只影响目标账号，且配置保留（不是删除）。
func TestSetEmbyMountsEnabledHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	ctx := t.Context()

	m1 := &model.EmbyMount{AccountID: "acct-1", RemoteViewID: "view-1", Enabled: true}
	m2 := &model.EmbyMount{AccountID: "acct-1", RemoteViewID: "view-2", Enabled: true}
	other := &model.EmbyMount{AccountID: "acct-2", RemoteViewID: "view-3", Enabled: true}
	for _, m := range []*model.EmbyMount{m1, m2, other} {
		if err := repos.EmbyMount.Create(ctx, m); err != nil {
			t.Fatalf("create mount: %v", err)
		}
	}

	svc := &service.Container{
		Repo:       repos,
		EmbyRemote: service.NewEmbyRemoteService(nil, zap.NewNop(), repos, nil),
	}
	router := gin.New()
	router.PUT("/admin/emby/accounts/:id/mounts/enabled", setEmbyMountsEnabledHandler(svc))

	call := func(body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/admin/emby/accounts/acct-1/mounts/enabled", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	w := call(map[string]any{"enabled": false})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK       bool  `json:"ok"`
		Affected int64 `json:"affected"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.Affected != 2 {
		t.Fatalf("expected ok with affected=2, got %+v", resp)
	}

	rows, err := repos.EmbyMount.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		want := row.AccountID != "acct-1" // 目标账号停用，其他账号不受影响
		if row.Enabled != want {
			t.Fatalf("mount %s enabled=%v, want %v", row.ID, row.Enabled, want)
		}
	}

	// 重新启用：挂载记录仍在，只是开关回 true。
	w = call(map[string]any{"enabled": true})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	rows, err = repos.EmbyMount.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected mounts to be preserved, got %d rows", len(rows))
	}
	for _, row := range rows {
		if !row.Enabled {
			t.Fatalf("mount %s should be enabled again", row.ID)
		}
	}

	// 缺少 enabled 字段必须拒绝，避免误把全部挂载停掉。
	if w := call(map[string]any{}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing enabled, got %d: %s", w.Code, w.Body.String())
	}
}
