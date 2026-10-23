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

// include_media=0 只是不把 media 写进响应，行数和可见性判断都不变。
func TestHistoryListCanOmitEmbeddedMedia(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.PlaybackHistory{}, &model.Media{}); err != nil {
		t.Fatal(err)
	}

	media := model.Media{
		Base:  model.Base{ID: "media-1"},
		Title: "影片",
		Path:  "/media/one.mkv",
	}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PlaybackHistory{
		Base:       model.Base{ID: "history-1"},
		UserID:     "user-1",
		MediaID:    media.ID,
		PositionMs: 30_000,
		DurationMs: 120_000,
		WatchedAt:  time.Now(),
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
	router.GET("/watch-history", historyListHandler(svc))

	type listRow struct {
		MediaID string       `json:"media_id"`
		Media   *model.Media `json:"media"`
	}

	full := httptest.NewRecorder()
	router.ServeHTTP(full, httptest.NewRequest(http.MethodGet, "/watch-history?limit=10", nil))
	var fullRows []listRow
	if err := json.Unmarshal(full.Body.Bytes(), &fullRows); err != nil {
		t.Fatal(err)
	}
	if len(fullRows) != 1 || fullRows[0].Media == nil || fullRows[0].Media.ID != media.ID {
		t.Fatalf("default response should embed media: %s", full.Body.String())
	}

	lean := httptest.NewRecorder()
	router.ServeHTTP(lean, httptest.NewRequest(http.MethodGet, "/watch-history?limit=10&include_media=0", nil))
	var leanRows []listRow
	if err := json.Unmarshal(lean.Body.Bytes(), &leanRows); err != nil {
		t.Fatal(err)
	}
	if len(leanRows) != 1 || leanRows[0].MediaID != media.ID {
		t.Fatalf("lean response should keep the row: %s", lean.Body.String())
	}
	if leanRows[0].Media != nil {
		t.Fatalf("include_media=0 must not embed media: %s", lean.Body.String())
	}
}
