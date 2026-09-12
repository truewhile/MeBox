package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

func TestQueueLibraryRootScanImportsNewLibraryMedia(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.LibraryRoot{}, &model.Media{}); err != nil {
		t.Fatal(err)
	}
	rootPath := t.TempDir()
	mediaPath := filepath.Join(rootPath, "Example Movie (2026).mkv")
	if err := os.WriteFile(mediaPath, []byte("movie"), 0o644); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	lib := model.Library{Name: "电影", Path: rootPath, Type: "movie", Enabled: true}
	root := model.LibraryRoot{Name: "电影", Path: rootPath, Enabled: true}
	if err := repos.Library.CreateWithRoots(t.Context(), &lib, []model.LibraryRoot{root}); err != nil {
		t.Fatal(err)
	}
	libWithRoots, err := repos.Library.FindByID(t.Context(), lib.ID)
	if err != nil || libWithRoots == nil || len(libWithRoots.Roots) != 1 {
		t.Fatalf("library roots=%#v err=%v", libWithRoots, err)
	}
	scanner := service.NewScannerService(&config.Config{}, zap.NewNop(), repos, service.NewHub(zap.NewNop()), nil, nil)
	svc := &service.Container{Repo: repos, Scan: scanner}
	queueLibraryRootScan(svc, lib.ID, libWithRoots.Roots[0].ID)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		if err := repos.DB.Model(&model.Media{}).Where("library_id = ?", lib.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("initial library scan did not import the media file")
}

