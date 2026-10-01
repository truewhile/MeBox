package database

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestAutoMigrateDedupesPlaybackHistories reproduces the upgrade path: a legacy
// database contains duplicate (user_id, media_id) history rows created by the
// old read-then-write upsert. AutoMigrate must merge them before creating the
// uniq_user_history composite unique index, otherwise the upgrade fails.
func TestAutoMigrateDedupesPlaybackHistories(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// 旧 schema：无 uniq_user_history 唯一索引。
	if err := db.Exec(`CREATE TABLE playback_histories (
		id varchar(36) PRIMARY KEY,
		created_at datetime,
		updated_at datetime,
		deleted_at datetime,
		user_id varchar(36) NOT NULL,
		media_id varchar(128) NOT NULL,
		position_ms integer,
		duration_ms integer,
		watched_at datetime,
		completed numeric
	)`).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	rows := []struct {
		id        string
		position  int64
		watchedAt time.Time
	}{
		{"h-old", 1_000, base.Add(-2 * time.Hour)},
		{"h-mid", 2_000, base.Add(-1 * time.Hour)},
		{"h-new", 3_000, base},
	}
	for _, r := range rows {
		if err := db.Exec(
			`INSERT INTO playback_histories (id, user_id, media_id, position_ms, watched_at, created_at, updated_at)
			 VALUES (?, 'u-1', 'm-1', ?, ?, ?, ?)`,
			r.id, r.position, r.watchedAt, r.watchedAt, r.watchedAt,
		).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate with duplicate histories: %v", err)
	}

	var count int64
	if err := db.Table("playback_histories").Where("user_id = ? AND media_id = ?", "u-1", "m-1").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected duplicate rows merged to 1, got %d", count)
	}
	var position int64
	if err := db.Table("playback_histories").
		Where("user_id = ? AND media_id = ?", "u-1", "m-1").
		Select("position_ms").Scan(&position).Error; err != nil {
		t.Fatal(err)
	}
	if position != 3_000 {
		t.Fatalf("dedupe should keep the most recent row, got position_ms=%d", position)
	}

	// 唯一索引存在时，重复插入同一 (user_id, media_id) 应触发冲突而非新增行。
	if err := db.Exec(
		`INSERT INTO playback_histories (id, user_id, media_id, position_ms, watched_at, created_at, updated_at)
		 VALUES ('h-dup', 'u-1', 'm-1', 4_000, ?, ?, ?)`,
		base, base, base,
	).Error; err == nil {
		t.Fatal("insert violating uniq_user_history should fail")
	}
}
