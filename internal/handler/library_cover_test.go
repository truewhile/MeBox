package handler

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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

func coverTestJPEG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 160, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 160; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// newLibraryCoverTestRouter 构造一个带真实图片代理的路由：成员海报来自
// 本地 httptest 服务，走的是与生产一致的 ImageProxy 抓取路径。
func newLibraryCoverTestRouter(t *testing.T) (*gin.Engine, *repository.Container, *httptest.Server) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	poster := coverTestJPEG(t, color.RGBA{R: 200, G: 120, B: 60, A: 255})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(poster)
	}))
	t.Cleanup(upstream.Close)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	cfg := &config.Config{Cache: config.CacheConfig{CacheDir: filepath.Join(t.TempDir(), "cache")}}
	proxy := service.NewImageProxy(cfg, zap.NewNop())
	proxy.SetAllowedRemoteHostsProvider(func() []string { return []string{parsed.Host} })

	svc := &service.Container{Repo: repos, ImageProxy: proxy}
	router := gin.New()
	router.GET("/libraries/:id/cover", libraryCoverHandler(svc))
	router.HEAD("/libraries/:id/cover", libraryCoverHandler(svc))
	return router, repos, upstream
}

func seedCoverLibrary(t *testing.T, repos *repository.Container, posterBase string, coverURL string) model.Library {
	t.Helper()
	lib := model.Library{Name: "电影", Path: "/media/movies", Type: "movie", Enabled: true, CoverURL: coverURL}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	for i := 0; i < 4; i++ {
		row := model.Media{
			Base:      model.Base{ID: "m" + string(rune('a'+i))},
			LibraryID: lib.ID,
			Title:     "Movie " + string(rune('A'+i)),
			Path:      "/media/movies/m" + string(rune('a'+i)) + ".mkv",
			PosterURL: posterBase + "/p" + string(rune('1'+i)) + ".jpg",
		}
		if err := repos.DB.Create(&row).Error; err != nil {
			t.Fatalf("create media: %v", err)
		}
	}
	return lib
}

// TestLibraryCoverHandlerServesGeneratedCollage 覆盖 Web/App 的封面接口：
// 返回的是服务端拼好的 16:9 图片，而不是单张成员海报。
func TestLibraryCoverHandlerServesGeneratedCollage(t *testing.T) {
	router, repos, upstream := newLibraryCoverTestRouter(t)
	lib := seedCoverLibrary(t, repos, upstream.URL, "")

	req := httptest.NewRequest(http.MethodGet, "/libraries/"+lib.ID+"/cover?w=480&h=270", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

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
	if etag := w.Header().Get("ETag"); etag == "" {
		t.Fatal("expected an ETag so clients can revalidate")
	}

	// 条件请求应拿到 304。
	req = httptest.NewRequest(http.MethodGet, "/libraries/"+lib.ID+"/cover?w=480&h=270", nil)
	req.Header.Set("If-None-Match", w.Header().Get("ETag"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional request status = %d, want 304", rec.Code)
	}
}

func TestLibraryCoverHandlerUnknownLibraryReturns404(t *testing.T) {
	router, _, _ := newLibraryCoverTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/libraries/missing/cover", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// TestLibraryCoverHandlerServesCustomCover 管理员填了 cover_url 时必须继续
// 用自定义封面，不能在不知不觉中换成拼图。
func TestLibraryCoverHandlerServesCustomCover(t *testing.T) {
	router, repos, upstream := newLibraryCoverTestRouter(t)
	lib := seedCoverLibrary(t, repos, upstream.URL, upstream.URL+"/custom.jpg")

	req := httptest.NewRequest(http.MethodGet, "/libraries/"+lib.ID+"/cover", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if _, err := jpeg.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Fatalf("custom cover should be an image: %v", err)
	}
}

// TestEmbyLibraryPrimaryUsesGeneratedCollage 保证 Emby 客户端请求库主图时
// 拿到的也是拼图（与 Web/App 同一份生成逻辑）。
func TestEmbyLibraryPrimaryUsesGeneratedCollage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	poster := coverTestJPEG(t, color.RGBA{G: 180, R: 90, B: 60, A: 255})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(poster)
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)

	repos := repository.New(db)
	lib := model.Library{Name: "剧集", Path: "/media/tv", Type: "tv", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	for i := 0; i < 4; i++ {
		row := model.Media{
			Base:      model.Base{ID: "e" + string(rune('a'+i))},
			LibraryID: lib.ID,
			Title:     "Show " + string(rune('A'+i)),
			Path:      "/media/tv/show" + string(rune('a'+i)) + "/S01E01.mkv",
			SeasonNum: 1, EpisodeNum: 1,
			PosterURL: upstream.URL + "/p" + string(rune('1'+i)) + ".jpg",
		}
		if err := repos.DB.Create(&row).Error; err != nil {
			t.Fatalf("create media: %v", err)
		}
	}

	cfg := &config.Config{Cache: config.CacheConfig{CacheDir: filepath.Join(t.TempDir(), "cache")}}
	proxy := service.NewImageProxy(cfg, zap.NewNop())
	proxy.SetAllowedRemoteHostsProvider(func() []string { return []string{parsed.Host} })
	svc := &service.Container{
		Repo:       repos,
		Emby:       service.NewEmbyService(cfg, zap.NewNop(), repos),
		ImageProxy: proxy,
	}
	router := gin.New()
	registerEmbyRoutes(router, "test-secret", svc)

	req := httptest.NewRequest(http.MethodGet, "/Items/"+lib.ID+"/Images/Primary?maxWidth=400&maxHeight=300", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	img, err := jpeg.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode emby library cover: %v", err)
	}
	// 请求的是 400×300 的盒子，拼图按 16:9 装进去 → 400×225。
	if img.Bounds().Dx() != 400 || img.Bounds().Dy() != 225 {
		t.Fatalf("cover size = %v, want 400x225 (16:9 inside the requested box)", img.Bounds())
	}
}