func TestListLibrariesHidesAdultDirectoriesUnlessAdminRequestsAll(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Library{}, &model.Media{}, &model.Setting{}, &model.PlayProfile{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	viewer := &model.User{Username: "viewer", PasswordHash: "hash", Role: "admin", HideAdult: true}
	if err := repos.User.Create(t.Context(), viewer); err != nil {
		t.Fatal(err)
	}
	safe := model.Library{Name: "电影", Path: "/media/movie", Type: "movie", Enabled: true}
	adult := model.Library{Name: "9KG", Path: "/media/9KG", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &safe); err != nil {
		t.Fatal(err)
	}
	if err := repos.Library.Create(t.Context(), &adult); err != nil {
		t.Fatal(err)
	}
	if err := repos.Setting.Set(t.Context(), service.AdultLibraryIDsSettingKey, `["`+adult.ID+`"]`); err != nil {
		t.Fatal(err)
	}
	if err := repos.Media.Upsert(t.Context(), &model.Media{LibraryID: safe.ID, Title: "误入普通库的成人条目", Path: "/media/movie/nsfw.mkv", NSFW: true}); err != nil {
		t.Fatal(err)
	}
	svc := &service.Container{
		Repo:  repos,
		Media: service.NewMediaService(&config.Config{}, zap.NewNop(), repos),
	}

	visible := requestLibraries(t, svc, viewer.ID, "admin", "/api/libraries")
	if len(visible) != 1 || visible[0].ID != safe.ID {
		t.Fatalf("watching library list should hide adult directories, got %#v", visible)
	}

		all := requestLibraries(t, svc, viewer.ID, "admin", "/api/libraries?include_hidden=1")
		if len(all) != 2 {
			t.Fatalf("admin include_hidden list should keep management access, got %#v", all)
		}

		filtered := requestLibraries(t, svc, viewer.ID, "admin", "/api/libraries?include_hidden=1&ids="+safe.ID)
		if len(filtered) != 1 || filtered[0].ID != safe.ID {
			t.Fatalf("ids filter should return only requested library, got %#v", filtered)
		}
	}

func TestGetLibraryAllowsEmptyLibrary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Library{}, &model.Media{}, &model.Setting{}, &model.PlayProfile{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	lib := model.Library{Name: "空媒体库", Path: "/media/empty", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	svc := &service.Container{
		Repo:  repos,
		Media: service.NewMediaService(&config.Config{}, zap.NewNop(), repos),
	}

	got := requestLibrary(t, svc, "user-1", "user", "/api/libraries/"+lib.ID, lib.ID)
	if got.ID != lib.ID || got.Name != "空媒体库" {
		t.Fatalf("library detail = %#v, want empty library detail", got)
	}
	media := requestMediaList(t, svc, "/api/libraries/"+lib.ID+"/media", lib.ID)
	if media.Total != 0 || len(media.Items) != 0 {
		t.Fatalf("empty library media = %#v, want no items", media)
	}
}

func TestListMediaGroupsMultipleVersionsByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Library{}, &model.Media{}, &model.Setting{}, &model.PlayProfile{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	lib := model.Library{Name: "Movies", Path: "/media/movies", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	if err := repos.DB.Create(&[]model.Media{
		{
			Base:      model.Base{ID: "movie-1080", CreatedAt: time.Now().Add(-time.Minute)},
			LibraryID: lib.ID,
			Title:     "流浪地球",
			Path:      "/media/movies/The.Wandering.Earth.2019.1080p.mkv",
			Year:      2019,
			Width:     1920,
			Height:    1080,
			SizeBytes: 100,
		},
		{
			Base:      model.Base{ID: "movie-2160", CreatedAt: time.Now()},
			LibraryID: lib.ID,
			Title:     "流浪地球",
			Path:      "cloud://openlist/Movies/The.Wandering.Earth.2019.2160p.mkv",
			Year:      2019,
			Width:     3840,
			Height:    2160,
			SizeBytes: 200,
		},
	}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &service.Container{
		Repo:  repos,
		Media: service.NewMediaService(&config.Config{}, zap.NewNop(), repos),
	}

	grouped := requestMediaList(t, svc, "/api/libraries/"+lib.ID+"/media", lib.ID)
	if grouped.Total != 1 || len(grouped.Items) != 1 {
		t.Fatalf("grouped response total=%d len=%d body=%#v", grouped.Total, len(grouped.Items), grouped)
	}
	if grouped.Items[0].ID != "movie-1080" {
		t.Fatalf("primary id = %q, want local version to remain primary", grouped.Items[0].ID)
	}
	if len(grouped.Items[0].Versions) != 2 {
		t.Fatalf("versions = %#v, want both versions", grouped.Items[0].Versions)
	}
	if grouped.Items[0].Versions[0].ID != "movie-1080" || grouped.Items[0].Versions[1].ID != "movie-2160" {
		t.Fatalf("versions should keep local before cloud: %#v", grouped.Items[0].Versions)
	}

	raw := requestMediaList(t, svc, "/api/libraries/"+lib.ID+"/media?group_versions=0", lib.ID)
	if raw.Total != 2 || len(raw.Items) != 2 {
		t.Fatalf("raw response total=%d len=%d body=%#v", raw.Total, len(raw.Items), raw)
	}
}

func TestListLibrarySeriesDoesNotTruncateLargeEpisodeLibraries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Library{}, &model.Media{}, &model.Setting{}, &model.PlayProfile{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	lib := model.Library{Name: "国漫", Path: "cloud://openlist/国漫", Type: "anime", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	rows := make([]model.Media, 0, 2001)
	for i := 1; i <= 2001; i++ {
		rows = append(rows, model.Media{
			Base:       model.Base{ID: fmt.Sprintf("ep-%04d", i), CreatedAt: time.Now().Add(time.Duration(i) * time.Second)},
			LibraryID:  lib.ID,
			Title:      "大剧",
			Path:       fmt.Sprintf("cloud://openlist/国漫/大剧 (2026) {tmdb-123}/Season 1/大剧.S01E%04d.mkv", i),
			SeasonNum:  1,
			EpisodeNum: i,
		})
	}
	if err := repos.DB.CreateInBatches(rows, 500).Error; err != nil {
		t.Fatal(err)
	}
	svc := &service.Container{
		Repo:  repos,
		Media: service.NewMediaService(&config.Config{}, zap.NewNop(), repos),
	}

	series := requestLibrarySeries(t, svc, "/api/libraries/"+lib.ID+"/series", lib.ID)
	if series.Total != 1 || len(series.Items) != 1 {
		t.Fatalf("series response total=%d len=%d body=%#v", series.Total, len(series.Items), series)
	}
	if series.Items[0].Count != 2001 {
		t.Fatalf("series count = %d, want 2001", series.Items[0].Count)
	}
	if !strings.HasPrefix(series.Items[0].Key, "series:") ||
		strings.Contains(series.Items[0].Key, "lib:") ||
		strings.Contains(series.Items[0].Key, "show:") {
		t.Fatalf("series key = %q, want compact non-raw key", series.Items[0].Key)
	}
	episodes := requestLibrarySeriesEpisodes(t, svc, "/api/libraries/"+lib.ID+"/series/episodes?key="+url.QueryEscape(series.Items[0].Key), lib.ID)
	if episodes.Total != 2001 || len(episodes.Items) != 2001 {
		t.Fatalf("episodes total=%d len=%d, want 2001", episodes.Total, len(episodes.Items))
	}
	if episodes.Items[0].EpisodeNum != 1 || episodes.Items[len(episodes.Items)-1].EpisodeNum != 2001 {
		t.Fatalf("episode order first=%d last=%d", episodes.Items[0].EpisodeNum, episodes.Items[len(episodes.Items)-1].EpisodeNum)
	}
}

func TestScrapeOptionsFromRequestPreservesEpisodeImagesFalse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/media/ep-1/scrape", bytes.NewBufferString(`{"episode_images":false,"refresh_matched":true}`))
	c.Request.Header.Set("Content-Type", "application/json")

	options, err := scrapeOptionsFromRequest(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if options.EpisodeArtwork == nil {
		t.Fatal("EpisodeArtwork is nil, want explicit false")
	}
	if *options.EpisodeArtwork {
		t.Fatal("EpisodeArtwork = true, want false")
	}
	if !options.IncludeMatched {
		t.Fatal("IncludeMatched = false, want true from refresh_matched")
	}
}

func requestLibraries(t *testing.T, svc *service.Container, userID, role, path string) []model.Library {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(middleware.CtxUserID, userID)
	c.Set(middleware.CtxUserRole, role)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	listLibrariesHandler(svc)(c)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d body=%s", path, w.Code, w.Body.String())
	}
	var libs []model.Library
	if err := json.Unmarshal(w.Body.Bytes(), &libs); err != nil {
		t.Fatalf("decode libraries: %v", err)
	}
	return libs
}

func requestLibrary(t *testing.T, svc *service.Container, userID, role, path, libraryID string) model.Library {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(middleware.CtxUserID, userID)
	c.Set(middleware.CtxUserRole, role)
	c.Params = gin.Params{{Key: "id", Value: libraryID}}
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	getLibraryHandler(svc)(c)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d body=%s", path, w.Code, w.Body.String())
	}
	var lib model.Library
	if err := json.Unmarshal(w.Body.Bytes(), &lib); err != nil {
		t.Fatalf("decode library: %v", err)
	}
	return lib
}

type mediaListResponse struct {
	Items []service.MediaItem `json:"items"`
	Total int64               `json:"total"`
}

type seriesListResponse struct {
	Items []service.SeriesCard `json:"items"`
	Total int64                `json:"total"`
}

type seriesEpisodesResponse struct {
	Items []model.Media `json:"items"`
	Total int64         `json:"total"`
}

func requestMediaList(t *testing.T, svc *service.Container, path, libraryID string) mediaListResponse {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(middleware.CtxUserID, "user-1")
	c.Set(middleware.CtxUserRole, "user")
	c.Params = gin.Params{{Key: "id", Value: libraryID}}
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	listMediaHandler(svc)(c)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d body=%s", path, w.Code, w.Body.String())
	}
	var payload mediaListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode media list: %v", err)
	}
	return payload
}

func requestLibrarySeries(t *testing.T, svc *service.Container, path, libraryID string) seriesListResponse {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(middleware.CtxUserID, "user-1")
	c.Set(middleware.CtxUserRole, "user")
	c.Params = gin.Params{{Key: "id", Value: libraryID}}
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	listLibrarySeriesHandler(svc)(c)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d body=%s", path, w.Code, w.Body.String())
	}
	var payload seriesListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode series list: %v", err)
	}
	return payload
}

