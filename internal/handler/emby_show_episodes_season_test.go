package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

// embySeasonEpisodesRouter builds an Emby-compatible router over one series with
// two seasons, so /Shows/{id}/Episodes can be exercised with the query forms
// real clients send.
func embySeasonEpisodesRouter(t *testing.T) (*gin.Engine, string, string) {
	t.Helper()
	return embySeriesRouter(t, []model.Media{
		{
			Base:       model.Base{ID: "s1e1"},
			Title:      "Test Show",
			Path:       "D:\\media\\tv\\Test Show\\Season 01\\Test Show - S01E01.mkv",
			SeasonNum:  1,
			EpisodeNum: 1,
			Container:  "mkv",
		},
		{
			Base:       model.Base{ID: "s2e1"},
			Title:      "Test Show",
			Path:       "D:\\media\\tv\\Test Show\\Season 02\\Test Show - S02E01.mkv",
			SeasonNum:  2,
			EpisodeNum: 1,
			Container:  "mkv",
		},
	})
}

// embySeriesRouter builds an Emby-compatible router with a single tv library and
// one series made of the given episode rows. Callers omit LibraryID: it is filled
// in here once the library exists.
func embySeriesRouter(t *testing.T, rows []model.Media) (*gin.Engine, string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Library{}, &model.Series{}, &model.Media{}, &model.Favorite{}, &model.PlaybackHistory{}, &model.Setting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	repos := repository.New(db)
	if err := repos.User.Create(t.Context(), &model.User{
		Base:         model.Base{ID: "user-1"},
		Username:     "tester",
		PasswordHash: "x",
		Role:         "admin",
		Tier:         "plus",
		IsActive:     true,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	lib := model.Library{Name: "剧集", Path: "D:\\media\\tv", Type: "tv", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	for i := range rows {
		rows[i].LibraryID = lib.ID
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("create media: %v", err)
		}
	}

	const secret = "test-secret"
	router := gin.New()
	registerEmbyRoutes(router, secret, &service.Container{
		Repo: repos,
		Emby: service.NewEmbyService(&config.Config{}, zap.NewNop(), repos),
	})

	// Resolve the virtual series id the way a client would.
	seriesReq := httptest.NewRequest(http.MethodGet, "/Items?ParentId="+lib.ID+"&IncludeItemTypes=Series", nil)
	seriesReq.Header.Set("X-Emby-Token", signedTestToken(t, secret))
	seriesRec := httptest.NewRecorder()
	router.ServeHTTP(seriesRec, seriesReq)
	if seriesRec.Code != http.StatusOK {
		t.Fatalf("series lookup status=%d body=%s", seriesRec.Code, seriesRec.Body.String())
	}
	var seriesPayload struct {
		Items []map[string]any `json:"Items"`
	}
	if err := json.Unmarshal(seriesRec.Body.Bytes(), &seriesPayload); err != nil {
		t.Fatalf("decode series: %v", err)
	}
	if len(seriesPayload.Items) != 1 {
		t.Fatalf("want one series, got %#v", seriesPayload.Items)
	}
	seriesID, _ := seriesPayload.Items[0]["Id"].(string)
	if seriesID == "" {
		t.Fatalf("series payload has no Id: %#v", seriesPayload.Items[0])
	}
	return router, secret, seriesID
}

func fetchSeasonID(t *testing.T, router *gin.Engine, secret, seriesID string, index int) string {
	t.Helper()
	path := "/Shows/" + seriesID + "/Seasons"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Emby-Token", signedTestToken(t, secret))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("seasons status=%d body=%s", rec.Code, rec.Body.String())
	}
	var seasons struct {
		Items []map[string]any `json:"Items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &seasons); err != nil {
		t.Fatalf("decode seasons: %v", err)
	}
	for _, s := range seasons.Items {
		if got, ok := s["IndexNumber"].(float64); ok && int(got) == index {
			if id, _ := s["Id"].(string); id != "" {
				return id
			}
		}
	}
	t.Fatalf("season %d not found in %#v", index, seasons.Items)
	return ""
}

func fetchEpisodeItems(t *testing.T, router *gin.Engine, secret, path string) ([]map[string]any, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Emby-Token", signedTestToken(t, secret))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
	}
	var payload struct {
		Items            []map[string]any `json:"Items"`
		TotalRecordCount float64          `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return payload.Items, int(payload.TotalRecordCount)
}

func fetchEpisodeIDs(t *testing.T, router *gin.Engine, secret, path string) ([]string, int) {
	t.Helper()
	items, total := fetchEpisodeItems(t, router, secret, path)
	ids := make([]string, 0, len(items))
	for _, item := range items {
		id, _ := item["Id"].(string)
		ids = append(ids, id)
	}
	return ids, total
}

// TestEmbyShowEpisodesHonoursSeasonQueryParam is the client-facing regression
// test: Emby clients scope episodes with ?Season=<index> (not only SeasonId), and
// the endpoint used to ignore it and return every season of the series.
func TestEmbyShowEpisodesHonoursSeasonQueryParam(t *testing.T) {
	router, secret, seriesID := embySeasonEpisodesRouter(t)

	cases := []struct {
		name    string
		query   string
		wantIDs []string
	}{
		{name: "season 1", query: "Season=1", wantIDs: []string{"s1e1"}},
		{name: "season 2", query: "Season=2", wantIDs: []string{"s2e1"}},
		{name: "season index alias", query: "SeasonIndex=2", wantIDs: []string{"s2e1"}},
		{name: "no season returns all", query: "", wantIDs: []string{"s1e1", "s2e1"}},
		{name: "unknown season is empty", query: "Season=9", wantIDs: []string{}},
		{name: "non numeric season falls back to all", query: "Season=Specials", wantIDs: []string{"s1e1", "s2e1"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "/Shows/" + seriesID + "/Episodes"
			if tc.query != "" {
				path += "?" + tc.query
			}
			ids, total := fetchEpisodeIDs(t, router, secret, path)
			if len(ids) != len(tc.wantIDs) {
				t.Fatalf("episode ids = %#v (total=%d), want %#v", ids, total, tc.wantIDs)
			}
			for i := range tc.wantIDs {
				if ids[i] != tc.wantIDs[i] {
					t.Fatalf("episode ids = %#v, want %#v", ids, tc.wantIDs)
				}
			}
			if total != len(tc.wantIDs) {
				t.Fatalf("TotalRecordCount = %d, want %d", total, len(tc.wantIDs))
			}
		})
	}
}

// TestEmbyShowEpisodesSeasonIdStillWins pins that the virtual SeasonId form keeps
// working, and that a conflicting Season query cannot empty it out.
func TestEmbyShowEpisodesSeasonIdStillWins(t *testing.T) {
	router, secret, seriesID := embySeasonEpisodesRouter(t)

	season1ID := fetchSeasonID(t, router, secret, seriesID, 1)

	ids, _ := fetchEpisodeIDs(t, router, secret, "/Shows/"+seriesID+"/Episodes?SeasonId="+season1ID)
	if len(ids) != 1 || ids[0] != "s1e1" {
		t.Fatalf("SeasonId episodes = %#v, want [s1e1]", ids)
	}

	// A stale/conflicting season number must not override the explicit season id.
	ids, _ = fetchEpisodeIDs(t, router, secret, "/Shows/"+seriesID+"/Episodes?SeasonId="+season1ID+"&Season=2")
	if len(ids) != 1 || ids[0] != "s1e1" {
		t.Fatalf("SeasonId+Season episodes = %#v, want [s1e1]", ids)
	}
}

// foldedDuplicateSeriesRows models a season where two files resolve to the same
// episode number — 3月的狮子 S01E11 together with S01E11.5, which the episode
// parser reads as S01E11 and stores as a second row for season 1 episode 2 here.
func foldedDuplicateSeriesRows() []model.Media {
	return []model.Media{
		{
			Base:       model.Base{ID: "e1"},
			Title:      "三月的狮子",
			Path:       "D:\\media\\tv\\三月的狮子\\三月的狮子 - S01E01.mkv",
			SeasonNum:  1,
			EpisodeNum: 1,
			Container:  "mkv",
		},
		{
			Base:       model.Base{ID: "e2"},
			Title:      "三月的狮子",
			Path:       "D:\\media\\tv\\三月的狮子\\三月的狮子 - S01E02.mkv",
			SeasonNum:  1,
			EpisodeNum: 2,
			Container:  "mkv",
		},
		{
			Base:       model.Base{ID: "e2half"},
			Title:      "三月的狮子",
			Path:       "D:\\media\\tv\\三月的狮子\\三月的狮子 - S01E02.5.mkv",
			SeasonNum:  1,
			EpisodeNum: 2,
			Container:  "mkv",
		},
	}
}

// TestEmbyShowEpisodesFoldedDuplicateKeepsTotalsConsistent is the regression test
// for the "第一季分集加载不出来" lock-up: two rows sharing one season/episode are
// folded into a single Emby item with two MediaSources. TotalRecordCount used to
// be computed before that fold, so clients that page until they have
// TotalRecordCount items kept re-requesting the same page and never rendered the
// season.
func TestEmbyShowEpisodesFoldedDuplicateKeepsTotalsConsistent(t *testing.T) {
	router, secret, seriesID := embySeriesRouter(t, foldedDuplicateSeriesRows())
	seasonID := fetchSeasonID(t, router, secret, seriesID, 1)

	items, total := fetchEpisodeItems(t, router, secret, "/Shows/"+seriesID+"/Episodes?SeasonId="+seasonID)
	if total != len(items) {
		t.Fatalf("TotalRecordCount = %d but %d items returned, want them equal", total, len(items))
	}
	if total != 2 {
		t.Fatalf("TotalRecordCount = %d, want 2 distinct episodes", total)
	}
	// The folded episode must still carry both files so clients can switch version.
	versioned := 0
	for _, item := range items {
		if sources, ok := item["MediaSources"].([]any); ok && len(sources) > 1 {
			versioned++
		}
	}
	if versioned != 1 {
		t.Fatalf("items with multiple MediaSources = %d, want 1 (%#v)", versioned, items)
	}
}

// TestEmbyShowEpisodesPagingAdvances pins that the client's StartIndex/Limit reach
// the episode list. Ignoring them made every page identical, so a client paging
// until it has TotalRecordCount items could never finish loading the season.
func TestEmbyShowEpisodesPagingAdvances(t *testing.T) {
	router, secret, seriesID := embySeriesRouter(t, foldedDuplicateSeriesRows())
	base := "/Shows/" + seriesID + "/Episodes?Season=1"

	first, total := fetchEpisodeIDs(t, router, secret, base+"&StartIndex=0&Limit=1")
	if total != 2 || len(first) != 1 {
		t.Fatalf("page 1 = %#v (total=%d), want one item of two", first, total)
	}
	second, _ := fetchEpisodeIDs(t, router, secret, base+"&StartIndex=1&Limit=1")
	if len(second) != 1 || second[0] == first[0] {
		t.Fatalf("page 2 = %#v, want the remaining episode (page 1 = %#v)", second, first)
	}
	third, _ := fetchEpisodeIDs(t, router, secret, base+"&StartIndex=2&Limit=1")
	if len(third) != 0 {
		t.Fatalf("page 3 = %#v, want an empty page so clients stop paging", third)
	}
}

// 半集（S01E11.5）在 Emby 侧必须和第 11 集并列返回：一旦被折成一个条目，
// TotalRecordCount 就会比 Items 多一条，按总数翻页的客户端会一直重发同一页
// （这就是「第一季分集加载不出来」的根因）。Emby 的 IndexNumber 只能是整数，
// 所以集号仍占 11，小数通过条目名体现。
func TestEmbyShowEpisodesKeepsHalfEpisodeVisible(t *testing.T) {
	router, secret, seriesID := embySeriesRouter(t, []model.Media{
		{
			Base:       model.Base{ID: "e11"},
			Title:      "三月的狮子",
			Path:       "D:\\media\\tv\\三月的狮子\\三月的狮子 - S01E11.mkv",
			SeasonNum:  1,
			EpisodeNum: 11,
			Container:  "mkv",
		},
		{
			Base:            model.Base{ID: "e11half"},
			Title:           "三月的狮子",
			Path:            "D:\\media\\tv\\三月的狮子\\三月的狮子 - S01E11.5.mkv",
			SeasonNum:       1,
			EpisodeNum:      11,
			EpisodeFraction: 0.5,
			Container:       "mkv",
		},
		{
			Base:       model.Base{ID: "e12"},
			Title:      "三月的狮子",
			Path:       "D:\\media\\tv\\三月的狮子\\三月的狮子 - S01E12.mkv",
			SeasonNum:  1,
			EpisodeNum: 12,
			Container:  "mkv",
		},
	})

	items, total := fetchEpisodeItems(t, router, secret, "/Shows/"+seriesID+"/Episodes?Season=1")
	if total != 3 || len(items) != 3 {
		t.Fatalf("TotalRecordCount = %d, items = %d, want 3/3", total, len(items))
	}
	wantOrder := []string{"e11", "e11half", "e12"}
	for i, want := range wantOrder {
		id, _ := items[i]["Id"].(string)
		if id != want {
			t.Fatalf("items[%d] = %q, want %q (order %#v)", i, id, want, items)
		}
	}
	for _, item := range items {
		sources, _ := item["MediaSources"].([]any)
		if len(sources) != 1 {
			t.Fatalf("item %v folded %d sources, want 1", item["Id"], len(sources))
		}
	}

	halfName, _ := items[1]["Name"].(string)
	if !strings.HasPrefix(halfName, "第 11.5 集") {
		t.Fatalf("half episode name = %q, want prefix 第 11.5 集", halfName)
	}
	if idx, _ := items[1]["IndexNumber"].(float64); int(idx) != 11 {
		t.Fatalf("half episode IndexNumber = %v, want 11 (Emby only supports integers)", items[1]["IndexNumber"])
	}
}
