package service

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// Dot-prefixed directory names are legal and common (hidden media folders,
// ".staging" drop dirs). cleanEntryName used to trim dots from BOTH ends, which
// silently rewrote `.media` into `media`.
func TestCleanEntryNameKeepsLeadingDots(t *testing.T) {
	cases := []struct {
		name  string
		isDir bool
		want  string
	}{
		{name: ".media", isDir: true, want: ".media"},
		{name: ".tmp-live-media", isDir: true, want: ".tmp-live-media"},
		{name: ".staging", isDir: true, want: ".staging"},
		{name: "Season 01.", isDir: true, want: "Season 01"},
		{name: "  trailing space  ", isDir: true, want: "trailing space"},
		{name: ".hidden.mkv", isDir: false, want: ".hidden.mkv"},
		{name: "poster.jpg", isDir: false, want: "poster.jpg"},
		{name: "file:name?test*<foo>|bar\".mkv", isDir: false, want: "file nametestfoobar.mkv"},
		// Names that are nothing but dots must still collapse to a safe placeholder.
		{name: ".", isDir: true, want: "unnamed"},
		{name: "..", isDir: true, want: "unnamed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanEntryName(tc.name, tc.isDir); got != tc.want {
				t.Fatalf("cleanEntryName(%q, %v) = %q, want %q", tc.name, tc.isDir, got, tc.want)
			}
		})
	}
}

// sanitizeLocalPath must preserve a dot-prefixed directory in the middle of an
// absolute path. Regression: artwork sidecars for a library at
// `<root>/.tmp-live-media/电影/...` were written to `<root>/tmp-live-media/...`
// instead — a sibling tree outside the media folder.
func TestSanitizeLocalPathKeepsDotPrefixedSegments(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"media", ".hidden-lib", "电影", "Inception (2010)")
	got := sanitizeLocalPath(dir)
	if got != dir {
		t.Fatalf("sanitizeLocalPath(%q) = %q, want the path unchanged", dir, got)
	}
}

// End-to-end guard for the observed bug: writing scraped artwork into a media
// folder located under a dot-prefixed library root must land inside that folder,
// never in a dot-stripped sibling directory.
func TestArtworkWriteKeepsDotPrefixedLibraryPath(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, ".media")
	mediaDir := filepath.Join(libraryRoot, "电影", "Inception (2010)")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatal(err)
	}

	svc := &ScraperService{log: zap.NewNop()}
	dst := svc.writeArtworkDataToPath(mediaDir, "Inception (2010)-poster", "image/jpeg", []byte{0xff, 0xd8, 0xff, 0xdb, 0x00})
	if dst == "" {
		t.Fatal("artwork write reported no destination")
	}
	if filepath.Dir(filepath.Clean(dst)) != filepath.Clean(mediaDir) {
		t.Fatalf("artwork written to %q, want a file inside %q", dst, mediaDir)
	}
	if _, err := os.Stat(filepath.Join(mediaDir, "Inception (2010)-poster.jpg")); err != nil {
		t.Fatalf("sidecar missing from the media folder: %v", err)
	}
	// A dot-stripped sibling must not be created.
	if _, err := os.Stat(filepath.Join(root, "media")); err == nil {
		t.Fatal("dot-stripped sibling directory was created")
	}
}
