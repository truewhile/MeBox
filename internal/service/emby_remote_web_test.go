package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestMapRemoteItemToMediaSortingFields(t *testing.T) {
	svc := &EmbyRemoteService{}
	mount := &model.EmbyMount{Base: model.Base{ID: "mount-1"}}
	acct := &model.StrmAccount{Base: model.Base{ID: "acct-1"}}
	cfg := &EmbyRemoteConfig{BaseURL: "http://localhost:8096"}

	item := map[string]any{
		"Id":              "item-1",
		"Name":            "测试电影",
		"OriginalTitle":   "Test Movie",
		"ProductionYear":  2023,
		"CommunityRating": 8.5,
		"PremiereDate":    "2023-05-12T00:00:00.0000000Z",
		"DateCreated":     "2024-01-15T08:30:00.0000000Z",
	}

	media := svc.MapRemoteItemToMedia(context.Background(), mount, acct, cfg, item)

	if media.ReleaseDate != "2023-05-12" {
		t.Fatalf("ReleaseDate = %q, want %q", media.ReleaseDate, "2023-05-12")
	}
	if media.Year != 2023 {
		t.Fatalf("Year = %d, want 2023", media.Year)
	}
	if media.Rating != 8.5 {
		t.Fatalf("Rating = %f, want 8.5", media.Rating)
	}
	expectedCreated, _ := time.Parse(time.RFC3339, "2024-01-15T08:30:00Z")
	if !media.CreatedAt.Equal(expectedCreated) {
		t.Fatalf("CreatedAt = %v, want %v", media.CreatedAt, expectedCreated)
	}
	if !media.UpdatedAt.Equal(expectedCreated) {
		t.Fatalf("UpdatedAt = %v, want %v", media.UpdatedAt, expectedCreated)
	}
}

func TestMapRemoteItemToMediaCriticRatingFallback(t *testing.T) {
	svc := &EmbyRemoteService{}
	mount := &model.EmbyMount{Base: model.Base{ID: "mount-1"}}
	acct := &model.StrmAccount{Base: model.Base{ID: "acct-1"}}
	cfg := &EmbyRemoteConfig{BaseURL: "http://localhost:8096"}

	item := map[string]any{
		"Id":           "item-2",
		"Name":         "评分测试",
		"CriticRating": 9.2,
		"PremiereDate": "2022-10-01",
	}

	media := svc.MapRemoteItemToMedia(context.Background(), mount, acct, cfg, item)
	if media.Rating != 9.2 {
		t.Fatalf("Rating = %f, want 9.2 from CriticRating", media.Rating)
	}
	if media.Year != 2022 {
		t.Fatalf("Year = %d, want 2022 from PremiereDate", media.Year)
	}
}

func TestMapRemoteItemToMediaDateLastMediaAdded(t *testing.T) {
	svc := &EmbyRemoteService{}
	mount := &model.EmbyMount{Base: model.Base{ID: "mount-1"}}
	acct := &model.StrmAccount{Base: model.Base{ID: "acct-1"}}
	cfg := &EmbyRemoteConfig{BaseURL: "http://localhost:8096"}

	item := map[string]any{
		"Id":                 "series-1",
		"Name":               "测试剧集",
		"DateCreated":        "2023-01-01T00:00:00.0000000Z",
		"DateLastMediaAdded": "2024-05-20T10:00:00.0000000Z",
	}

	media := svc.MapRemoteItemToMedia(context.Background(), mount, acct, cfg, item)
	expectedCreated, _ := time.Parse(time.RFC3339, "2023-01-01T00:00:00Z")
	expectedLastAdded, _ := time.Parse(time.RFC3339, "2024-05-20T10:00:00Z")
	if !media.CreatedAt.Equal(expectedCreated) {
		t.Fatalf("CreatedAt = %v, want %v", media.CreatedAt, expectedCreated)
	}
	if !media.UpdatedAt.Equal(expectedLastAdded) {
		t.Fatalf("UpdatedAt = %v, want %v", media.UpdatedAt, expectedLastAdded)
	}
}

