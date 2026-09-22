package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

func newHistoryStatsEnv(t *testing.T) (*gin.Engine, *service.Container, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Library{}, &model.Media{}, &model.PlaybackHistory{}, &model.UserPermission{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	svc := &service.Container{Repo: repos, Log: zap.NewNop()}
	svc.Permissions = service.NewPermissionService(zap.NewNop(), repos)

	const userID = "user-1"
	if err := repos.User.Create(context.Background(), &model.User{
		Base: model.Base{ID: userID}, Username: "tester", PasswordHash: "x", Role: "user", IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	authed := router.Group("/api", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, userID)
		c.Next()
	})
	authed.GET("/watch-history/stats", historyStatsHandler(svc))
	return router, svc, userID
}

type historyStatsPayload struct {
	Total         int64 `json:"total"`
	Completed     int64 `json:"completed"`
	InProgress    int64 `json:"in_progress"`
	WatchedMs     int64 `json:"watched_ms"`
	WatchedHours  float64 `json:"watched_hours"`
	Daily         []struct {
		Day     string `json:"day"`
		WatchMs int64  `json:"watch_ms"`
		Plays   int64  `json:"plays"`
	} `json:"daily"`
	ByLibraryType []struct {
		Type    string `json:"type"`
		WatchMs int64  `json:"watch_ms"`
		Count   int64  `json:"count"`
	} `json:"by_library_type"`
	Recent []struct {
		Media *model.Media `json:"media"`
	} `json:"recent"`
}

func fetchHistoryStats(t *testing.T, router *gin.Engine) historyStatsPayload {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/watch-history/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var payload historyStatsPayload
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return payload
}

// 新字段必须提供每日聚合、库类型分布与在看数量，供个人统计页绘图。
func TestHistoryStatsIncludesDailyAndTypes(t *testing.T) {
	router, svc, userID := newHistoryStatsEnv(t)
	ctx := context.Background()

	movieLib := &model.Library{Name: "电影", Path: "/media/movies", Type: "movie", Enabled: true}
	if err := svc.Repo.Library.Create(ctx, movieLib); err != nil {
		t.Fatal(err)
	}
	tvLib := &model.Library{Name: "剧集", Path: "/media/tv", Type: "tv", Enabled: true}
	if err := svc.Repo.Library.Create(ctx, tvLib); err != nil {
		t.Fatal(err)
	}

	yesterday := time.Now().Add(-24 * time.Hour)
	today := time.Now().Add(-time.Hour)

	rows := []struct {
		media     *model.Media
		watchedAt time.Time
		position  int64
		completed bool
	}{
		{
			media:     &model.Media{LibraryID: movieLib.ID, Title: "电影A", Path: "/media/movies/a.mkv"},
			watchedAt: yesterday, position: 60000, completed: true,
		},
		{
			media:     &model.Media{LibraryID: tvLib.ID, Title: "剧B", Path: "/media/tv/b.mkv"},
			watchedAt: today, position: 30000, completed: false,
		},
	}
	for _, row := range rows {
		if err := svc.Repo.DB.Create(row.media).Error; err != nil {
			t.Fatal(err)
		}
		h := &model.PlaybackHistory{
			UserID: userID, MediaID: row.media.ID, PositionMs: row.position,
			DurationMs: 120000, WatchedAt: row.watchedAt, Completed: row.completed,
		}
		if err := svc.Repo.DB.Create(h).Error; err != nil {
			t.Fatal(err)
		}
	}

	payload := fetchHistoryStats(t, router)

	if payload.Total != 2 {
		t.Fatalf("total = %d, want 2", payload.Total)
	}
	if payload.Completed != 1 {
		t.Fatalf("completed = %d, want 1", payload.Completed)
	}
	if payload.InProgress != 1 {
		t.Fatalf("in_progress = %d, want 1", payload.InProgress)
	}
	if payload.WatchedMs != 90000 {
		t.Fatalf("watched_ms = %d, want 90000", payload.WatchedMs)
	}
	if len(payload.Daily) != 2 {
		t.Fatalf("daily = %+v, want 2 days", payload.Daily)
	}
	if len(payload.ByLibraryType) != 2 {
		t.Fatalf("by_library_type = %+v, want 2 entries", payload.ByLibraryType)
	}
	if len(payload.Recent) != 2 {
		t.Fatalf("recent = %d entries, want 2", len(payload.Recent))
	}
	if payload.Recent[0].Media == nil || payload.Recent[0].Media.Title != "剧B" {
		t.Fatalf("recent[0] = %+v, want the most recent entry (剧B)", payload.Recent[0])
	}
}

// 没有任何播放记录时，新字段要返回空数组而不是 null，前端无需额外判空。
func TestHistoryStatsEmptyProvidesEmptyArrays(t *testing.T) {
	router, _, _ := newHistoryStatsEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/watch-history/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	body := w.Body.String()
	for _, field := range []string{`"daily":[]`, `"by_library_type":[]`, `"recent":[]`} {
		if !containsSubstring(body, field) {
			t.Fatalf("body = %s, want %s", body, field)
		}
	}
}

func containsSubstring(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestHistoryStatsBreakdownsVisibilityFilter validates that historyStatsBreakdowns
// respects the caller's MediaVisibility: media in a hidden library must be absent
// from both the recent list and the by_library_type buckets.
func TestHistoryStatsBreakdownsVisibilityFilter(t *testing.T) {
	_, svc, userID := newHistoryStatsEnv(t)
	ctx := context.Background()

	allowedLib := &model.Library{Name: "允许库", Path: "/media/allowed", Type: "movie", Enabled: true}
	hiddenLib := &model.Library{Name: "隐藏库", Path: "/media/hidden", Type: "tv", Enabled: true}
	if err := svc.Repo.Library.Create(ctx, allowedLib); err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo.Library.Create(ctx, hiddenLib); err != nil {
		t.Fatal(err)
	}

	allowedMedia := &model.Media{LibraryID: allowedLib.ID, Title: "允许媒体", Path: "/media/allowed/a.mkv"}
	hiddenMedia := &model.Media{LibraryID: hiddenLib.ID, Title: "隐藏媒体", Path: "/media/hidden/b.mkv"}
	if err := svc.Repo.DB.Create(allowedMedia).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo.DB.Create(hiddenMedia).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	for _, mid := range []string{allowedMedia.ID, hiddenMedia.ID} {
		h := &model.PlaybackHistory{
			UserID: userID, MediaID: mid, PositionMs: 10000,
			DurationMs: 100000, WatchedAt: now, Completed: false,
		}
		if err := svc.Repo.DB.Create(h).Error; err != nil {
			t.Fatal(err)
		}
	}

	// visibility that hides hiddenLib
	vis := service.MediaVisibility{
		HiddenLibraryIDs: []string{hiddenLib.ID},
	}

	_, byType, recent := historyStatsBreakdowns(ctx, svc, userID, vis)

	// recent must contain only the allowed media
	for _, entry := range recent {
		m, ok := entry["media"]
		if !ok {
			t.Fatal("recent entry missing media field")
		}
		switch med := m.(type) {
		case *model.Media:
			if med.LibraryID == hiddenLib.ID {
				t.Fatalf("hidden media appeared in recent: %s", med.Title)
			}
		case model.Media:
			if med.LibraryID == hiddenLib.ID {
				t.Fatalf("hidden media appeared in recent: %s", med.Title)
			}
		}
	}
	if len(recent) != 1 {
		t.Fatalf("recent length = %d, want 1 (hidden entry must be excluded)", len(recent))
	}

	// by_library_type must not contain the hidden library's type ("tv")
	for _, bt := range byType {
		if bt.Type == "tv" {
			t.Fatalf("hidden library type 'tv' appeared in by_library_type (count=%d)", bt.Count)
		}
	}
}

// TestHistoryStatsBreakdownsNilMediaCountsAsOther confirms that a history row
// whose media has been deleted (nil lookup) is counted under the "other" type
// bucket rather than silently dropped.
func TestHistoryStatsBreakdownsNilMediaCountsAsOther(t *testing.T) {
	_, svc, userID := newHistoryStatsEnv(t)
	ctx := context.Background()

	// Insert a history row whose media_id does not correspond to any Media row.
	ghost := &model.PlaybackHistory{
		UserID:     userID,
		MediaID:    "ghost-media-id",
		PositionMs: 5000,
		DurationMs: 50000,
		WatchedAt:  time.Now(),
		Completed:  false,
	}
	if err := svc.Repo.DB.Create(ghost).Error; err != nil {
		t.Fatal(err)
	}

	vis := service.MediaVisibility{} // unrestricted
	_, byType, _ := historyStatsBreakdowns(ctx, svc, userID, vis)

	var otherEntry *historyTypeStat
	for i := range byType {
		if byType[i].Type == "other" {
			otherEntry = &byType[i]
			break
		}
	}
	if otherEntry == nil {
		t.Fatalf("expected 'other' bucket for nil-media history row, got %+v", byType)
	}
	if otherEntry.Count != 1 {
		t.Fatalf("other.Count = %d, want 1", otherEntry.Count)
	}
}

// TestHistoryStatsPermissionDeny checks that a user without can_view_history
// receives HTTP 403 from the gated route.
func TestHistoryStatsPermissionDeny(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Library{}, &model.Media{},
		&model.PlaybackHistory{}, &model.UserPermission{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	svc := &service.Container{Repo: repos, Log: zap.NewNop()}
	svc.Permissions = service.NewPermissionService(zap.NewNop(), repos)

	const userID = "user-noperm"
	if err := repos.User.Create(context.Background(), &model.User{
		Base: model.Base{ID: userID}, Username: "noperm", PasswordHash: "x", Role: "user", IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}

	// Explicitly deny can_view_history for this user.
	// First seed defaults (Effective will create the row with defaults), then
	// update to deny via Save which uses an explicit map update path in the repo.
	if _, err := svc.Permissions.Effective(context.Background(), userID); err != nil {
		t.Fatalf("seed permissions: %v", err)
	}
	denyPerm := &model.UserPermission{UserID: userID, CanViewHistory: false}
	if err := svc.Permissions.Save(context.Background(), userID, denyPerm); err != nil {
		t.Fatalf("save permission: %v", err)
	}

	router := gin.New()
	authed := router.Group("/api", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, userID)
		c.Set(middleware.CtxUserRole, "user")
		c.Next()
	})
	authed.GET("/watch-history/stats", requirePermission(svc, "can_view_history"), historyStatsHandler(svc))

	req := httptest.NewRequest(http.MethodGet, "/api/watch-history/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for user without can_view_history", w.Code)
	}
}
