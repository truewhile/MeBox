package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	for _, m := range []model.Media{
		{
			Base:       model.Base{ID: "s1e1"},
			LibraryID:  lib.ID,
			Title:      "Test Show",
			Path:       "D:\\media\\tv\\Test Show\\Season 01\\Test Show - S01E01.mkv",
			SeasonNum:  1,
			EpisodeNum: 1,
			Container:  "mkv",
		},
		{
			Base:       model.Base{ID: "s2e1"},
			LibraryID:  lib.ID,
			Title:      "Test Show",
			Path:       "D:\\media\\tv\\Test Show\\Season 02\\Test Show - S02E01.mkv",
			SeasonNum:  2,
			EpisodeNum: 1,
			Container:  "mkv",
		},
	} {
		if err := db.Create(&m).Error; err != nil {
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

func fetchEpisodeIDs(t *testing.T, router *gin.Engine, secret, path string) ([]string, int) {
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
	ids := make([]string, 0, len(payload.Items))
	for _, item := range payload.Items {
		id, _ := item["Id"].(string)
		ids = append(ids, id)
	}
	return ids, int(payload.TotalRecordCount)
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

	seasonsPath := "/Shows/" + seriesID + "/Seasons"
	req := httptest.NewRequest(http.MethodGet, seasonsPath, nil)
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
	var season1ID string
	for _, s := range seasons.Items {
		if index, ok := s["IndexNumber"].(float64); ok && int(index) == 1 {
			season1ID, _ = s["Id"].(string)
		}
	}
	if season1ID == "" {
		t.Fatalf("season 1 not found in %#v", seasons.Items)
	}

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
