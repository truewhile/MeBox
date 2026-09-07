package service

import (
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestGroupMediaVersionsMergesEpisodeByExternalIDAcrossLibraries(t *testing.T) {
	local := model.Media{
		LibraryID:  "local-tv",
		Title:      "折腰",
		Path:       "/media/国产剧/折腰 (2025)/Season 1/折腰.S01E01.mkv",
		SeasonNum:  1,
		EpisodeNum: 1,
		TMDbID:     296753,
		SizeBytes:  100,
		PosterURL:  "https://image.tmdb.org/t/p/w500/poster.jpg",
	}
	cloud := model.Media{
		LibraryID:  "cloud-tv",
		Title:      "折腰",
		Path:       "cloud://openlist/国产剧/折腰 (2025) {tmdb-296753}/Season 1/折腰.S01E01.mkv",
		SeasonNum:  1,
		EpisodeNum: 1,
		TMDbID:     296753,
		SizeBytes:  200,
		STRMURL:    "/api/cloud/play/openlist?ref=/国产剧/折腰/01.mkv",
	}

	grouped := groupMediaVersions([]model.Media{local, cloud})
	if len(grouped) != 1 {
		t.Fatalf("grouped len = %d, want 1: %#v", len(grouped), grouped)
	}
	if len(grouped[0].Versions) != 2 {
		t.Fatalf("versions len = %d, want 2: %#v", len(grouped[0].Versions), grouped[0].Versions)
	}
	if grouped[0].Media.Path != local.Path {
		t.Fatalf("local version should remain primary, got %q want %q", grouped[0].Media.Path, local.Path)
	}
	if grouped[0].Versions[0].Path != local.Path || grouped[0].Versions[1].Path != cloud.Path {
		t.Fatalf("versions should be ordered local before cloud, got %#v", grouped[0].Versions)
	}
}

func TestGroupMediaVersionsMergesMovieEncodingVariants(t *testing.T) {
	hd := model.Media{
		LibraryID:    "movies",
		Title:        "Inception 2010 1080p BluRay x264",
		Path:         "/media/Movies/Inception.2010.1080p.BluRay.x264.mkv",
		Year:         2010,
		SizeBytes:    100,
		VideoCodec:   "h264",
		ScrapeStatus: "pending",
	}
	uhd := model.Media{
		LibraryID:    "movies",
		Title:        "Inception 2010 2160p UHD BluRay x265",
		Path:         "/media/Movies/Inception.2010.2160p.UHD.BluRay.x265.mkv",
		Year:         2010,
		SizeBytes:    200,
		VideoCodec:   "hevc",
		ScrapeStatus: "pending",
	}

	grouped := groupMediaVersions([]model.Media{hd, uhd})
	if len(grouped) != 1 {
		t.Fatalf("grouped len = %d, want 1: %#v", len(grouped), grouped)
	}
	if len(grouped[0].Versions) != 2 {
		t.Fatalf("versions len = %d, want 2: %#v", len(grouped[0].Versions), grouped[0].Versions)
	}
	if grouped[0].Media.Path != uhd.Path {
		t.Fatalf("larger 2160p version should be primary, got %q want %q", grouped[0].Media.Path, uhd.Path)
	}
}

func TestGroupMediaVersionsCleansCodecPunctuation(t *testing.T) {
	web := model.Media{
		LibraryID:    "movies",
		Title:        "Everything Everywhere All at Once 2022 WEB-DL H.265 DDP5.1",
		Path:         "/media/Movies/Everything.Everywhere.All.At.Once.2022.2160p.WEB-DL.H.265.DDP5.1-GRP.mkv",
		SizeBytes:    200,
		VideoCodec:   "hevc",
		ScrapeStatus: "pending",
	}
	bluray := model.Media{
		LibraryID:    "movies",
		Title:        "Everything Everywhere All at Once 2022 BluRay x264 DTS",
		Path:         "/media/Movies/Everything.Everywhere.All.At.Once.2022.1080p.BluRay.x264.DTS-GRP.mkv",
		SizeBytes:    100,
		VideoCodec:   "h264",
		ScrapeStatus: "pending",
	}

	grouped := groupMediaVersions([]model.Media{web, bluray})
	if len(grouped) != 1 {
		t.Fatalf("grouped len = %d, want 1: %#v", len(grouped), grouped)
	}
	if len(grouped[0].Versions) != 2 {
		t.Fatalf("versions len = %d, want 2: %#v", len(grouped[0].Versions), grouped[0].Versions)
	}
	if grouped[0].Media.Path != web.Path {
		t.Fatalf("larger WEB-DL version should be primary, got %q want %q", grouped[0].Media.Path, web.Path)
	}
}

func TestListMediaVisibleGroupedPaginatesAfterVersionGrouping(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.Media{})
	repos := repository.New(db)
	lib := model.Library{Name: "电影", Path: "/media/movies", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows := []model.Media{
		{
			Base:      model.Base{CreatedAt: now.Add(2 * time.Hour), UpdatedAt: now.Add(2 * time.Hour)},
			LibraryID: lib.ID,
			Title:     "Inception 2010 2160p UHD BluRay x265",
			Path:      "/media/movies/Inception.2010.2160p.UHD.BluRay.x265.mkv",
			TMDbID:    27205,
			Year:      2010,
			Width:     3840,
			Height:    2160,
			SizeBytes: 200,
		},
		{
			Base:      model.Base{CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour)},
			LibraryID: lib.ID,
			Title:     "Inception 2010 1080p BluRay x264",
			Path:      "/media/movies/Inception.2010.1080p.BluRay.x264.mkv",
			TMDbID:    27205,
			Year:      2010,
			Width:     1920,
			Height:    1080,
			SizeBytes: 100,
		},
		{
			Base:      model.Base{CreatedAt: now, UpdatedAt: now},
			LibraryID: lib.ID,
			Title:     "The Matrix 1999 1080p BluRay",
			Path:      "/media/movies/The.Matrix.1999.1080p.BluRay.mkv",
			TMDbID:    603,
			Year:      1999,
			SizeBytes: 90,
		},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos)
	page, total, err := svc.ListMediaVisibleGrouped(t.Context(), lib.ID, 1, 1, MediaVisibility{IncludeNSFW: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("grouped total = %d, want 2", total)
	}
	if len(page) != 1 || len(page[0].Versions) != 2 {
		t.Fatalf("first page should contain merged Inception versions, got %#v", page)
	}
	if page[0].Media.Path != rows[0].Path {
		t.Fatalf("primary version = %q, want %q", page[0].Media.Path, rows[0].Path)
	}
}

func TestSearchMediaVisiblePageGroupedPaginatesAfterVersionGrouping(t *testing.T) {
	db := newServiceTestDB(t, &model.Media{})
	repos := repository.New(db)
	now := time.Now()
	rows := []model.Media{
		{
			Base:      model.Base{CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour)},
			LibraryID: "movies",
			Title:     "Dune 2021 2160p WEB-DL H265",
			Path:      "/media/movies/Dune.2021.2160p.WEB-DL.H265.mkv",
			TMDbID:    438631,
			Year:      2021,
			Width:     3840,
			Height:    2160,
			SizeBytes: 220,
		},
		{
			Base:      model.Base{CreatedAt: now, UpdatedAt: now},
			LibraryID: "movies",
			Title:     "Dune 2021 1080p BluRay x264",
			Path:      "/media/movies/Dune.2021.1080p.BluRay.x264.mkv",
			TMDbID:    438631,
			Year:      2021,
			Width:     1920,
			Height:    1080,
			SizeBytes: 120,
		},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos)
	page, total, err := svc.SearchMediaVisiblePageGrouped(t.Context(), "Dune", 1, 1, MediaVisibility{IncludeNSFW: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("grouped total = %d, want 1", total)
	}
	if len(page) != 1 || len(page[0].Versions) != 2 {
		t.Fatalf("search page should contain merged Dune versions, got %#v", page)
	}
	if page[0].Media.Path != rows[0].Path {
		t.Fatalf("primary version = %q, want %q", page[0].Media.Path, rows[0].Path)
	}
}

func TestGroupMediaVersionsDoesNotMergeDifferentRemoteSeries(t *testing.T) {
	// 模拟来自两个不同挂载库（如 00 新番连载 与 2018 动漫）的同名剧集《碧蓝之海》
	season1 := model.Media{
		Base:        model.Base{ID: EncodeEmbyRemoteID("mount-1", "156019")},
		LibraryID:   EncodeEmbyRemoteID("mount-1", "view-1"),
		Title:       "碧蓝之海",
		Year:        2018,
		LibraryName: "2018 动漫",
	}
	season2 := model.Media{
		Base:        model.Base{ID: EncodeEmbyRemoteID("mount-2", "156030")},
		LibraryID:   EncodeEmbyRemoteID("mount-2", "view-2"),
		Title:       "碧蓝之海",
		Year:        2024,
		LibraryName: "00 新番连载",
	}

	grouped := groupMediaVersions([]model.Media{season1, season2})
	if len(grouped) != 2 {
		t.Fatalf("expected 2 separate groups for different remote series/libraries, got %d: %#v", len(grouped), grouped)
	}
	if grouped[0].Title != "碧蓝之海" || grouped[1].Title != "碧蓝之海" {
		t.Fatalf("expected both titles to be '碧蓝之海'")
	}
}

func TestGroupMediaVersionsDoesNotMergeAcrossDifferentLibrariesForMovies(t *testing.T) {
	movieLib1 := model.Media{
		Base:      model.Base{ID: "m-1"},
		LibraryID: "lib-1",
		Title:     "碧蓝之海",
		Year:      2018,
	}
	movieLib2 := model.Media{
		Base:      model.Base{ID: "m-2"},
		LibraryID: "lib-2",
		Title:     "碧蓝之海",
		Year:      2018,
	}

	grouped := groupMediaVersions([]model.Media{movieLib1, movieLib2})
	if len(grouped) != 2 {
		t.Fatalf("expected 2 separate groups for movies in different libraries, got %d", len(grouped))
	}
}

func TestGroupMediaVersionsMergesKeepExtStrmVariants(t *testing.T) {
	mkv := model.Media{
		LibraryID: "movies",
		Title:     "竞女01",
		Path:      "/strm/竞女01.mkv.strm",
		SizeBytes: 500,
		STRMURL:   "/api/strm/play/cloud115/video.mkv?pickcode=a",
	}
	mp4 := model.Media{
		LibraryID: "movies",
		Title:     "竞女01",
		Path:      "/strm/竞女01.mp4.strm",
		SizeBytes: 100,
		STRMURL:   "/api/strm/play/cloud115/video.mp4?pickcode=b",
	}
	grouped := groupMediaVersions([]model.Media{mkv, mp4})
	if len(grouped) != 1 {
		t.Fatalf("grouped len = %d, want 1", len(grouped))
	}
	if len(grouped[0].Versions) != 2 {
		t.Fatalf("versions len = %d, want 2", len(grouped[0].Versions))
	}
	if grouped[0].Path != mkv.Path {
		t.Fatalf("primary should be larger mkv, got %q", grouped[0].Path)
	}
}

func TestMediaVersionLabelUsesContainerAndSize(t *testing.T) {
	label := MediaVersionLabel(model.Media{
		Title:     "竞女01",
		Path:      "/strm/竞女01.mkv.strm",
		Height:    1080,
		SizeBytes: 1024 * 1024 * 1200,
		STRMURL:   "/api/strm/play/cloud115/video.mkv?pickcode=a",
	})
	if !strings.Contains(label, "1080p") || !strings.Contains(strings.ToUpper(label), "MKV") {
		t.Fatalf("unexpected label %q", label)
	}
}
