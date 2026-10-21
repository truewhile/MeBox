package handler

import (
	"bytes"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

func coverTestToken(t *testing.T, secret, userID string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, middleware.Claims{
		UserID: userID,
		Role:   "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   userID,
		},
	})
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

// TestLibraryCoverRouteServesImageThroughFullRouter 走完整的 handler.Register
// 路由表请求封面：既验证路由挂载正确，也验证鉴权链（AuthRequired +
// activeUserRequired）不会把它挡成 404。
func TestLibraryCoverRouteServesImageThroughFullRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "test-secret"

	poster := coverTestJPEG(t, color.RGBA{R: 200, G: 120, B: 60, A: 255})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(poster)
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}, &model.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	user := model.User{Base: model.Base{ID: "user-1"}, Username: "viewer", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	lib := model.Library{Name: "电影", Path: "/media/movies", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	for i := 0; i < 4; i++ {
		row := model.Media{
			Base:      model.Base{ID: "m" + string(rune('a'+i))},
			LibraryID: lib.ID,
			Title:     "Movie " + string(rune('A'+i)),
			Path:      "/media/movies/m" + string(rune('a'+i)) + ".mkv",
			PosterURL: upstream.URL + "/p" + string(rune('1'+i)) + ".jpg",
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("create media: %v", err)
		}
	}

	cfg := &config.Config{
		Cache:   config.CacheConfig{CacheDir: filepath.Join(t.TempDir(), "cache")},
		Secrets: config.SecretsConfig{JWTSecret: secret},
	}
	proxy := service.NewImageProxy(cfg, zap.NewNop())
	proxy.SetAllowedRemoteHostsProvider(func() []string { return []string{parsed.Host} })
	svc := &service.Container{Repo: repos, ImageProxy: proxy}

	engine := gin.New()
	Register(engine, cfg, zap.NewNop(), svc)

	token := coverTestToken(t, secret, user.ID)
	req := httptest.NewRequest(http.MethodGet, "/api/libraries/"+lib.ID+"/cover?w=480&h=270&api_key="+token, nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content type = %q, want image/jpeg", ct)
	}
	img, err := jpeg.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode cover: %v", err)
	}
	if img.Bounds().Dx() != 480 || img.Bounds().Dy() != 270 {
		t.Fatalf("cover size = %v, want 480x270", img.Bounds())
	}
}
