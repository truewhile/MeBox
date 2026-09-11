package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

func TestSTRMPathAliasesCoverBothKeepExtDirections(t *testing.T) {
	toKeepExt := strmPathAliases(filepath.Join(t.TempDir(), "Show.S01E01.strm"))
	if !containsPathAlias(toKeepExt, "Show.S01E01.mkv.strm") {
		t.Fatalf("missing keep_ext alias: %#v", toKeepExt)
	}
	fromKeepExt := strmPathAliases(filepath.Join(t.TempDir(), "Show.S01E01.mkv.strm"))
	if !containsPathAlias(fromKeepExt, "Show.S01E01.strm") {
		t.Fatalf("missing stripped alias: %#v", fromKeepExt)
	}
}

func TestMissingSTRMPathAliasesKeepsRealKeepExtSiblingVersions(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "Show.S01E01.mkv.strm")
	sibling := filepath.Join(dir, "Show.S01E01.mp4.strm")
	writeFileContent(t, current, "https://example.test/mkv")
	writeFileContent(t, sibling, "https://example.test/mp4")

	aliases := missingSTRMPathAliases(current)
	if containsPathAlias(aliases, sibling) {
		t.Fatalf("existing keep_ext sibling must remain a separate version: %#v", aliases)
	}
}

func TestScanLibraryMigratesSTRMKeepExtRename(t *testing.T) {
	cases := []struct {
		name    string
		oldName string
		newName string
	}{
		{name: "add video extension", oldName: "Show.S01E01.strm", newName: "Show.S01E01.mkv.strm"},
		{name: "remove video extension", oldName: "Show.S01E01.mkv.strm", newName: "Show.S01E01.strm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc, repos := newScannerTestEnv(t)
			root := t.TempDir()
			lib := model.Library{Name: "Anime", Path: root, Type: "anime", Enabled: true}
			if err := repos.Library.Create(t.Context(), &lib); err != nil {
				t.Fatal(err)
			}
			oldPath := filepath.Join(root, tc.oldName)
			newPath := filepath.Join(root, tc.newName)
			writeFileContent(t, oldPath, "https://example.test/video")
			if _, err := sc.IngestPath(t.Context(), lib.ID, oldPath); err != nil {
				t.Fatal(err)
			}

			var before model.Media
			if err := repos.DB.First(&before, "path = ?", oldPath).Error; err != nil {
				t.Fatal(err)
			}
			if err := repos.DB.Model(&model.Media{}).Where("id = ?", before.ID).Updates(map[string]any{
				"title":         "地狱乐",
				"original_name": "Jigokuraku",
				"overview":      "preserved overview",
				"poster_url":    "Jigokuraku-poster.jpg",
				"backdrop_url":  "Jigokuraku-backdrop.jpg",
				"year":          2023,
				"rating":        8.6,
				"tm_db_id":      12345,
				"bangumi_id":    67890,
				"genres":        "Action,Adventure",
				"scrape_status": "matched",
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(oldPath, newPath); err != nil {
				t.Fatal(err)
			}

			res, err := sc.ScanLibrary(t.Context(), lib.ID)
			if err != nil {
				t.Fatal(err)
			}
			if res.ErrorCount != 0 {
				t.Fatalf("scan errors: %#v", res.Errors)
			}
			if got := countMedia(t, repos); got != 1 {
				t.Fatalf("media count = %d, want 1", got)
			}

			var after model.Media
			if err := repos.DB.First(&after, "id = ?", before.ID).Error; err != nil {
				t.Fatalf("old media row was not preserved: %v", err)
			}
			if after.Path != newPath {
				t.Fatalf("path = %q, want %q", after.Path, newPath)
			}
			if after.Title != "地狱乐" || after.OriginalName != "Jigokuraku" || after.Overview != "preserved overview" {
				t.Fatalf("scraped identity metadata was lost: %#v", after)
			}
			if after.PosterURL != "Jigokuraku-poster.jpg" || after.BackdropURL != "Jigokuraku-backdrop.jpg" {
				t.Fatalf("artwork was lost: poster=%q backdrop=%q", after.PosterURL, after.BackdropURL)
			}
			if after.TMDbID != 12345 || after.BangumiID != 67890 || after.ScrapeStatus != "matched" {
				t.Fatalf("scrape IDs/status changed: tmdb=%d bgm=%d status=%q", after.TMDbID, after.BangumiID, after.ScrapeStatus)
			}
		})
	}
}

func TestIngestPathAdoptsSoftDeletedSTRMRenameTombstone(t *testing.T) {
	sc, repos := newScannerTestEnv(t)
	root := t.TempDir()
	lib := model.Library{Name: "Anime", Path: root, Type: "anime", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(root, "Show.S01E01.mkv.strm")
	newPath := filepath.Join(root, "Show.S01E01.strm")
	writeFileContent(t, oldPath, "https://example.test/video")
	if _, err := sc.IngestPath(t.Context(), lib.ID, oldPath); err != nil {
		t.Fatal(err)
	}

	var before model.Media
	if err := repos.DB.First(&before, "path = ?", oldPath).Error; err != nil {
		t.Fatal(err)
	}
	if err := repos.DB.Model(&model.Media{}).Where("id = ?", before.ID).Updates(map[string]any{
		"title":         "拔作岛",
		"overview":      "kept through remove-before-create",
		"poster_url":    "nukitashi-poster.webp",
		"tm_db_id":      222,
		"scrape_status": "matched",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}

	// Simulate fsnotify delivering Remove(old) before Create(new).
	if removed, err := sc.RemovePath(t.Context(), oldPath); err != nil || removed != 1 {
		t.Fatalf("RemovePath() removed=%d err=%v, want 1", removed, err)
	}
	var tombstone model.Media
	if err := repos.DB.Unscoped().First(&tombstone, "id = ?", before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !tombstone.DeletedAt.Valid {
		t.Fatal("rename tombstone should be soft-deleted")
	}
	if got := countMedia(t, repos); got != 0 {
		t.Fatalf("active media count = %d, want 0 before create event", got)
	}

	if _, err := sc.IngestPath(t.Context(), lib.ID, newPath); err != nil {
		t.Fatal(err)
	}
	var after model.Media
	if err := repos.DB.First(&after, "id = ?", before.ID).Error; err != nil {
		t.Fatalf("soft-deleted row was not restored: %v", err)
	}
	if after.Path != newPath || after.Title != "拔作岛" || after.Overview != "kept through remove-before-create" {
		t.Fatalf("rename metadata was not adopted: %#v", after)
	}
	if after.TMDbID != 222 || after.ScrapeStatus != "matched" {
		t.Fatalf("scrape metadata changed: tmdb=%d status=%q", after.TMDbID, after.ScrapeStatus)
	}
	if got := countMedia(t, repos); got != 1 {
		t.Fatalf("media count = %d, want 1", got)
	}
}

func TestRemovePathStillHardDeletesTrueDeletionWithoutAlias(t *testing.T) {
	sc, repos := newScannerTestEnv(t)
	root := t.TempDir()
	lib := model.Library{Name: "Anime", Path: root, Type: "anime", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "Deleted.S01E01.mkv.strm")
	writeFileContent(t, path, "https://example.test/video")
	if _, err := sc.IngestPath(t.Context(), lib.ID, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if removed, err := sc.RemovePath(t.Context(), path); err != nil || removed != 1 {
		t.Fatalf("RemovePath() removed=%d err=%v, want 1", removed, err)
	}
	var count int64
	if err := repos.DB.Unscoped().Model(&model.Media{}).Where("path = ?", path).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("true deletion left %d tombstone row(s)", count)
	}
}

func containsPathAlias(paths []string, name string) bool {
	for _, path := range paths {
		if strings.EqualFold(filepath.Base(path), filepath.Base(name)) {
			return true
		}
	}
	return false
}
