package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestMediaSidecarBaseIgnoresKeepExt(t *testing.T) {
	cases := map[string]string{
		"/media/竞女01.mkv.strm":         "竞女01",
		"/media/竞女01.mp4.strm":         "竞女01",
		"/media/竞女01.strm":             "竞女01",
		"/media/竞女01.mkv":              "竞女01",
		"/lib/Show.S01E02.mkv.strm":    "Show.S01E02",
		`C:/lib/Show.S01E02.mkv.strm`:  "Show.S01E02",
	}
	for in, want := range cases {
		if got := mediaSidecarBase(in); got != want {
			t.Fatalf("mediaSidecarBase(%q)=%q want %q", in, got, want)
		}
	}
}

func TestNFOPathUsesSharedStemForKeepExt(t *testing.T) {
	got := nfoPath("/strm/竞女01.mkv.strm")
	want := filepath.Join("/strm", "竞女01.nfo")
	if got != want {
		t.Fatalf("nfoPath keep_ext = %q want %q", got, want)
	}
	if nfoPath("/strm/竞女01.mp4.strm") != want {
		t.Fatalf("mkv/mp4 versions should share nfo path")
	}
}

func TestFindMovieNFOMatchesSharedStemBesideKeepExtStrm(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "竞女01.mkv.strm")
	nfo := filepath.Join(dir, "竞女01.nfo")
	writeFileContent(t, media, "http://example/play")
	writeFileContent(t, nfo, `<movie><title>竞女01</title></movie>`)

	doc, path, err := findMovieNFO(media, dir)
	if err != nil {
		t.Fatal(err)
	}
	if path != nfo {
		t.Fatalf("path=%q want %q", path, nfo)
	}
	if doc == nil || strings.TrimSpace(doc.Title) != "竞女01" {
		t.Fatalf("unexpected nfo doc: %#v", doc)
	}
}

func TestLocalPosterCandidatesIncludeSharedStem(t *testing.T) {
	cands := localPosterCandidates("/strm/竞女01.mkv.strm")
	found := false
	for _, name := range cands {
		if name == "竞女01-poster" || name == "竞女01" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected shared stem poster candidates, got %#v", cands)
	}
}

func TestSubtitleDiscoveryMatchesSharedStemBesideKeepExt(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "竞女01.mkv.strm")
	subPath := filepath.Join(dir, "竞女01.zh.srt")
	writeFileContent(t, mediaPath, "http://example/play")
	writeFileContent(t, subPath, "1\n00:00:01,000 --> 00:00:02,000\nhi\n")

	db := newServiceTestDB(t, &model.Media{})
	repos := repository.New(db)
	media := model.Media{Title: "竞女01", Path: mediaPath}
	if err := repos.Media.Upsert(t.Context(), &media); err != nil {
		t.Fatal(err)
	}
	svc := &SubtitleService{repo: repos}
	tracks, err := svc.discoverUncached(t.Context(), media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 {
		t.Fatalf("tracks=%d want 1 (%#v)", len(tracks), tracks)
	}
	if tracks[0].Path != subPath {
		t.Fatalf("track path=%q want %q", tracks[0].Path, subPath)
	}
}

func writeFileContent(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
