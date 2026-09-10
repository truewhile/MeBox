package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadLocalMetadataDropsResolutionArtifactEpisode(t *testing.T) {
	// 刮削曾把分辨率 1920x1080 误读成 S20E108 并回写进边车 NFO；
	// 重扫时必须忽略这个伪季集号，否则旧文件会把错误身份灌回 DB。
	root := t.TempDir()
	showDir := filepath.Join(root, "彼得·格里尔的贤者时间")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(showDir,
		"[UHA-WINGS][Peter Grill to Kenja no Jikan][01][BDRIP 1920x1080 HEVC-YUV420P10 FLAC].strm")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nfoPath(mediaPath), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<episodedetails>
  <title>第 108 集</title>
  <showtitle>彼得·格里尔的贤者时间</showtitle>
  <season>20</season>
  <episode>108</episode>
  <tmdbid>99080</tmdbid>
</episodedetails>`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLocalMetadata(mediaPath, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("metadata is nil")
	}
	if got.SeasonNum != 0 || got.EpisodeNum != 0 {
		t.Fatalf("resolution artifact season/episode not dropped: s=%d e=%d", got.SeasonNum, got.EpisodeNum)
	}
	if got.EpisodeTitle != "" {
		t.Fatalf("generated episode title not dropped: %q", got.EpisodeTitle)
	}
	if got.Title != "彼得·格里尔的贤者时间" {
		t.Fatalf("series title = %q, want 彼得·格里尔的贤者时间", got.Title)
	}
}

func TestReadLocalMovieMetadata(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Inception.2010.mkv")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	nfo := `<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <title>盗梦空间</title>
  <originaltitle>Inception</originaltitle>
  <year>2010</year>
  <plot>梦境盗窃。</plot>
  <rating>8.8</rating>
  <uniqueid type="tmdb">27205</uniqueid>
  <genre>科幻</genre>
  <genre>动作</genre>
</movie>`
	if err := os.WriteFile(nfoPath(mediaPath), []byte(nfo), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLocalMetadata(mediaPath, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Title != "盗梦空间" || got.OriginalName != "Inception" || got.Year != 2010 || got.TMDbID != 27205 {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	if got.Genres != "科幻,动作" {
		t.Fatalf("genres = %q", got.Genres)
	}
}

func TestReadLocalEpisodeMetadataMergesShowAndEpisode(t *testing.T) {
	root := t.TempDir()
	showDir := filepath.Join(root, "Show")
	seasonDir := filepath.Join(showDir, "Season 02")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(seasonDir, "Show - EP03.mkv")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "tvshow.nfo"), []byte(`<tvshow><title>正确剧名</title><year>2024</year><tmdbid>123</tmdbid></tvshow>`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 单集 NFO 携带【单集级】tmdb id(4375419)与单集名(第三集):二者都不得
	// 覆盖整剧字段,否则同剧各集 id/原名互不相同会被拆成多张卡。
	if err := os.WriteFile(nfoPath(mediaPath), []byte(`<episodedetails><title>第三集</title><season>2</season><episode>3</episode><plot>本集简介</plot><uniqueid type="tmdb">4375419</uniqueid></episodedetails>`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLocalMetadata(mediaPath, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Title != "正确剧名" || got.SeasonNum != 2 || got.EpisodeNum != 3 {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	// 单集名不得写入整剧原名(分组键)。
	if got.OriginalName != "" {
		t.Fatalf("episode title must not pollute OriginalName, got %q", got.OriginalName)
	}
	if got.EpisodeTitle != "第三集" {
		t.Fatalf("episode title metadata = %q, want 第三集", got.EpisodeTitle)
	}
	// 单集级简介按集回填;整剧 tmdb 仍取 tvshow.nfo 的 123,单集 id 不得覆盖。
	if got.Overview != "本集简介" || got.TMDbID != 123 {
		t.Fatalf("episode/show merge failed: %+v", got)
	}
}

func TestReadLocalEpisodeMetadataWithoutShowTitleDoesNotUseEpisodeTitleAsSeries(t *testing.T) {
	root := t.TempDir()
	showDir := filepath.Join(root, "哈哈哈哈哈")
	seasonDir := filepath.Join(showDir, "Season 06")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(seasonDir, "哈哈哈哈哈 - S06E11.mkv")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nfoPath(mediaPath), []byte(`<episodedetails><title>第 11 集</title><season>6</season><episode>11</episode><uniqueid type="tmdb">4375419</uniqueid></episodedetails>`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLocalMetadata(mediaPath, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("metadata is nil")
	}
	if got.Title != "" {
		t.Fatalf("episode title must not become series title, got %q", got.Title)
	}
	if got.EpisodeTitle != "第 11 集" || got.SeasonNum != 6 || got.EpisodeNum != 11 {
		t.Fatalf("episode metadata not preserved: %+v", got)
	}
	if got.TMDbID != 0 {
		t.Fatalf("episode-level tmdb id must not become series tmdb id, got %d", got.TMDbID)
	}
}

func TestMergeEpisodeMetadataKeepsExistingSeasonWhenEpisodeNFOOmitsIt(t *testing.T) {
	dst := &LocalMetadata{Title: "哈哈哈哈哈", SeasonNum: 6}
	episodeDoc := &nfoDocument{Title: "第 11 集", Episode: 11}
	episode := metadataFromDoc(episodeDoc, "", true)

	mergeEpisodeMetadata(dst, episode, episodeDoc)

	if dst.SeasonNum != 6 || dst.EpisodeNum != 11 {
		t.Fatalf("episode NFO without season should keep parsed season, got %+v", dst)
	}
}

func TestReadLocalEpisodeMetadataIgnoresNoneNumericFields(t *testing.T) {
	root := t.TempDir()
	showDir := filepath.Join(root, "链锯人 总集篇 (2025)")
	seasonDir := filepath.Join(showDir, "Season 1")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(seasonDir, "链锯人 总集篇 - S01E01.mkv")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "tvshow.nfo"), []byte(`<tvshow><title>链锯人 总集篇</title><year>2025</year></tvshow>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nfoPath(mediaPath), []byte(`<episodedetails><title>第 1 集</title><season>None</season><episode>None</episode><rating>None</rating></episodedetails>`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLocalMetadata(mediaPath, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Title != "链锯人 总集篇" || got.Year != 2025 {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	if got.SeasonNum != 0 || got.EpisodeNum != 0 || got.Rating != 0 {
		t.Fatalf("None numeric fields should parse as zero, got %+v", got)
	}
}

func TestReadLocalVarietyMetadataUsesLocalArtwork(t *testing.T) {
	root := t.TempDir()
	showDir := filepath.Join(root, "哈哈哈哈哈")
	seasonDir := filepath.Join(showDir, "Season 06")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(seasonDir, "哈哈哈哈哈 - S06E17.mkv")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "哈哈哈哈哈.nfo"), []byte(`<tvshow><title>哈哈哈哈哈</title><genre>综艺</genre></tvshow>`), 0o644); err != nil {
		t.Fatal(err)
	}
	showPoster := filepath.Join(showDir, "poster.jpg")
	if err := os.WriteFile(showPoster, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	episodeThumb := filepath.Join(seasonDir, "哈哈哈哈哈 - S06E17-thumb.jpg")
	if err := os.WriteFile(episodeThumb, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	backdrop := filepath.Join(showDir, "fanart.jpg")
	if err := os.WriteFile(backdrop, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLocalMetadata(mediaPath, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("metadata is nil")
	}
	if got.Title != "哈哈哈哈哈" || got.Genres != "综艺" {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	if got.PosterURL != showPoster {
		t.Fatalf("PosterURL = %q, want show poster %q, not episode thumb %q", got.PosterURL, showPoster, episodeThumb)
	}
	if got.BackdropURL != backdrop {
		t.Fatalf("BackdropURL = %q, want %q", got.BackdropURL, backdrop)
	}
}

func TestReadLocalMetadataPrioritizesPosterOverThumbAndStills(t *testing.T) {
	root := t.TempDir()
	mediaPath := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	thumb := filepath.Join(root, "Movie-thumb.jpg")
	if err := os.WriteFile(thumb, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	still := filepath.Join(root, "Movie-still.jpg")
	if err := os.WriteFile(still, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	poster := filepath.Join(root, "poster.jpg")
	if err := os.WriteFile(poster, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLocalMetadata(mediaPath, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.PosterURL != poster {
		t.Fatalf("PosterURL = %q, want poster %q", got.PosterURL, poster)
	}
}

func TestReadLocalMetadataIgnoresActorAndStillArtworkOnly(t *testing.T) {
	root := t.TempDir()
	mediaPath := filepath.Join(root, "SSIS-001.mp4")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"actor.jpg", "sample.jpg", "fanart.jpg"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("jpg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ReadLocalMetadata(mediaPath, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.PosterURL != "" || got.BackdropURL == "" {
		t.Fatalf("expected backdrop-only metadata without actor/still poster, got %+v", got)
	}
}

func TestReadLocalMetadataWithoutNFOStillFindsArtwork(t *testing.T) {
	root := t.TempDir()
	mediaPath := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	poster := filepath.Join(root, "Movie-poster.jpg")
	if err := os.WriteFile(poster, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLocalMetadata(mediaPath, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.PosterURL != poster {
		t.Fatalf("unexpected artwork metadata: %+v", got)
	}
}

// TestReadLocalMetadataRecoversFromTruncatedShowNFO mirrors a real-world issue:
// some anime tvshow.nfo files are truncated mid-URL (unexpected EOF). Before the
// fix this discarded the whole series, leaving episodes pending with per-episode
// titles and no poster. The recoverable fields (title/year) and the matching
// episode NFO + local artwork must still be applied.
func TestReadLocalMetadataRecoversFromTruncatedShowNFO(t *testing.T) {
	root := t.TempDir()
	showDir := filepath.Join(root, "夏日重现 (2022)")
	seasonDir := filepath.Join(showDir, "Season 1")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(seasonDir, "S01E01.mkv")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// tvshow.nfo cut off inside a <thumb> URL, like the broken real files.
	truncated := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<tvshow>
  <title>夏日重现</title>
  <originaltitle>サマータイムレンダ</originaltitle>
  <year>2022</year>
  <plot>听闻自己青梅竹马死讯。</plot>
  <thumb aspect="poster">https://image.tmdb.org/t/p/original/2koyWLm6iVn5OTEExTjKzVms5Iz.jpg</thumb>
  <fanart>
    <thumb>https://image.tmdb.org/t/p/original/p2eZlGwd8OjkWpwD2hSoBiIlHBZ.jpg</thu`
	if err := os.WriteFile(filepath.Join(showDir, "tvshow.nfo"), []byte(truncated), 0o644); err != nil {
		t.Fatal(err)
	}
	// The episode sidecar NFO is complete and should still be merged.
	if err := os.WriteFile(nfoPath(mediaPath), []byte(`<episodedetails><title>再见了夏日</title><season>1</season><episode>1</episode></episodedetails>`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Local artwork next to the show folder.
	poster := filepath.Join(showDir, "poster.jpg")
	if err := os.WriteFile(poster, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLocalMetadata(mediaPath, root, true)
	if err != nil {
		t.Fatalf("ReadLocalMetadata returned error on truncated show NFO: %v", err)
	}
	if got == nil {
		t.Fatal("metadata is nil; truncated show NFO discarded the series")
	}
	// The recovered show title takes precedence over the episode title.
	if got.Title != "夏日重现" {
		t.Fatalf("Title = %q, want recovered show title 夏日重现", got.Title)
	}
	if got.Year != 2022 {
		t.Fatalf("Year = %d, want 2022", got.Year)
	}
	if got.EpisodeTitle != "再见了夏日" || got.SeasonNum != 1 || got.EpisodeNum != 1 {
		t.Fatalf("episode metadata not preserved: %+v", got)
	}
	// Prior to the fix the episode metadata was dropped entirely; the poster comes
	// from the local poster.jpg next to the show.
	if got.PosterURL != poster {
		t.Fatalf("PosterURL = %q, want local poster %q", got.PosterURL, poster)
	}
	// A truncated show NFO must not be treated as an authoritative match by
	// itself; since the episode NFO is valid we still mark it matched so the
	// recovered series title participates in grouping.
	if !got.HasNFO {
		t.Fatalf("HasNFO = false, want true (episode NFO is valid)")
	}
}

// TestReadLocalMetadataKeepsArtworkOnlyWhenNoUsableNFO verifies that when a show
// NFO is truncated AND yields no recoverable fields, we still fall back to local
// artwork instead of returning an error.
func TestReadLocalMetadataArtworkFallbackOnGarbageShowNFO(t *testing.T) {
	root := t.TempDir()
	showDir := filepath.Join(root, "Some Show")
	seasonDir := filepath.Join(showDir, "Season 1")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(seasonDir, "S01E01.mkv")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Severely truncated: no recoverable title/fields at all.
	if err := os.WriteFile(filepath.Join(showDir, "tvshow.nfo"), []byte(`<tvshow><title>半截`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Episode-level backdrop sits next to the episode file.
	backdrop := filepath.Join(seasonDir, "S01E01-backdrop.jpg")
	if err := os.WriteFile(backdrop, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLocalMetadata(mediaPath, root, true)
	if err != nil {
		t.Fatalf("ReadLocalMetadata should not error when NFO is unusable: %v", err)
	}
	if got == nil {
		t.Fatal("metadata is nil; artwork fallback missing")
	}
	if got.HasNFO {
		t.Fatalf("HasNFO = true, want false for unusable NFO")
	}
	if got.BackdropURL != backdrop {
		t.Fatalf("expected episode artwork fallback, got %+v", got)
	}
}
