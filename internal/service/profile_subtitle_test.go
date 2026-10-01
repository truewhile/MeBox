package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestProfileSubtitleChineseModePersistsAndValidates(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}

	repos := repository.New(db)
	svc := NewProfileService(zap.NewNop(), repos)
	user := &model.User{
		Username:     "subtitle-viewer",
		PasswordHash: "hash",
		Role:         "user",
		IsActive:     true,
	}
	if err := repos.User.Create(t.Context(), user); err != nil {
		t.Fatal(err)
	}

	traditional := "traditional"
	updated, err := svc.UpdateProfile(t.Context(), user.ID, ProfileUpdate{
		SubtitleChineseMode: &traditional,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.SubtitleChineseMode != traditional {
		t.Fatalf("subtitle mode = %q, want %q", updated.SubtitleChineseMode, traditional)
	}

	invalid := "automatic"
	if _, err := svc.UpdateProfile(t.Context(), user.ID, ProfileUpdate{
		SubtitleChineseMode: &invalid,
	}); err == nil {
		t.Fatal("invalid subtitle mode should be rejected")
	}

	reloaded, err := repos.User.FindByID(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.SubtitleChineseMode != traditional {
		t.Fatalf("invalid update changed subtitle mode to %q", reloaded.SubtitleChineseMode)
	}
}
