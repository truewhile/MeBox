package handler

import (
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

func TestRecordProgressHandlerIgnoresStaleSessionSequence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.PlaybackHistory{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	svc := &service.Container{
		Repo:     repos,
		Playback: service.NewPlaybackService(zap.NewNop(), repos),
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, "user-1")
		c.Next()
	})
	router.POST("/history", recordProgressHandler(svc))

	post := func(body string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/history", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}

	if code := post(`{"media_id":"m-1","position_ms":90000,"duration_ms":120000,"session_id":"s-1","session_started_at_ms":1000,"sequence":2}`); code != http.StatusNoContent {
		t.Fatalf("newer progress status = %d", code)
	}
	if code := post(`{"media_id":"m-1","position_ms":10000,"duration_ms":120000,"session_id":"s-1","session_started_at_ms":1000,"sequence":1}`); code != http.StatusNoContent {
		t.Fatalf("stale progress status = %d", code)
	}

	var got model.PlaybackHistory
	if err := db.Where("user_id = ? AND media_id = ?", "user-1", "m-1").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.PositionMs != 90_000 || got.Sequence != 2 {
		t.Fatalf("stale handler request overwrote newer state: %#v", got)
	}
}
