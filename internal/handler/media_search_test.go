package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

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

func TestSearchMediaSeriesKeepsRemoteLibrariesSeparate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"TotalRecordCount": 1,
			"Items": []map[string]any{{
				"Id": "same-series", "Name": "龙珠改", "Type": "Series",
				"ProviderIds":    map[string]string{"Tmdb": "61709"},
				"ProductionYear": 2024, "Genres": []string{"Science Fiction"},
			}},
		})
	}))
	defer upstream.Close()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}, &model.StrmAccount{}, &model.EmbyMount{}, &model.Setting{}, &model.User{}, &model.PlayProfile{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	lib := model.Library{Name: "本地番剧", Path: "/media/anime", Type: "anime", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	rows := []model.Media{
		{Base: model.Base{ID: "local-1"}, LibraryID: lib.ID, Title: "龙珠改", Path: "/media/anime/龙珠改/Season 1/ep1.mkv", SeasonNum: 1, EpisodeNum: 1, TMDbID: 61709},
		{Base: model.Base{ID: "local-2"}, LibraryID: lib.ID, Title: "龙珠改", Path: "/media/anime/龙珠改/Season 1/ep2.mkv", SeasonNum: 1, EpisodeNum: 2, TMDbID: 61709},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	rawConfig, _ := json.Marshal(map[string]string{"url": upstream.URL, "token": "fake-token"})
	acct := model.StrmAccount{Base: model.Base{ID: "account"}, Name: "Emby", Provider: model.StrmProviderEmbyRemote, Config: string(rawConfig), Enabled: true}
	if err := repos.StrmAccount.Create(t.Context(), &acct); err != nil {
		t.Fatal(err)
	}
	expectedIDs := map[string]string{lib.ID: "local-1"}
	for _, suffix := range []string{"a", "b"} {
		mount := model.EmbyMount{Base: model.Base{ID: "mount-" + suffix}, AccountID: acct.ID, RemoteViewID: "view-" + suffix, RemoteViewName: "番剧" + suffix, CollectionType: "tvshows", Enabled: true}
		if err := repos.EmbyMount.Create(t.Context(), &mount); err != nil {
			t.Fatal(err)
		}
		expectedIDs[service.EncodeEmbyRemoteID(mount.ID, mount.RemoteViewID)] = service.EncodeEmbyRemoteID(mount.ID, "same-series")
	}
	svc := &service.Container{
		Repo:       repos,
		Media:      service.NewMediaService(&config.Config{}, zap.NewNop(), repos),
		EmbyRemote: service.NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, service.NewCryptoService("", zap.NewNop())),
	}

	request := func(params string) ([]model.Media, int) {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(middleware.CtxUserID, "admin")
		c.Set(middleware.CtxUserRole, "admin")
		c.Request = httptest.NewRequest(http.MethodGet, "/api/media?q=龙珠改&group_series=1&"+params, nil)
		searchMediaHandler(svc)(c)
		if w.Code != http.StatusOK {
			t.Fatalf("search status=%d, body=%s", w.Code, w.Body.String())
		}
		var response struct {
			Items []model.Media `json:"items"`
			Total int           `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Items, response.Total
	}

	items, _ := request("limit=8")
	if len(items) != len(expectedIDs) {
		t.Fatalf("got %d results, want one local series and two remote series: %#v", len(items), items)
	}
	seen := make(map[string]bool)
	for page := 1; page <= len(expectedIDs); page++ {
		items, total := request(fmt.Sprintf("page=%d&page_size=1", page))
		if total != len(expectedIDs) || len(items) != 1 {
			t.Fatalf("page %d: total=%d items=%#v, want total=3 and one item", page, total, items)
		}
		item := items[0]
		if expectedIDs[item.DisplayLibraryID] != item.ID || seen[item.DisplayLibraryID] || item.DisplayLibraryName == "" {
			t.Fatalf("page %d lost or repeated a library's series identity: %#v", page, item)
		}
		seen[item.DisplayLibraryID] = true
	}
	remoteLibraryID := service.EncodeEmbyRemoteID("mount-a", "view-a")
	items, total := request("page=1&page_size=8&library_id=" + url.QueryEscape(remoteLibraryID) + "&year_min=2020&genre=Science+Fiction")
	if total != 1 || len(items) != 1 || items[0].DisplayLibraryID != remoteLibraryID {
		t.Fatalf("remote library/year/genre filters lost results: total=%d items=%#v", total, items)
	}
}
