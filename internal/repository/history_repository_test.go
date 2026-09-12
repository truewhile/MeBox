package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/database"
	"github.com/truewhile/MeBox/internal/model"
)

func TestHistoryUpsertSingleRowPerUserMedia(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := New(db)
	ctx := t.Context()
	watched := time.Now()

	first := &model.PlaybackHistory{UserID: "u-1", MediaID: "m-1", PositionMs: 30_000, DurationMs: 0, WatchedAt: watched, Completed: false}
	if err := repos.History.Upsert(ctx, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	second := &model.PlaybackHistory{UserID: "u-1", MediaID: "m-1", PositionMs: 90_000, DurationMs: 120_000, WatchedAt: watched.Add(time.Minute), Completed: true}
	if err := repos.History.Upsert(ctx, second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var count int64
	if err := db.Model(&model.PlaybackHistory{}).Where("user_id = ? AND media_id = ?", "u-1", "m-1").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 history row after upserts, got %d", count)
	}
	var got model.PlaybackHistory
	if err := db.Where("user_id = ? AND media_id = ?", "u-1", "m-1").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.PositionMs != 90_000 || !got.Completed {
		t.Fatalf("position/completion not updated: %#v", got)
	}
	if got.DurationMs != 120_000 {
		t.Fatalf("duration should update when known, got %d", got.DurationMs)
	}
}

func TestHistoryUpsertKeepsDurationWhenUnknown(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := New(db)
	ctx := t.Context()
	watched := time.Now()

	if err := repos.History.Upsert(ctx, &model.PlaybackHistory{UserID: "u-1", MediaID: "m-2", PositionMs: 10, DurationMs: 600_000, WatchedAt: watched}); err != nil {
		t.Fatal(err)
	}
	if err := repos.History.Upsert(ctx, &model.PlaybackHistory{UserID: "u-1", MediaID: "m-2", PositionMs: 20, DurationMs: 0, WatchedAt: watched.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	var got model.PlaybackHistory
	if err := db.Where("user_id = ? AND media_id = ?", "u-1", "m-2").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.DurationMs != 600_000 {
		t.Fatalf("duration_ms=0 upsert must not clear stored duration, got %d", got.DurationMs)
	}
}

func TestHistoryUpsertProgressRejectsStaleReports(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := New(db)
	ctx := t.Context()
	watched := time.Now()

	first := &model.PlaybackHistory{
		UserID:             "u-1",
		MediaID:            "m-1",
		PositionMs:         30_000,
		DurationMs:         120_000,
		WatchedAt:          watched,
		SessionID:          "session-1",
		SessionStartedAtMs: 1_000,
		Sequence:           1,
	}
	if err := repos.History.UpsertProgress(ctx, first); err != nil {
		t.Fatalf("first progress: %v", err)
	}
	newer := &model.PlaybackHistory{
		UserID:             "u-1",
		MediaID:            "m-1",
		PositionMs:         90_000,
		DurationMs:         120_000,
		WatchedAt:          watched.Add(time.Minute),
		Completed:          true,
		SessionID:          "session-1",
		SessionStartedAtMs: 1_000,
		Sequence:           2,
	}
	if err := repos.History.UpsertProgress(ctx, newer); err != nil {
		t.Fatalf("newer progress: %v", err)
	}

	stale := &model.PlaybackHistory{
		UserID:             "u-1",
		MediaID:            "m-1",
		PositionMs:         10_000,
		DurationMs:         120_000,
		WatchedAt:          watched.Add(2 * time.Minute),
		SessionID:          "session-1",
		SessionStartedAtMs: 1_000,
		Sequence:           1,
	}
	if err := repos.History.UpsertProgress(ctx, stale); err != nil {
		t.Fatalf("stale progress: %v", err)
	}
	var got model.PlaybackHistory
	if err := db.Where("user_id = ? AND media_id = ?", "u-1", "m-1").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.PositionMs != 90_000 || !got.Completed || got.Sequence != 2 {
		t.Fatalf("stale report overwrote newer state: %#v", got)
	}

	oldSession := &model.PlaybackHistory{
		UserID:             "u-1",
		MediaID:            "m-1",
		PositionMs:         5_000,
		DurationMs:         120_000,
		WatchedAt:          watched.Add(3 * time.Minute),
		SessionID:          "session-0",
		SessionStartedAtMs: 500,
		Sequence:           99,
	}
	if err := repos.History.UpsertProgress(ctx, oldSession); err != nil {
		t.Fatalf("old session progress: %v", err)
	}
	if err := db.Where("user_id = ? AND media_id = ?", "u-1", "m-1").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.PositionMs != 90_000 || !got.Completed {
		t.Fatalf("old session overwrote newer state: %#v", got)
	}

	restart := &model.PlaybackHistory{
		UserID:             "u-1",
		MediaID:            "m-1",
		PositionMs:         1_000,
		DurationMs:         120_000,
		WatchedAt:          watched.Add(4 * time.Minute),
		SessionID:          "session-2",
		SessionStartedAtMs: 2_000,
		Sequence:           1,
	}
	if err := repos.History.UpsertProgress(ctx, restart); err != nil {
		t.Fatalf("restart progress: %v", err)
	}
	if err := db.Where("user_id = ? AND media_id = ?", "u-1", "m-1").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.PositionMs != 1_000 || got.Completed || got.SessionID != "session-2" {
		t.Fatalf("new playback session did not reset state: %#v", got)
	}
}
