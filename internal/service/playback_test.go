package service

import (
	"context"
	"testing"
	"time"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"go.uber.org/zap"
)

func TestRecordProgressPreservesDurationWhenMissing(t *testing.T) {
	db := newServiceTestDB(t, &model.PlaybackHistory{}, &model.Media{})
	repos := repository.New(db)
	userID := "user-1"
	mediaID := "local-movie-1"
	if err := db.Create(&model.PlaybackHistory{
		Base:       model.Base{ID: "hist-1"},
		UserID:     userID,
		MediaID:    mediaID,
		PositionMs: 30_000,
		DurationMs: 120_000,
		WatchedAt:  time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed history: %v", err)
	}

	svc := NewPlaybackService(zap.NewNop(), repos)
	if err := svc.RecordProgress(context.Background(), userID, mediaID, 60_000, 0); err != nil {
		t.Fatalf("RecordProgress: %v", err)
	}

	var hist model.PlaybackHistory
	if err := db.Where("user_id = ? AND media_id = ?", userID, mediaID).First(&hist).Error; err != nil {
		t.Fatalf("find history: %v", err)
	}
	if hist.DurationMs != 120_000 {
		t.Fatalf("expected duration preserved, got %d", hist.DurationMs)
	}
	if hist.PositionMs != 60_000 {
		t.Fatalf("expected position updated, got %d", hist.PositionMs)
	}
}

func TestGetProgressReturnsNilWhenMissing(t *testing.T) {
	db := newServiceTestDB(t, &model.PlaybackHistory{})
	repos := repository.New(db)
	svc := NewPlaybackService(zap.NewNop(), repos)

	row, err := svc.GetProgress(context.Background(), "user-1", "missing-media")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if row != nil {
		t.Fatalf("expected nil progress, got %#v", row)
	}
}

func TestListFavouritesIncludesLocalAndRemoteIDs(t *testing.T) {
	db := newServiceTestDB(t, &model.Favorite{}, &model.Media{})
	repos := repository.New(db)
	userID := "user-1"
	local := model.Media{
		Base:  model.Base{ID: "local-movie-1"},
		Title: "Local Movie",
	}
	if err := db.Create(&local).Error; err != nil {
		t.Fatalf("create local media: %v", err)
	}
	remoteID := EncodeEmbyRemoteID("mount-1", "remote-series-1")
	favs := []model.Favorite{
		{Base: model.Base{CreatedAt: time.Now().Add(-time.Minute)}, UserID: userID, MediaID: remoteID},
		{Base: model.Base{CreatedAt: time.Now()}, UserID: userID, MediaID: local.ID},
	}
	for i := range favs {
		if err := db.Create(&favs[i]).Error; err != nil {
			t.Fatalf("create favorite %d: %v", i, err)
		}
	}

	svc := NewPlaybackService(zap.NewNop(), repos)
	items, err := svc.ListFavourites(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListFavourites: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one hydrated local favourite without remote service, got %d: %#v", len(items), items)
	}
	if items[0].ID != local.ID {
		t.Fatalf("expected local favourite first by created_at desc, got %#v", items[0])
	}
}

func TestPlaybackProgressCompletedThreshold(t *testing.T) {
	tests := []struct {
		name     string
		position int64
		duration int64
		want     bool
	}{
		{name: "zero duration", position: 10_000, duration: 0, want: false},
		{name: "short clip early", position: 10_000, duration: 20_000, want: false},
		{name: "short clip near end", position: 18_000, duration: 20_000, want: true},
		{name: "movie before final window", position: 80_000, duration: 120_000, want: false},
		{name: "movie in final window", position: 95_000, duration: 120_000, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := playbackProgressCompleted(tt.position, tt.duration); got != tt.want {
				t.Fatalf("playbackProgressCompleted(%d, %d) = %t, want %t", tt.position, tt.duration, got, tt.want)
			}
		})
	}
}

// TestRecentHistoryWithoutMediaSkipsHydration 覆盖详情页用的轻量历史路径：
// 不返回 media，但可见性过滤必须与带 media 的版本一致。
func TestRecentHistoryWithoutMediaSkipsHydration(t *testing.T) {
	db := newServiceTestDB(t, &model.PlaybackHistory{}, &model.Media{}, &model.Library{})
	repos := repository.New(db)
	svc := NewPlaybackService(zap.NewNop(), repos)

	seed := func(id, libraryID string, nsfw bool) {
		t.Helper()
		m := model.Media{
			Base:      model.Base{ID: id},
			LibraryID: libraryID,
			Title:     "标题 " + id,
			Path:      "/media/" + id + ".mkv",
			NSFW:      nsfw,
		}
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("seed media %s: %v", id, err)
		}
	}
	seed("visible", "lib-ok", false)
	seed("adult", "lib-ok", true)
	seed("hidden-lib", "lib-hidden", false)

	now := time.Now()
	for i, mediaID := range []string{"visible", "adult", "hidden-lib", "gone"} {
		if err := db.Create(&model.PlaybackHistory{
			Base:       model.Base{ID: "hist-" + mediaID},
			UserID:     "user-1",
			MediaID:    mediaID,
			PositionMs: 10_000,
			DurationMs: 60_000,
			WatchedAt:  now.Add(-time.Duration(i) * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed history %s: %v", mediaID, err)
		}
	}

	items, err := svc.RecentHistoryWithoutMedia(context.Background(), "user-1", 50, MediaVisibility{
		HiddenLibraryIDs: []string{"lib-hidden"},
	})
	if err != nil {
		t.Fatalf("RecentHistoryWithoutMedia: %v", err)
	}
	got := make([]string, 0, len(items))
	for _, item := range items {
		if item.Media != nil {
			t.Fatalf("expected no media hydration, got %#v", item.Media)
		}
		got = append(got, item.MediaID)
	}
	want := []string{"visible", "gone"}
	if len(got) != len(want) {
		t.Fatalf("media ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("media ids = %v, want %v", got, want)
		}
	}
}
