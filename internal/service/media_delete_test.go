package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestDeleteMediaInvalidatesMediaAndStatsCache(t *testing.T) {
	db := newServiceTestDB(t, &model.Media{})
	repos := repository.New(db)
	media := model.Media{
		Base:  model.Base{ID: "local-media"},
		Title: "Cached Movie",
		Path:  filepath.Join(t.TempDir(), "Cached Movie.mkv"),
	}
	if err := repos.DB.Create(&media).Error; err != nil {
		t.Fatal(err)
	}

	cache := NewRuntimeCacheService(&config.Config{}, zap.NewNop())
	cache.SetJSON(t.Context(), "media:list:test", map[string]string{"state": "stale"}, time.Minute)
	cache.SetJSON(t.Context(), "stats:snapshot:base", map[string]int{"media": 1}, time.Minute)
	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos).SetRuntimeCache(cache)
	if err := svc.DeleteMedia(t.Context(), media.ID, false); err != nil {
		t.Fatal(err)
	}

	var mediaCache map[string]string
	if cache.GetJSON(t.Context(), "media:list:test", &mediaCache) {
		t.Fatal("delete should invalidate media cache")
	}
	var statsCache map[string]int
	if cache.GetJSON(t.Context(), "stats:snapshot:base", &statsCache) {
		t.Fatal("delete should invalidate stats cache")
	}
	var count int64
	if err := db.Unscoped().Model(&model.Media{}).Where("id = ?", media.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("media row still present, count=%d", count)
	}
}

func TestDeleteMediaRemovesLocalFilesWhenRequested(t *testing.T) {
	db := newServiceTestDB(t, &model.Media{})
	repos := repository.New(db)
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Movie.mkv")
	nfo := filepath.Join(dir, "Movie.nfo")
	if err := os.WriteFile(mediaPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nfo, []byte("nfo"), 0o644); err != nil {
		t.Fatal(err)
	}
	media := model.Media{
		Base:  model.Base{ID: "local-delete-files"},
		Title: "Movie",
		Path:  mediaPath,
	}
	if err := repos.DB.Create(&media).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos)
	if err := svc.DeleteMedia(t.Context(), media.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mediaPath); !os.IsNotExist(err) {
		t.Fatalf("expected media file removed, err=%v", err)
	}
	if _, err := os.Stat(nfo); !os.IsNotExist(err) {
		t.Fatalf("expected nfo removed, err=%v", err)
	}
}

func TestDeleteMediaKeepsLocalFilesByDefault(t *testing.T) {
	db := newServiceTestDB(t, &model.Media{})
	repos := repository.New(db)
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Keep.mkv")
	if err := os.WriteFile(mediaPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	media := model.Media{
		Base:  model.Base{ID: "keep-files"},
		Title: "Keep",
		Path:  mediaPath,
	}
	if err := repos.DB.Create(&media).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos)
	if err := svc.DeleteMedia(t.Context(), media.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mediaPath); err != nil {
		t.Fatalf("expected media file kept, err=%v", err)
	}
}

func TestDeleteMediaSkipsCloudPaths(t *testing.T) {
	db := newServiceTestDB(t, &model.Media{})
	repos := repository.New(db)
	media := model.Media{
		Base:  model.Base{ID: "cloud-media"},
		Title: "Cloud",
		Path:  "cloud://openlist/Movie.mkv",
	}
	if err := repos.DB.Create(&media).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos)
	if err := svc.DeleteMedia(t.Context(), media.ID, true); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Unscoped().Model(&model.Media{}).Where("id = ?", media.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("cloud media row still present, count=%d", count)
	}
}
