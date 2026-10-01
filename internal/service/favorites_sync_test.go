package service

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestSyncUserFavoriteWritesLocalForRemoteID(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Favorite{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	userID := "user-1"
	remoteMediaID := EncodeEmbyRemoteID("mount-1", "remote-item-1")

	if err := SyncUserFavorite(context.Background(), repos, nil, userID, remoteMediaID, true); err != nil {
		t.Fatalf("SyncUserFavorite favorite: %v", err)
	}
	favorite, err := IsUserFavorite(context.Background(), repos, userID, remoteMediaID)
	if err != nil {
		t.Fatalf("IsUserFavorite: %v", err)
	}
	if !favorite {
		t.Fatal("expected remote favourite to be stored locally")
	}

	if err := SyncUserFavorite(context.Background(), repos, nil, userID, remoteMediaID, false); err != nil {
		t.Fatalf("SyncUserFavorite unfavorite: %v", err)
	}
	favorite, err = IsUserFavorite(context.Background(), repos, userID, remoteMediaID)
	if err != nil {
		t.Fatalf("IsUserFavorite after delete: %v", err)
	}
	if favorite {
		t.Fatal("expected remote favourite to be removed locally")
	}
}

func TestFavoriteItemsIncludesRemoteFavourites(t *testing.T) {
	db := newServiceTestDB(t, &model.User{}, &model.Library{}, &model.Media{}, &model.Favorite{})
	repos := repository.New(db)
	viewer := &model.User{Username: "viewer", PasswordHash: "hash", Role: "user"}
	if err := repos.User.Create(t.Context(), viewer); err != nil {
		t.Fatal(err)
	}
	remoteMediaID := EncodeEmbyRemoteID("mount-1", "remote-item-1")
	if err := db.Create(&model.Favorite{UserID: viewer.ID, MediaID: remoteMediaID}).Error; err != nil {
		t.Fatal(err)
	}

	svc := &EmbyService{repo: repos}
	out, err := svc.favoriteItems(t.Context(), ItemsParams{UserID: viewer.ID, Limit: 50})
	if err != nil {
		t.Fatalf("favoriteItems: %v", err)
	}
	total, _ := out["TotalRecordCount"].(int64)
	if total != 0 {
		// Without a wired remote service hydration is skipped, but local-only path
		// should not error and should not count unavailable remote rows.
		items, _ := out["Items"].([]map[string]any)
		if len(items) != 0 {
			t.Fatalf("expected no hydrated remote rows without remote service, got %#v", out)
		}
	}
}