func TestRemoteSeriesCardsAutoAuthOnFirstBrowse(t *testing.T) {
	// 模拟远程 Emby：未认证兜底用户 ID "0" 被拒绝（与真实服务器一致），
	// 只有认证拿到的真实用户 GUID 才能浏览。
	var zeroUserHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/AuthenticateByName") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "real-token",
				"User":        map[string]any{"Id": "real-user-guid"},
			})
			return
		}
		if r.URL.Path == "/emby/Users/0/Items" {
			zeroUserHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("Unrecognized Guid format."))
			return
		}
		if r.URL.Path == "/emby/Users/real-user-guid/Items" {
			q := r.URL.Query()
			if q.Get("ParentId") != "view-1" || q.Get("IncludeItemTypes") != "Series" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"TotalRecordCount": 1,
				"Items": []map[string]any{
					{
						"Id":                 "series-100",
						"Name":               "测试剧",
						"Type":               "Series",
						"ProductionYear":     2024,
						"RecursiveItemCount": 12,
						"ChildCount":         2,
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{})
	repos := repository.New(db)
	svc := NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, NewCryptoService("", zap.NewNop()))

	// 账号只配置了用户名/密码，从未「测试连接」：无 api_key、无 remote_user_id。
	rawConfig, _ := json.Marshal(map[string]string{
		"url":      server.URL,
		"username": "user",
		"password": "pass",
	})
	acct := &model.StrmAccount{
		Base:     model.Base{ID: "acct-1"},
		Name:     "test-emby",
		Provider: model.StrmProviderEmbyRemote,
		Config:   string(rawConfig),
		Enabled:  true,
	}
	if err := repos.StrmAccount.Create(t.Context(), acct); err != nil {
		t.Fatalf("create account: %v", err)
	}
	mount := &model.EmbyMount{
		Base:           model.Base{ID: "mount-1"},
		AccountID:      acct.ID,
		RemoteViewID:   "view-1",
		RemoteViewName: "剧集库",
		CollectionType: "tvshows",
		Enabled:        true,
	}
	if err := repos.EmbyMount.Create(t.Context(), mount); err != nil {
		t.Fatalf("create mount: %v", err)
	}

	cards, err := svc.RemoteSeriesCards(t.Context(), mount, acct, "view-1")
	if err != nil {
		t.Fatalf("RemoteSeriesCards on first browse failed: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	if cards[0].Rep.Title != "测试剧" {
		t.Fatalf("title = %q, want 测试剧", cards[0].Rep.Title)
	}
	if cards[0].Count != 12 {
		t.Fatalf("count = %d, want 12 (RecursiveItemCount)", cards[0].Count)
	}
	if n := zeroUserHits.Load(); n != 0 {
		t.Fatalf("request hit /Users/0/Items %d time(s), want 0 (must use real user id)", n)
	}

	// 首次浏览自动认证应把 token 与 remote_user_id 回写账号配置（等价于测试连接）。
	stored := map[string]string{}
	if err := json.Unmarshal([]byte(acct.Config), &stored); err != nil {
		t.Fatalf("decode account config: %v", err)
	}
	if stored["api_key"] == "" {
		t.Fatalf("account config missing api_key after first browse: %v", stored)
	}
	if stored["remote_user_id"] != "real-user-guid" {
		t.Fatalf("remote_user_id = %q, want real-user-guid (config %v)", stored["remote_user_id"], stored)
	}

	// 第二次浏览不再需要认证步骤，直接命中真实用户 ID。
	if _, err := svc.RemoteSeriesCards(t.Context(), mount, acct, "view-1"); err != nil {
		t.Fatalf("RemoteSeriesCards second browse failed: %v", err)
	}
}

func TestRemoteSeriesCardsResolveUserIDFromAPIKey(t *testing.T) {
	// api_key 直连场景：账号只填了 token（无用户名/密码），从未回写过
	// remote_user_id。首次浏览应通过 /Users 列表解析出真实用户 GUID。
	var zeroUserHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/emby/Users" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "real-user-guid", "Name": "admin"},
			})
			return
		}
		if r.URL.Path == "/emby/Users/0/Items" {
			zeroUserHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("Unrecognized Guid format."))
			return
		}
		if r.URL.Path == "/emby/Users/real-user-guid/Items" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"TotalRecordCount": 1,
				"Items": []map[string]any{
					{"Id": "series-200", "Name": "API剧", "Type": "Series", "RecursiveItemCount": 8},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{})
	repos := repository.New(db)
	svc := NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, NewCryptoService("", zap.NewNop()))

	rawConfig, _ := json.Marshal(map[string]string{
		"url":   server.URL,
		"token": "api-key-only",
	})
	acct := &model.StrmAccount{
		Base:     model.Base{ID: "acct-2"},
		Name:     "api-key-emby",
		Provider: model.StrmProviderEmbyRemote,
		Config:   string(rawConfig),
		Enabled:  true,
	}
	if err := repos.StrmAccount.Create(t.Context(), acct); err != nil {
		t.Fatalf("create account: %v", err)
	}
	mount := &model.EmbyMount{
		Base:           model.Base{ID: "mount-2"},
		AccountID:      acct.ID,
		RemoteViewID:   "view-2",
		RemoteViewName: "剧集库",
		CollectionType: "tvshows",
		Enabled:        true,
	}
	if err := repos.EmbyMount.Create(t.Context(), mount); err != nil {
		t.Fatalf("create mount: %v", err)
	}

	cards, err := svc.RemoteSeriesCards(t.Context(), mount, acct, "view-2")
	if err != nil {
		t.Fatalf("RemoteSeriesCards with api_key only failed: %v", err)
	}
	if len(cards) != 1 || cards[0].Rep.Title != "API剧" {
		t.Fatalf("cards = %#v, want 1 card 测试剧", cards)
	}
	if n := zeroUserHits.Load(); n != 0 {
		t.Fatalf("request hit /Users/0/Items %d time(s), want 0", n)
	}
	stored := map[string]string{}
	if err := json.Unmarshal([]byte(acct.Config), &stored); err != nil {
		t.Fatalf("decode account config: %v", err)
	}
	if stored["remote_user_id"] != "real-user-guid" {
		t.Fatalf("remote_user_id = %q, want real-user-guid (config %v)", stored["remote_user_id"], stored)
	}
}

