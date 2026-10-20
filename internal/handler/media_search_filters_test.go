package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestSearchMediaSortLimitFiltersAndDeepPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}, &model.User{}, &model.Setting{}, &model.PlayProfile{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	lib := model.Library{Name: "Review", Path: "/review", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	rows := make([]model.Media, 2001)
	for i := range rows {
		rows[i] = model.Media{
			Base: model.Base{ID: fmt.Sprintf("review-%04d", i)}, LibraryID: lib.ID,
			Title: fmt.Sprintf("Review %04d", i), Path: fmt.Sprintf("/review/%04d.mp4", i), Year: 2020, Genres: "Comedy",
		}
	}
	rows[2000].Year = 2025
	rows[2000].Genres = "Drama"
	if err := db.CreateInBatches(rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	svc := &service.Container{Repo: repos, Media: service.NewMediaService(&config.Config{}, zap.NewNop(), repos)}
	cases := []struct {
		name, query, expectedID  string
		count, total, page, size int
	}{
		{name: "sorted output limit", query: "sort=year_desc&limit=1", expectedID: "review-2000", count: 1},
		{name: "page past old 2000 cap", query: "sort=title_asc&page=2001&page_size=1", expectedID: "review-2000", count: 1, total: 2001, page: 2001, size: 1},
		{name: "genre filtering", query: "genre=Drama&library_id=" + lib.ID, expectedID: "review-2000", count: 1},
		{name: "normalized page", query: "page=0&page_size=0", count: 50, total: 2001, page: 1, size: 50},
		{name: "bounded page size", query: "page=1&page_size=99999", count: 2000, total: 2001, page: 1, size: 2000},
		{name: "huge page does not overflow", query: "page=" + strconv.Itoa(int(^uint(0)>>1)) + "&page_size=2000", count: 0, total: 2001, page: int(^uint(0) >> 1), size: 2000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)
			ctx.Set(middleware.CtxUserID, "admin")
			ctx.Set(middleware.CtxUserRole, "admin")
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/media?q=Review&group_versions=0&"+tc.query, nil)
			searchMediaHandler(svc)(ctx)
			var response struct {
				Items []model.Media `json:"items"`
				Total int           `json:"total"`
				Page  int           `json:"page"`
				Size  int           `json:"page_size"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil {
				t.Fatalf("search failed: status=%d body=%s", w.Code, w.Body.String())
			}
			if len(response.Items) != tc.count || response.Total != tc.total || response.Page != tc.page || response.Size != tc.size {
				t.Fatalf("response count=%d total=%d page=%d size=%d, want %#v", len(response.Items), response.Total, response.Page, response.Size, tc)
			}
			if tc.expectedID != "" && response.Items[0].ID != tc.expectedID {
				t.Fatalf("first ID=%q want %q", response.Items[0].ID, tc.expectedID)
			}
		})
	}
}
