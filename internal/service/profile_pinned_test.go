package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"go.uber.org/zap"
)

func TestProfilePinnedLibrariesFiltersInaccessibleAndPreservesOrder(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Library{}, &model.EmbyMount{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	svc := NewProfileService(zap.NewNop(), repos)

	user := &model.User{Username: "viewer", PasswordHash: "hash", Role: "user"}
	if err := repos.User.Create(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	libA := &model.Library{Name: "Movies", Path: "/media/movies", Type: "movie", Enabled: true}
	libB := &model.Library{Name: "TV", Path: "/media/tv", Type: "tv", Enabled: true}
	libHidden := &model.Library{Name: "Adult", Path: "/media/adult", Type: "movie", Enabled: true}
	for _, lib := range []*model.Library{libA, libB, libHidden} {
		if err := repos.Library.Create(t.Context(), lib); err != nil {
			t.Fatal(err)
		}
	}
	if err := repos.User.UpdateFields(t.Context(), user.ID, map[string]any{
		"allowed_library_ids": `["` + libA.ID + `","` + libB.ID + `"]`,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.SetPinnedLibraryIDs(t.Context(), user.ID, []string{
		libB.ID, libHidden.ID, libA.ID, libB.ID, "missing",
	})
	if err != nil {
		t.Fatalf("SetPinnedLibraryIDs: %v", err)
	}
	want := []string{libB.ID, libA.ID}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("SetPinnedLibraryIDs = %v, want %v", got, want)
	}

	loaded, err := svc.GetPinnedLibraryIDs(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("GetPinnedLibraryIDs: %v", err)
	}
	if len(loaded) != len(want) || loaded[0] != want[0] || loaded[1] != want[1] {
		t.Fatalf("GetPinnedLibraryIDs = %v, want %v", loaded, want)
	}
}

func TestProfilePinnedLibrariesKeepsMountedEmbyAndLocalPins(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Library{}, &model.EmbyMount{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	svc := NewProfileService(zap.NewNop(), repos)

	user := &model.User{Username: "viewer", PasswordHash: "hash", Role: "user"}
	if err := repos.User.Create(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	local := &model.Library{Name: "Movies", Path: "/media/movies", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), local); err != nil {
		t.Fatal(err)
	}
	mount := &model.EmbyMount{
		AccountID:      "acct-1",
		RemoteViewID:   "view-42",
		RemoteViewName: "Remote Movies",
		Enabled:        true,
	}
	if err := repos.EmbyMount.Create(t.Context(), mount); err != nil {
		t.Fatal(err)
	}
	remoteID := EncodeEmbyRemoteID(mount.ID, mount.RemoteViewID)
	disabled := &model.EmbyMount{
		AccountID:    "acct-1",
		RemoteViewID: "view-99",
		Enabled:      true,
	}
	if err := repos.EmbyMount.Create(t.Context(), disabled); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(disabled).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	disabledID := EncodeEmbyRemoteID(disabled.ID, disabled.RemoteViewID)

	got, err := svc.SetPinnedLibraryIDs(t.Context(), user.ID, []string{
		local.ID, remoteID, disabledID, "embyremote~missing~view",
	})
	if err != nil {
		t.Fatalf("SetPinnedLibraryIDs: %v", err)
	}
	want := []string{local.ID, remoteID}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("SetPinnedLibraryIDs = %v, want %v", got, want)
	}

	loaded, err := svc.GetPinnedLibraryIDs(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("GetPinnedLibraryIDs: %v", err)
	}
	if len(loaded) != len(want) || loaded[0] != want[0] || loaded[1] != want[1] {
		t.Fatalf("GetPinnedLibraryIDs = %v, want %v", loaded, want)
	}
}
