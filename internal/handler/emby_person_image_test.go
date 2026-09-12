package handler

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/service"
)

func TestEmbyItemImageRouteResolvesTMDbPersonID(t *testing.T) {
	imageData, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9Zl9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/t/p/w300/actor.jpg" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageData)
	}))
	defer imageServer.Close()

	cfg := &config.Config{
		Cache:   config.CacheConfig{CacheDir: t.TempDir()},
		Secrets: config.SecretsConfig{TMDbImageProxy: imageServer.URL + "/t/p"},
	}
	tmdb := service.NewTMDbProvider(cfg, zap.NewNop(), nil)
	emby := service.NewEmbyService(cfg, zap.NewNop(), nil).SetTMDbProvider(tmdb)
	proxy := service.NewImageProxy(cfg, zap.NewNop())
	imageURL, _ := url.Parse(imageServer.URL)
	proxy.SetAllowedRemoteHostsProvider(func() []string { return []string{imageURL.Host} })
	svc := &service.Container{Emby: emby, ImageProxy: proxy}

	raw, err := emby.PersonImageURL(t.Context(), "person~tmdb~101", "Primary", "tmdb:/actor.jpg?p2")
	if err != nil || raw != imageServer.URL+"/t/p/w300/actor.jpg" {
		t.Fatalf("resolved raw=%q err=%v", raw, err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{
		{Key: "id", Value: "person~tmdb~101"},
		{Key: "type", Value: "Primary"},
	}
	c.Request = httptest.NewRequest(http.MethodGet, "/emby/Items/person~tmdb~101/Images/Primary?tag=tmdb%3A%2Factor.jpg%3Fp2", nil)
	embyItemImageHandler(svc)(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := len(w.Body.Bytes()); got != len(imageData) {
		t.Fatalf("image bytes=%d want %d", got, len(imageData))
	}
}