func TestRemoteSearchMedia(t *testing.T) {
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

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{})
	repos := repository.New(db)
	svc := NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, NewCryptoService("", zap.NewNop()))

	rawConfig, _ := json.Marshal(map[string]string{
		"url":   server.URL,
		"token": "fake-token",
	})
	acct := &model.StrmAccount{
		Base:     model.Base{ID: "acct-1"},
		Name:     "test-emby",
		Provider: model.StrmProviderEmbyRemote,
		Config:   string(rawConfig),
		Enabled:  true,
	}
	if err := repos.StrmAccount.Create(t.Context(), acct); err != nil {
		t.Fatalf("create account: %v", err)
	}

	mount := &model.EmbyMount{
		Base:           model.Base{ID: "mount-1"},
		AccountID:      acct.ID,
		RemoteViewID:   "view-1",
		RemoteViewName: "动漫",
		CollectionType: "tvshows",
		Enabled:        true,
	}
	if err := repos.EmbyMount.Create(t.Context(), mount); err != nil {
		t.Fatalf("create mount: %v", err)
	}

	// 1. 正常搜索
	items, err := svc.RemoteSearchMedia(t.Context(), "碧蓝之海", 10, MediaVisibility{IncludeNSFW: true})
	if err != nil {
		t.Fatalf("RemoteSearchMedia failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Title != "碧蓝之海" {
		t.Fatalf("expected Title '碧蓝之海', got %q", items[0].Title)
	}
	expectedID := EncodeEmbyRemoteID("mount-1", "156030")
	if items[0].ID != expectedID {
		t.Fatalf("expected ID %q, got %q", expectedID, items[0].ID)
	}

	// 2. 搜索不到的内容
	notFound, err := svc.RemoteSearchMedia(t.Context(), "其它不存在的剧", 10, MediaVisibility{IncludeNSFW: true})
	if err != nil {
		t.Fatalf("RemoteSearchMedia failed: %v", err)
	}
	if len(notFound) != 0 {
		t.Fatalf("expected 0 items, got %d", len(notFound))
	}

	// 3. 白名单过滤：当白名单不包含该挂载虚拟库 ID 时应过滤掉
	allowedLibID := "local-lib-1"
	filtered, err := svc.RemoteSearchMedia(t.Context(), "碧蓝之海", 10, MediaVisibility{
		IncludeNSFW:       true,
		AllowedLibraryIDs: []string{allowedLibID},
	})
	if err != nil {
		t.Fatalf("RemoteSearchMedia with allowed filter failed: %v", err)
	}
	if len(filtered) != 0 {
		t.Fatalf("expected 0 items due to AllowedLibraryIDs, got %d", len(filtered))
	}

	// 4. 黑名单过滤：当黑名单包含该挂载虚拟库 ID 时应过滤掉
	mountLibID := EncodeEmbyRemoteID("mount-1", "view-1")
	hiddenFiltered, err := svc.RemoteSearchMedia(t.Context(), "碧蓝之海", 10, MediaVisibility{
		IncludeNSFW:      true,
		HiddenLibraryIDs: []string{mountLibID},
	})
	if err != nil {
		t.Fatalf("RemoteSearchMedia with hidden filter failed: %v", err)
	}
	if len(hiddenFiltered) != 0 {
		t.Fatalf("expected 0 items due to HiddenLibraryIDs, got %d", len(hiddenFiltered))
	}
}

