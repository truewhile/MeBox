package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

func TestHistoryContinueRemovesMissingMedia(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.PlaybackHistory{}, &model.Media{}); err != nil {
		t.Fatal(err)
	}

	valid := model.Media{
		Base:  model.Base{ID: "media-valid"},
		Title: "有效影片",
		Path:  "/media/valid.mkv",
	}
	if err := db.Create(&valid).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if err := db.Create(&model.PlaybackHistory{
		Base:       model.Base{ID: "history-valid"},
		UserID:     "user-1",
		MediaID:    valid.ID,
		PositionMs: 30_000,
		DurationMs: 120_000,
		WatchedAt:  now.Add(-time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PlaybackHistory{
		Base:       model.Base{ID: "history-stale"},
		UserID:     "user-1",
		MediaID:    "media-deleted",
		PositionMs: 60_000,
		DurationMs: 120_000,
		WatchedAt:  now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	repos := repository.New(db)
	svc := &service.Container{
		Log:      zap.NewNop(),
		Repo:     repos,
		Playback: service.NewPlaybackService(zap.NewNop(), repos),
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, "user-1")
		c.Next()
	})
	router.GET("/watch-history/continue", historyContinueHandler(svc))

	req := httptest.NewRequest(http.MethodGet, "/watch-history/continue?limit=10", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body []struct {
		History model.PlaybackHistory `json:"history"`
		Media   model.Media           `json:"media"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 {
		t.Fatalf("continue watching rows = %d, want 1: %s", len(body), rec.Body.String())
	}
	if body[0].Media.ID != valid.ID || body[0].History.ID != "history-valid" {
		t.Fatalf("unexpected row: %#v", body[0])
	}

	var staleCount int64
	if err := db.Unscoped().Model(&model.PlaybackHistory{}).
		Where("user_id = ? AND media_id = ?", "user-1", "media-deleted").
		Count(&staleCount).Error; err != nil {
		t.Fatal(err)
	}
	if staleCount != 0 {
		t.Fatalf("stale history rows remaining = %d, want 0", staleCount)
	}
}
