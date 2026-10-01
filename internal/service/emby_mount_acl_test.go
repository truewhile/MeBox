package service

import (
	"encoding/json"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

func TestViewsHidesDisallowedMountedEmbyLibraries(t *testing.T) {
	svc := newTestEmbyService(t)
	if err := svc.repo.DB.AutoMigrate(&model.EmbyMount{}); err != nil {
		t.Fatal(err)
	}
	local := model.Library{Name: "Local", Path: "/media/local", Type: "movie", Enabled: true}
	if err := svc.repo.Library.Create(t.Context(), &local); err != nil {
		t.Fatal(err)
	}
	mount := &model.EmbyMount{
		AccountID:      "acct-1",
		RemoteViewID:   "view-1",
		RemoteViewName: "Remote Movies",
		Enabled:        true,
	}
	if err := svc.repo.EmbyMount.Create(t.Context(), mount); err != nil {
		t.Fatal(err)
	}
	remoteID := EncodeEmbyRemoteID(mount.ID, mount.RemoteViewID)

	user := &model.User{Username: "viewer", PasswordHash: "hash", Role: "user"}
	allowed, err := json.Marshal([]string{local.ID})
	if err != nil {
		t.Fatal(err)
	}
	user.AllowedLibraryIDs = string(allowed)
	if err := svc.repo.User.Create(t.Context(), user); err != nil {
		t.Fatal(err)
	}

	// Without a live remote service, remoteViews is empty; assert helper ACL instead
	// and that local Views still honor the allow-list.
	views, err := svc.Views(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("Views: %v", err)
	}
	items := views["Items"].([]map[string]any)
	for _, item := range items {
		if id, _ := item["Id"].(string); id == remoteID {
			t.Fatalf("disallowed remote library should not appear in Views: %#v", item)
		}
	}
	if !EmbyMountLibraryAllowed(MediaVisibility{AllowedLibraryIDs: []string{local.ID, remoteID}}, mount) {
		t.Fatal("expected remote library allowed when listed")
	}
	if EmbyMountLibraryAllowed(MediaVisibility{AllowedLibraryIDs: []string{local.ID}}, mount) {
		t.Fatal("expected remote library denied when not listed")
	}
}

func TestLibraryIDAllowed(t *testing.T) {
	if !LibraryIDAllowed(MediaVisibility{}, "any") {
		t.Fatal("empty allow-list should allow all")
	}
	if LibraryIDAllowed(MediaVisibility{AllowedLibraryIDs: []string{"a"}}, "b") {
		t.Fatal("missing id should be denied")
	}
	if !LibraryIDAllowed(MediaVisibility{AllowedLibraryIDs: []string{"a", "b"}}, "b") {
		t.Fatal("listed id should be allowed")
	}
}
