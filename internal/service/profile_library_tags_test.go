package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"go.uber.org/zap"
)

func TestProfileLibraryTagsFiltersInaccessibleAndKeepsOneTagPerLibrary(t *testing.T) {
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

	got, err := svc.SetLibraryTags(t.Context(), user.ID, []model.LibraryTagSet{
		{Name: " 热门 ", LibraryIDs: []string{libA.ID, libHidden.ID, "missing", libA.ID}},
		{Name: "热门", LibraryIDs: []string{libB.ID, libA.ID}},
		{Name: "", LibraryIDs: []string{libB.ID}},
	})
	if err != nil {
		t.Fatalf("SetLibraryTags: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("SetLibraryTags returned %d tags, want 1 (%#v)", len(got), got)
	}
	if got[0].Name != "热门" {
		t.Fatalf("tag name = %q, want 热门", got[0].Name)
	}
	// libA 已在第一个标签里占位，第二个标签里的 libA 应被丢弃。
	want := []string{libA.ID, libB.ID}
	if len(got[0].LibraryIDs) != len(want) {
		t.Fatalf("tag ids = %v, want %v", got[0].LibraryIDs, want)
	}
	for i := range want {
		if got[0].LibraryIDs[i] != want[i] {
			t.Fatalf("tag ids = %v, want %v", got[0].LibraryIDs, want)
		}
	}

	loaded, err := svc.GetLibraryTags(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("GetLibraryTags: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Name != "热门" || len(loaded[0].LibraryIDs) != len(want) {
		t.Fatalf("GetLibraryTags = %#v, want one tag with %v", loaded, want)
	}
}

func TestProfileLibraryTagsKeepsEmptyTagAndMountedEmby(t *testing.T) {
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

	got, err := svc.SetLibraryTags(t.Context(), user.ID, []model.LibraryTagSet{
		{Name: "远程", LibraryIDs: []string{remoteID, "embyremote~missing~view"}},
		{Name: "以后再用", LibraryIDs: []string{}},
	})
	if err != nil {
		t.Fatalf("SetLibraryTags: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("SetLibraryTags = %#v, want 2 tags", got)
	}
	if len(got[0].LibraryIDs) != 1 || got[0].LibraryIDs[0] != remoteID {
		t.Fatalf("remote tag ids = %v, want [%s]", got[0].LibraryIDs, remoteID)
	}
	if got[1].Name != "以后再用" || len(got[1].LibraryIDs) != 0 {
		t.Fatalf("empty tag should survive as-is, got %#v", got[1])
	}

	loaded, err := svc.GetLibraryTags(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("GetLibraryTags: %v", err)
	}
	if len(loaded) != 2 || len(loaded[1].LibraryIDs) != 0 {
		t.Fatalf("GetLibraryTags = %#v, want the empty tag preserved", loaded)
	}
}

func TestProfileLibraryTagsEmptyStateReturnsEmptySlice(t *testing.T) {
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
	if tags, err := svc.GetLibraryTags(t.Context(), user.ID); err != nil || tags == nil || len(tags) != 0 {
		t.Fatalf("GetLibraryTags = %#v, %v; want empty non-nil slice", tags, err)
	}
	if tags, err := svc.SetLibraryTags(t.Context(), user.ID, nil); err != nil || tags == nil || len(tags) != 0 {
		t.Fatalf("SetLibraryTags(nil) = %#v, %v; want empty non-nil slice", tags, err)
	}
}