func requestLibrarySeriesEpisodes(t *testing.T, svc *service.Container, path, libraryID string) seriesEpisodesResponse {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(middleware.CtxUserID, "user-1")
	c.Set(middleware.CtxUserRole, "user")
	c.Params = gin.Params{{Key: "id", Value: libraryID}}
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	listLibrarySeriesEpisodesHandler(svc)(c)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d body=%s", path, w.Code, w.Body.String())
	}
	var payload seriesEpisodesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode series episodes: %v", err)
	}
	return payload
}

// 空库的 media / series 列表必须返回 "items":[]（而不是 Go nil slice 序列化出的 null）。
// 前端 [].concat(null) 会得到 [null]，随后在渲染期解引用 null 崩溃，导致「空库点进去
// 报错且无法返回」。这条测试钉死该 JSON 契约，防止再退化。
func TestEmptyLibraryListsReturnEmptyArraysNotNull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	movie := model.Library{Name: "空电影库", Path: "/media/empty-movie", Type: "movie", Enabled: true}
	tv := model.Library{Name: "空剧集库", Path: "/media/empty-tv", Type: "tv", Enabled: true}
	for _, lib := range []*model.Library{&movie, &tv} {
		if err := repos.Library.Create(t.Context(), lib); err != nil {
			t.Fatal(err)
		}
	}
	svc := &service.Container{
		Repo:  repos,
		Media: service.NewMediaService(&config.Config{}, zap.NewNop(), repos),
	}

	cases := []struct {
		name    string
		path    string
		lib     string
		handler gin.HandlerFunc
	}{
		{"media", "/api/libraries/" + movie.ID + "/media", movie.ID, listMediaHandler(svc)},
		{"series", "/api/libraries/" + tv.ID + "/series", tv.ID, listLibrarySeriesHandler(svc)},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(middleware.CtxUserID, "user-1")
		c.Set(middleware.CtxUserRole, "user")
		c.Params = gin.Params{{Key: "id", Value: tc.lib}}
		c.Request = httptest.NewRequest(http.MethodGet, tc.path, nil)
		tc.handler(c)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body=%s", tc.name, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if strings.Contains(body, `"items":null`) {
			t.Fatalf("%s: empty library returned items:null (crashes frontend): %s", tc.name, body)
		}
			if !strings.Contains(body, `"items":[]`) {
				t.Fatalf("%s: expected items:[] for empty library, got %s", tc.name, body)
			}
		}
	}

