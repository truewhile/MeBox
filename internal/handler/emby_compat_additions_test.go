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

// newEmbyCompatTestRouter 构造一个挂载了完整 Emby 路由表的测试引擎。
func newEmbyCompatTestRouter(t *testing.T, secret string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
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
	router := gin.New()
	registerEmbyRoutes(router, secret, &service.Container{
		Repo: repos,
		Emby: service.NewEmbyService(&config.Config{}, zap.NewNop(), repos),
	})
	return router
}

func TestEmbyAdditionalPartsReturnsEmptyArray(t *testing.T) {
	const secret = "test-secret"
	router := newEmbyCompatTestRouter(t, secret)

	req := httptest.NewRequest(http.MethodGet, "/emby/Videos/msgo-series-abc/AdditionalParts", nil)
	req.Header.Set("X-Emby-Token", signedTestToken(t, secret))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	// 必须命中 AdditionalParts 静态路由并返回空数组，而不是被 /Videos/:id/:seg
	// 的 HLS 兜底路由接走返回空 404。
	var payload []any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	if len(payload) != 0 {
		t.Fatalf("expected an empty array, got %v", payload)
	}
}

func TestEmbyItemImagesWithoutTypeReturnsArray(t *testing.T) {
	router := newEmbyCompatTestRouter(t, "test-secret")

	// 不带 Type 的图片清单接口是公开路由，不要求 token，与带 Type 的
	// 图片字节流一致（客户端缓存 URL 时会丢 token）。
	req := httptest.NewRequest(http.MethodGet, "/emby/Items/unknown-item/Images", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct == "" || ct[:16] != "application/json" {
		t.Fatalf("content type = %q, want application/json", ct)
	}
	var payload []any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
}

func TestEmbyItemImagesLowerCaseRouteIsRegistered(t *testing.T) {
	router := newEmbyCompatTestRouter(t, "test-secret")

	req := httptest.NewRequest(http.MethodGet, "/emby/items/unknown-item/images", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}

func TestEmbyUserImageWithoutAvatarReturnsCacheableNotFound(t *testing.T) {
	router := newEmbyCompatTestRouter(t, "test-secret")

	req := httptest.NewRequest(http.MethodGet, "/emby/Users/user-1/Images/Primary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 用户没有头像时 Emby 同样返回 404，但响应必须可缓存，否则客户端会
	// 在每次进入设置页时重复请求（线上曾观察到每分钟一次的重试）。
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=86400" {
		t.Fatalf("Cache-Control = %q, want the cacheable directive", cc)
	}
}