func TestRemoteLatestCardsTvShowsYearAndPoster(t *testing.T) {
	var requestedFields string
	var requestedIncludeItemTypes string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedFields = r.URL.Query().Get("Fields")
		requestedIncludeItemTypes = r.URL.Query().Get("IncludeItemTypes")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"TotalRecordCount": 1,
			"Items": []map[string]any{
				{
					"Id":             "series-100",
					"Name":           "炒翻天",
					"Type":           "Series",
					"ProductionYear": 2024,
					"ImageTags": map[string]any{
						"Primary": "tag123",
					},
					"RecursiveItemCount": 12,
				},
			},
		})
	}))
	defer server.Close()

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{})
	repos := repository.New(db)
	svc := NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, NewCryptoService("", zap.NewNop()))

	rawConfig, _ := json.Marshal(map[string]string{
		"url":   server.URL,
		"token": "fake-token",
	})
	acct := &model.StrmAccount{
		Base:     model.Base{ID: "acct-tv"},
		Name:     "tv-emby",
		Provider: model.StrmProviderEmbyRemote,
		Config:   string(rawConfig),
		Enabled:  true,
	}
	_ = repos.StrmAccount.Create(t.Context(), acct)
	mount := &model.EmbyMount{
		Base:           model.Base{ID: "mount-tv"},
		AccountID:      acct.ID,
		RemoteViewID:   "view-tv",
		RemoteViewName: "新番连载",
		CollectionType: "tvshows",
		Enabled:        true,
	}
	_ = repos.EmbyMount.Create(t.Context(), mount)

	cards, err := svc.RemoteLatestCards(t.Context(), mount, acct, "view-tv", 10)
	if err != nil {
		t.Fatalf("RemoteLatestCards failed: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].Rep.Year != 2024 {
		t.Fatalf("expected Year 2024, got %d", cards[0].Rep.Year)
	}
	if cards[0].Count != 12 {
		t.Fatalf("expected Count 12, got %d", cards[0].Count)
	}
	if cards[0].Rep.PosterURL == "" {
		t.Fatalf("expected PosterURL not empty")
	}
	if requestedIncludeItemTypes != "Series" {
		t.Fatalf("expected IncludeItemTypes=Series, got %q", requestedIncludeItemTypes)
	}
	if !strings.Contains(requestedFields, "ProductionYear") {
		t.Fatalf("expected Fields to contain ProductionYear, got %q", requestedFields)
	}
}

func TestRemoteLatestFields(t *testing.T) {
	var requestedFields string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedFields = r.URL.Query().Get("Fields")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"Id":             "movie-100",
				"Name":           "测试电影",
				"Type":           "Movie",
				"ProductionYear": 2023,
			},
		})
	}))
	defer server.Close()

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{})
	repos := repository.New(db)
	svc := NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, NewCryptoService("", zap.NewNop()))

	rawConfig, _ := json.Marshal(map[string]string{
		"url":   server.URL,
		"token": "fake-token",
	})
	acct := &model.StrmAccount{
		Base:     model.Base{ID: "acct-movie"},
		Name:     "movie-emby",
		Provider: model.StrmProviderEmbyRemote,
		Config:   string(rawConfig),
		Enabled:  true,
	}
	_ = repos.StrmAccount.Create(t.Context(), acct)
	mount := &model.EmbyMount{
		Base:           model.Base{ID: "mount-movie"},
		AccountID:      acct.ID,
		RemoteViewID:   "view-movie",
		CollectionType: "movies",
		Enabled:        true,
	}

	items, err := svc.RemoteLatest(t.Context(), mount, acct, "view-movie", 10)
	if err != nil {
		t.Fatalf("RemoteLatest failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if !strings.Contains(requestedFields, "ProductionYear") {
		t.Fatalf("expected Fields to contain ProductionYear, got %q", requestedFields)
	}
}
