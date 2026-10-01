package service

import (
	"encoding/json"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

func TestViewsOrdersPinnedLibrariesFirst(t *testing.T) {
	svc := newTestEmbyService(t)
	first := model.Library{Name: "AAA", Path: "/media/a", Type: "movie", Enabled: true, SortOrder: 0}
	second := model.Library{Name: "BBB", Path: "/media/b", Type: "movie", Enabled: true, SortOrder: 1}
	third := model.Library{Name: "CCC", Path: "/media/c", Type: "movie", Enabled: true, SortOrder: 2}
	for _, lib := range []*model.Library{&first, &second, &third} {
		if err := svc.repo.Library.Create(t.Context(), lib); err != nil {
			t.Fatalf("create library: %v", err)
		}
	}

	user := &model.User{Username: "viewer", PasswordHash: "hash", Role: "user"}
	pinned, err := json.Marshal([]string{third.ID, first.ID})
	if err != nil {
		t.Fatal(err)
	}
	user.PinnedLibraryIDs = string(pinned)
	if err := svc.repo.User.Create(t.Context(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	views, err := svc.Views(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("Views: %v", err)
	}
	items := views["Items"].([]map[string]any)
	if len(items) != 3 {
		t.Fatalf("expected 3 views, got %d", len(items))
	}
	got := []string{items[0]["Id"].(string), items[1]["Id"].(string), items[2]["Id"].(string)}
	want := []string{third.ID, first.ID, second.ID}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Views order = %v, want %v", got, want)
		}
	}
}

func TestSortViewItemsByPinnedIDsKeepsUnpinnedOrder(t *testing.T) {
	items := []map[string]any{
		{"Id": "a", "Name": "A"},
		{"Id": "b", "Name": "B"},
		{"Id": "c", "Name": "C"},
		{"Id": "d", "Name": "D"},
	}
	sorted := sortViewItemsByPinnedIDs(items, []string{"c", "a"})
	got := make([]string, len(sorted))
	for i, item := range sorted {
		got[i] = item["Id"].(string)
	}
	want := []string{"c", "a", "b", "d"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}