func TestSearchMediaGroupsSeriesBeforeLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Library{}, &model.Media{}, &model.Setting{}, &model.PlayProfile{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	lib := model.Library{Name: "动漫", Path: "/media/anime", Type: "anime", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	rows := []model.Media{
		{
			Base:       model.Base{ID: "dbkai-ep-1", CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now.Add(-2 * time.Minute)},
			LibraryID: lib.ID, Title: "龙珠改", Path: "/media/anime/龙珠改 (2009)/Season 1/龙珠改.S01E01.mkv",
			SeasonNum: 1, EpisodeNum: 1, TMDbID: 61709,
		},
		{
			Base:       model.Base{ID: "dbkai-ep-2", CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)},
			LibraryID: lib.ID, Title: "龙珠改", Path: "/media/anime/龙珠改 (2009)/Season 1/龙珠改.S01E02.mkv",
			SeasonNum: 1, EpisodeNum: 2, TMDbID: 61709,
		},
		{
			Base:       model.Base{ID: "dbkai-ep-3", CreatedAt: now, UpdatedAt: now},
			LibraryID: lib.ID, Title: "龙珠改", Path: "/media/anime/龙珠改 (2009)/Season 1/龙珠改.S01E03.mkv",
			SeasonNum: 1, EpisodeNum: 3, TMDbID: 61709,
		},
		{
			Base:       model.Base{ID: "db-movie", CreatedAt: now.Add(-3 * time.Minute), UpdatedAt: now.Add(-3 * time.Minute)},
			LibraryID: lib.ID, Title: "龙珠超：布罗利", Path: "/media/anime/龙珠超：布罗利 (2018)/龙珠超：布罗利.mkv",
			TMDbID: 503314,
		},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	svc := &service.Container{
		Repo:  repos,
		Media: service.NewMediaService(&config.Config{}, zap.NewNop(), repos),
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(middleware.CtxUserID, "user-1")
	c.Set(middleware.CtxUserRole, "user")
	c.Request = httptest.NewRequest(http.MethodGet, "/api/media?q=龙珠&limit=2&group_series=1", nil)
	searchMediaHandler(svc)(c)

	if w.Code != http.StatusOK {
		t.Fatalf("search status=%d, body=%s", w.Code, w.Body.String())
	}
	var res struct {
		Items []model.Media `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("expected one representative per series after limit, got %d: %#v", len(res.Items), res.Items)
	}
	seenSeries := false
	seenMovie := false
	for _, item := range res.Items {
		switch item.TMDbID {
		case 61709:
			seenSeries = true
			if item.EpisodeNum != 1 {
				t.Fatalf("series representative episode=%d, want first episode", item.EpisodeNum)
			}
		case 503314:
			seenMovie = true
		}
	}
	if !seenSeries || !seenMovie {
		t.Fatalf("expected one Dragon Ball series and one movie, got %#v", res.Items)
	}
}

func TestSearchMediaHandlerIncludesEmbyRemote(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("SearchTerm") == "碧蓝之海" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"TotalRecordCount": 1,
				"Items": []map[string]any{
					{
						"Id":             "156030",
						"Name":           "碧蓝之海",
						"Type":           "Series",
						"ProductionYear": 2018,
					},
				},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"TotalRecordCount": 0,
			"Items":            []map[string]any{},
		})
	}))
	defer server.Close()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}, &model.StrmAccount{}, &model.EmbyMount{}, &model.Setting{}, &model.User{}, &model.PlayProfile{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	adminUser := model.User{
		Base:     model.Base{ID: "user-1"},
		Username: "admin",
		Role:     "admin",
	}
	_ = repos.DB.Create(&adminUser).Error

	localLib := model.Library{Name: "本地电影", Path: "/media/movies", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &localLib); err != nil {
		t.Fatal(err)
	}
	localMedia := model.Media{
		Base:      model.Base{ID: "local-1"},
		LibraryID: localLib.ID,
		Title:     "流浪地球",
		Year:      2019,
	}
	if err := repos.DB.Create(&localMedia).Error; err != nil {
		t.Fatal(err)
	}

	rawCfg, _ := json.Marshal(map[string]string{"url": server.URL, "token": "fake-token"})
	acct := model.StrmAccount{
		Base:     model.Base{ID: "acct-1"},
		Name:     "远程Emby",
		Provider: model.StrmProviderEmbyRemote,
		Config:   string(rawCfg),
		Enabled:  true,
	}
	if err := repos.StrmAccount.Create(t.Context(), &acct); err != nil {
		t.Fatal(err)
	}
	mount := model.EmbyMount{
		Base:           model.Base{ID: "mount-1"},
		AccountID:      acct.ID,
		RemoteViewID:   "view-1",
		RemoteViewName: "动漫",
		CollectionType: "tvshows",
		Enabled:        true,
	}
	if err := repos.EmbyMount.Create(t.Context(), &mount); err != nil {
		t.Fatal(err)
	}

	crypto := service.NewCryptoService("", zap.NewNop())
	remoteSvc := service.NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, crypto)
	mediaSvc := service.NewMediaService(&config.Config{}, zap.NewNop(), repos)

	svc := &service.Container{
		Repo:       repos,
		Media:      mediaSvc,
		EmbyRemote: remoteSvc,
	}

	// 1. 搜索远程挂载媒体（碧蓝之海）
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(middleware.CtxUserID, "user-1")
		c.Set(middleware.CtxUserRole, "admin")
		c.Request = httptest.NewRequest(http.MethodGet, "/api/media?q=碧蓝之海&limit=8", nil)
		searchMediaHandler(svc)(c)

		if w.Code != http.StatusOK {
			t.Fatalf("search status=%d, body=%s", w.Code, w.Body.String())
		}
		var res struct {
			Items []service.MediaItem `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if len(res.Items) != 1 {
			t.Fatalf("expected 1 item, got %d", len(res.Items))
		}
		if res.Items[0].Title != "碧蓝之海" {
			t.Fatalf("expected Title '碧蓝之海', got %q", res.Items[0].Title)
		}
		expectedID := service.EncodeEmbyRemoteID("mount-1", "156030")
		if res.Items[0].ID != expectedID {
			t.Fatalf("expected ID %q, got %q", expectedID, res.Items[0].ID)
		}
	}

	// 2. 搜索本地媒体（流浪地球）
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(middleware.CtxUserID, "user-1")
		c.Set(middleware.CtxUserRole, "admin")
		c.Request = httptest.NewRequest(http.MethodGet, "/api/media?q=流浪地球&limit=8", nil)
		searchMediaHandler(svc)(c)

		if w.Code != http.StatusOK {
			t.Fatalf("search status=%d, body=%s", w.Code, w.Body.String())
		}
		var res struct {
			Items []service.MediaItem `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if len(res.Items) != 1 {
			t.Fatalf("expected 1 item, got %d", len(res.Items))
		}
		if res.Items[0].Title != "流浪地球" {
			t.Fatalf("expected Title '流浪地球', got %q", res.Items[0].Title)
		}
	}

	// 3. 搜索不存在的媒体
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(middleware.CtxUserID, "user-1")
		c.Set(middleware.CtxUserRole, "admin")
		c.Request = httptest.NewRequest(http.MethodGet, "/api/media?q=不存在的影片&limit=8", nil)
		searchMediaHandler(svc)(c)

		if w.Code != http.StatusOK {
			t.Fatalf("search status=%d, body=%s", w.Code, w.Body.String())
		}
		var res struct {
			Items []service.MediaItem `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if len(res.Items) != 0 {
			t.Fatalf("expected 0 items, got %d", len(res.Items))
		}
		if strings.Contains(w.Body.String(), `"items":null`) {
			t.Fatalf("expected items:[], got null: %s", w.Body.String())
		}
	}
}
