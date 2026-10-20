package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/service"
)

func realtimeTestRouter(hub *service.Hub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	authed := router.Group("/api")
	authed.Use(middleware.AuthRequired("realtime-test"))
	registerAuthedRealtimeRoutes(authed, &service.Container{WSHub: hub})
	return router
}

func realtimeTestToken(t *testing.T, role string) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, middleware.Claims{
		UserID: "viewer", Role: role,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}).SignedString([]byte("realtime-test"))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestRealtimeStreamsRejectOrdinaryUsers(t *testing.T) {
	router := realtimeTestRouter(nil)
	for _, path := range []string{"/api/ws", "/api/events"} {
		for _, role := range []string{"", "user"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			want := http.StatusUnauthorized
			if role != "" {
				req.Header.Set("Authorization", "Bearer "+realtimeTestToken(t, role))
				want = http.StatusForbidden
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != want {
				t.Fatalf("%s role %q: status %d, want %d", path, role, recorder.Code, want)
			}
		}
	}
}

func TestAdminWebSocketReceivesTaskEvents(t *testing.T) {
	hub := service.NewHub(zap.NewNop())
	go hub.Run()
	defer hub.Stop()
	server := httptest.NewServer(realtimeTestRouter(hub))
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/ws?token=" + realtimeTestToken(t, "admin")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				hub.Publish("task", service.BackgroundTask{ID: "task-1", SourcePath: "/media/private"})
			}
		}
	}()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event service.Event
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Topic != "task" || event.Payload.(map[string]any)["source_path"] != "/media/private" {
		t.Fatalf("event = %#v", event)
	}
}
