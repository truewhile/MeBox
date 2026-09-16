package handler

import (
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/service"
)

func TestAuthenticatedRouteSurfacesAreRegistered(t *testing.T) {
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
		"GET /api/me",
		"GET /api/me/pinned-libraries",
		"PUT /api/me/pinned-libraries",
		"GET /api/me/library-tags",
		"PUT /api/me/library-tags",
		"GET /api/auth/permissions",
		"GET /api/libraries",
		"GET /api/media",
		"GET /api/stream/:id",
		"GET /api/storage",
		"GET /api/watch-history",
		"GET /api/playback/:id/info",
	} {
		if !routes[want] {
			t.Fatalf("%s route is not registered", want)
		}
	}
}
