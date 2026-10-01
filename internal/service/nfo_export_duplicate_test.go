package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

func writeNFOTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A folder that already carries the Kodi/Emby convention "movie.nfo" must be
// updated in place. Exporting used to always write "<filename>.nfo", leaving two
// divergent NFOs for one movie — and since the reader prefers "<filename>.nfo",
// the pre-existing movie.nfo silently went stale.
func TestWriteMediaNFOUpdatesExistingMovieNFO(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "The Shawshank Redemption (1994).mp4")
	writeNFOTestFile(t, mediaPath, "video")
	existing := filepath.Join(dir, "movie.nfo")
	writeNFOTestFile(t, existing, `<?xml version="1.0"?><movie><title>旧标题</title></movie>`)

	media := &model.Media{
		Title:        "肖申克的救赎",
		Year:         1994,
		Path:         mediaPath,
		Overview:     "希望让人自由。",
		TMDbID:       278,
		ScrapeStatus: "matched",
	}
	dst, err := WriteMediaNFO(media)
	if err != nil {
		t.Fatalf("WriteMediaNFO: %v", err)
	}
	if filepath.Clean(dst) != filepath.Clean(existing) {
		t.Fatalf("export wrote %q, want the existing movie.nfo at %q", dst, existing)
	}
	if _, err := os.Stat(filepath.Join(dir, "The Shawshank Redemption (1994).nfo")); err == nil {
		t.Fatal("export created a duplicate <filename>.nfo next to movie.nfo")
	}
	body, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "旧标题") || !strings.Contains(string(body), "肖申克的救赎") {
		t.Fatalf("movie.nfo was not refreshed: %s", string(body))
	}
}

// With no pre-existing sidecar the canonical <filename>.nfo is still the target.
func TestWriteMediaNFOWritesCanonicalNameByDefault(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Inception (2010).mkv")
	writeNFOTestFile(t, mediaPath, "video")

	dst, err := WriteMediaNFO(&model.Media{Title: "盗梦空间", Year: 2010, Path: mediaPath})
	if err != nil {
		t.Fatalf("WriteMediaNFO: %v", err)
	}
	want := filepath.Join(dir, "Inception (2010).nfo")
	if filepath.Clean(dst) != filepath.Clean(want) {
		t.Fatalf("export wrote %q, want %q", dst, want)
	}
}

// Re-exporting over an existing canonical sidecar must keep updating that file.
func TestWriteMediaNFOOverwritesCanonicalSidecar(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Inception (2010).mkv")
	writeNFOTestFile(t, mediaPath, "video")
	canonical := filepath.Join(dir, "Inception (2010).nfo")
	writeNFOTestFile(t, canonical, `<?xml version="1.0"?><movie><title>old</title></movie>`)

	dst, err := WriteMediaNFO(&model.Media{Title: "盗梦空间", Year: 2010, Path: mediaPath})
	if err != nil {
		t.Fatalf("WriteMediaNFO: %v", err)
	}
	if filepath.Clean(dst) != filepath.Clean(canonical) {
		t.Fatalf("export wrote %q, want %q", dst, canonical)
	}
	body, _ := os.ReadFile(canonical)
	if strings.Contains(string(body), ">old<") {
		t.Fatal("existing canonical sidecar was not refreshed")
	}
}

// Episode exports must never be redirected to the series-level movie.nfo /
// <dirname>.nfo files: those describe the show, not the episode.
func TestWriteMediaNFOEpisodeIgnoresSeriesLevelSidecars(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Show - S01E01.mkv")
	writeNFOTestFile(t, mediaPath, "video")
	seriesNFO := filepath.Join(dir, "movie.nfo")
	writeNFOTestFile(t, seriesNFO, `<?xml version="1.0"?><movie><title>series level</title></movie>`)

	dst, err := WriteMediaNFO(&model.Media{
		Title:      "Show",
		Path:       mediaPath,
		SeasonNum:  1,
		EpisodeNum: 1,
	})
	if err != nil {
		t.Fatalf("WriteMediaNFO: %v", err)
	}
	if filepath.Clean(dst) == filepath.Clean(seriesNFO) {
		t.Fatal("episode export must not overwrite the series-level movie.nfo")
	}
	if filepath.Clean(dst) != filepath.Clean(filepath.Join(dir, "Show - S01E01.nfo")) {
		t.Fatalf("episode export wrote %q, want the episode sidecar", dst)
	}
	body, _ := os.ReadFile(seriesNFO)
	if !strings.Contains(string(body), "series level") {
		t.Fatal("series-level movie.nfo was modified by an episode export")
	}
}

// The <dirname>.nfo convention must be reused too.
func TestWriteMediaNFOUpdatesDirectoryNamedNFO(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "Interstellar (2014)")
	mediaPath := filepath.Join(dir, "Interstellar (2014).mkv")
	writeNFOTestFile(t, mediaPath, "video")
	dirNFO := filepath.Join(dir, "Interstellar (2014).nfo")
	// The directory-named sidecar is the only pre-existing candidate besides the
	// canonical name, so the canonical name is skipped and this one is reused.
	writeNFOTestFile(t, dirNFO, `<?xml version="1.0"?><movie><title>old</title></movie>`)

	dst, err := WriteMediaNFO(&model.Media{Title: "星际穿越", Year: 2014, Path: mediaPath})
	if err != nil {
		t.Fatalf("WriteMediaNFO: %v", err)
	}
	if filepath.Clean(dst) != filepath.Clean(dirNFO) {
		t.Fatalf("export wrote %q, want %q", dst, dirNFO)
	}
}
