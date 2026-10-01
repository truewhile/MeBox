package service

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
)

func writeWalkFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func collectWalkPaths(t *testing.T, root string) []string {
	t.Helper()
	var seen []string
	if err := walk(root, func(path string, info walkInfo) error {
		if !info.isDir {
			seen = append(seen, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return seen
}

// A dot-prefixed walk root must not be treated as a hidden directory: doing so
// skipped the whole tree silently, so a library rooted at e.g. "/media/.staging"
// indexed nothing and the organizer reported "0 organized" with no error.
func TestWalkIncludesDotPrefixedRoot(t *testing.T) {
	parent := t.TempDir()
	dotRoot := filepath.Join(parent, ".dotroot")
	want := filepath.Join(dotRoot, "Movie (2021)", "Movie (2021).mkv")
	writeWalkFile(t, want)

	got := collectWalkPaths(t, dotRoot)
	if len(got) != 1 || filepath.Clean(got[0]) != filepath.Clean(want) {
		t.Fatalf("walk over dot-prefixed root = %#v, want the one file inside it", got)
	}
}

// Hidden directories *below* the root are still skipped.
func TestWalkStillSkipsHiddenChildDirectories(t *testing.T) {
	root := t.TempDir()
	visible := filepath.Join(root, "Movie (2021)", "Movie (2021).mkv")
	hidden := filepath.Join(root, ".trash", "Old Movie.mkv")
	deepHidden := filepath.Join(root, "Season 01", ".thumbnails", "thumb.mkv")
	writeWalkFile(t, visible)
	writeWalkFile(t, hidden)
	writeWalkFile(t, deepHidden)

	got := collectWalkPaths(t, root)
	if len(got) != 1 || filepath.Clean(got[0]) != filepath.Clean(visible) {
		t.Fatalf("walk = %#v, want only the visible file", got)
	}
}

// End-to-end: a library whose root directory starts with "." must be scanned.
func TestScanLibraryIndexesDotPrefixedRoot(t *testing.T) {
	sc, repos := newScannerTestEnv(t)
	root := filepath.Join(t.TempDir(), ".media")
	lib := model.Library{Name: "Movies", Path: root, Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	writeWalkFile(t, filepath.Join(root, "Movie (2021)", "Movie (2021).mkv"))

	res, err := sc.ScanLibrary(t.Context(), lib.ID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Added != 1 {
		t.Fatalf("scan added=%d (errors=%v), want 1 for a dot-prefixed library root", res.Added, res.Errors)
	}
	if got := countMedia(t, repos); got != 1 {
		t.Fatalf("media count = %d, want 1", got)
	}
}

// End-to-end: organizing from a dot-prefixed source directory must find files.
func TestOrganizeDirectoryReadsDotPrefixedSource(t *testing.T) {
	repos := newOrganizerTestRepo(t)
	cfg := &config.Config{}
	cfg.Organizer.SmartClassify = true
	organizer := NewOrganizerService(cfg, zap.NewNop(), repos)

	root := t.TempDir()
	src := filepath.Join(root, ".downloads")
	dest := filepath.Join(root, "media")
	writeWalkFile(t, filepath.Join(src, "Oppenheimer.2023.2160p.WEB-DL.H265", "oppenheimer-2160p.mkv"))

	res, err := organizer.OrganizeDirectory(t.Context(), OrganizeOptions{
		SourcePath:   src,
		DestPath:     dest,
		TransferMode: TransferCopy,
	})
	if err != nil {
		t.Fatalf("organize directory: %v", err)
	}
	if res.Organized != 1 {
		t.Fatalf("organized=%d skipped=%d errors=%v, want the file under a dot-prefixed source to be organized",
			res.Organized, res.Skipped, res.Errors)
	}
}
