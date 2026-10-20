package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

func TestScanRestoresUnchangedMissingFileAndMetadata(t *testing.T) {
	scanner, repos := newScannerTestEnv(t)
	root := t.TempDir()
	library := model.Library{Name: "Movies", Path: root, Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &library); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "Example (2024).mkv")
	writeTestFile(t, path, "unchanged content")
	if _, err := scanner.ScanLibrary(t.Context(), library.ID); err != nil {
		t.Fatal(err)
	}
	var original model.Media
	if err := repos.DB.Where("path = ?", path).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := repos.DB.Model(&original).Updates(map[string]any{
		"title": "Manually matched title", "scrape_status": "matched", "tm_db_id": 42,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if changed, err := scanner.RemovePath(t.Context(), path); err != nil || changed != 1 {
		t.Fatalf("mark missing: changed=%d err=%v", changed, err)
	}
	if changed, err := scanner.RemovePath(t.Context(), path); err != nil || changed != 0 {
		t.Fatalf("repeated missing event: changed=%d err=%v", changed, err)
	}
	writeTestFile(t, path, "unchanged content")
	if _, err := scanner.ScanLibrary(t.Context(), library.ID); err != nil {
		t.Fatal(err)
	}
	var restored model.Media
	if err := repos.DB.Where("path = ?", path).First(&restored).Error; err != nil {
		t.Fatal(err)
	}
	if restored.ID != original.ID || restored.TMDbID != 42 || restored.Title != "Manually matched title" {
		t.Fatalf("restored file lost identity or metadata: %#v", restored)
	}
	if restored.AvailabilityState != "available" || restored.MissingSince != nil || restored.LastSeenAt == nil {
		t.Fatalf("unchanged restored file stayed missing: %#v", restored)
	}
}
