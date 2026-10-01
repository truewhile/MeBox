package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// 回归：挂载的远程 Emby 剧集里，只看了几秒就退出的那一集必须仍是 NextUp 的结果。
// 客户端（Yamby 等）剧集详情页的「继续播放」直接取 NextUp 第一条，跳集会播错集。
func TestMountedRemoteNextUpKeepsPartiallyWatchedEpisode(t *testing.T) {
	episode := func(id string, index int) map[string]any {
		return map[string]any{
			"Id":                id,
			"Name":              "第" + strconv.Itoa(index) + "集",
			"Type":              "Episode",
			"SeriesId":          "series-100",
			"ParentIndexNumber": 1,
			"IndexNumber":       index,
			"RunTimeTicks":      14400640000,
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/emby/Users/uid-1/Items/series-100":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "series-100", "Name": "剧一", "Type": "Series"})
		case "/emby/Users/uid-1/Items/ep-2":
			_ = json.NewEncoder(w).Encode(episode("ep-2", 2))
		case "/emby/Users/uid-1/Items":
			q := r.URL.Query()
			if q.Get("IncludeItemTypes") != "Episode" || q.Get("ParentId") != "series-100" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"TotalRecordCount": 3,
				"Items": []map[string]any{
					episode("ep-1", 1),
					episode("ep-2", 2),
					episode("ep-3", 3),
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{}, &model.PlaybackHistory{}, &model.User{})
	repos := repository.New(db)

	cfg := &config.Config{}
	remote := NewEmbyRemoteService(cfg, zap.NewNop(), repos, NewCryptoService("", zap.NewNop()))
	svc := NewEmbyService(cfg, zap.NewNop(), repos).SetEmbyRemote(remote)

	rawConfig, _ := json.Marshal(map[string]string{
		"url":            server.URL,
		"api_key":        "test-api-key",
		"remote_user_id": "uid-1",
	})
	acct := &model.StrmAccount{
		Base:     model.Base{ID: "acct-1"},
		Name:     "远程 Emby",
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
		RemoteViewName: "新番连载",
		CollectionType: "tvshows",
		Enabled:        true,
	}
	if err := repos.EmbyMount.Create(t.Context(), mount); err != nil {
		t.Fatalf("create mount: %v", err)
	}
	user := &model.User{
		Base:         model.Base{ID: "user-1"},
		Username:     "viewer",
		PasswordHash: "x",
		Role:         "user",
		Tier:         "free",
		IsActive:     true,
	}
	if err := repos.User.Create(t.Context(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// 第 2 集只播了 3.5 秒就退出：有进度、未标记看完。
	if err := repos.DB.Create(&model.PlaybackHistory{
		UserID:     user.ID,
		MediaID:    EncodeEmbyRemoteID(mount.ID, "ep-2"),
		PositionMs: 3582,
		DurationMs: 1440064,
		WatchedAt:  time.Now(),
		Completed:  false,
	}).Error; err != nil {
		t.Fatalf("create history: %v", err)
	}

	envelope, err := svc.NextUp(t.Context(), user.ID, EncodeEmbyRemoteID(mount.ID, "series-100"), 1)
	if err != nil {
		t.Fatalf("NextUp: %v", err)
	}
	items, _ := envelope["Items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (%#v)", len(items), envelope)
	}
	wantID := EncodeEmbyRemoteID(mount.ID, "ep-2")
	if id, _ := items[0]["Id"].(string); id != wantID {
		t.Fatalf("Id = %q, want %q (未看完的那一集不能被跳过)", id, wantID)
	}
	userData, _ := items[0]["UserData"].(map[string]any)
	if ticks, _ := userData["PlaybackPositionTicks"].(int64); ticks != 35820000 {
		t.Fatalf("PlaybackPositionTicks = %#v, want 35820000 (详情页要能续播到 3.5 秒)", userData["PlaybackPositionTicks"])
	}
}

// 远程剧集已看完当前一集时，NextUp 仍要指向下一集。
func TestMountedRemoteNextUpAfterCompletedEpisode(t *testing.T) {
	episode := func(id string, index int) map[string]any {
		return map[string]any{
			"Id":                id,
			"Name":              "第" + strconv.Itoa(index) + "集",
			"Type":              "Episode",
			"SeriesId":          "series-100",
			"ParentIndexNumber": 1,
			"IndexNumber":       index,
			"RunTimeTicks":      14400640000,
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/emby/Users/uid-1/Items/series-100":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "series-100", "Name": "剧一", "Type": "Series"})
		case "/emby/Users/uid-1/Items/ep-3":
			_ = json.NewEncoder(w).Encode(episode("ep-3", 3))
		case "/emby/Users/uid-1/Items":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"TotalRecordCount": 3,
				"Items": []map[string]any{
					episode("ep-1", 1),
					episode("ep-2", 2),
					episode("ep-3", 3),
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{}, &model.PlaybackHistory{}, &model.User{})
	repos := repository.New(db)

	cfg := &config.Config{}
	remote := NewEmbyRemoteService(cfg, zap.NewNop(), repos, NewCryptoService("", zap.NewNop()))
	svc := NewEmbyService(cfg, zap.NewNop(), repos).SetEmbyRemote(remote)

	rawConfig, _ := json.Marshal(map[string]string{
		"url":            server.URL,
		"api_key":        "test-api-key",
		"remote_user_id": "uid-1",
	})
	acct := &model.StrmAccount{
		Base:     model.Base{ID: "acct-1"},
		Name:     "远程 Emby",
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
		RemoteViewName: "新番连载",
		CollectionType: "tvshows",
		Enabled:        true,
	}
	if err := repos.EmbyMount.Create(t.Context(), mount); err != nil {
		t.Fatalf("create mount: %v", err)
	}
	user := &model.User{
		Base:         model.Base{ID: "user-1"},
		Username:     "viewer",
		PasswordHash: "x",
		Role:         "user",
		Tier:         "free",
		IsActive:     true,
	}
	if err := repos.User.Create(t.Context(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// 第 2 集已整集看完。
	if err := repos.DB.Create(&model.PlaybackHistory{
		UserID:     user.ID,
		MediaID:    EncodeEmbyRemoteID(mount.ID, "ep-2"),
		PositionMs: 1440064,
		DurationMs: 1440064,
		WatchedAt:  time.Now(),
		Completed:  true,
	}).Error; err != nil {
		t.Fatalf("create history: %v", err)
	}

	envelope, err := svc.NextUp(context.Background(), user.ID, EncodeEmbyRemoteID(mount.ID, "series-100"), 1)
	if err != nil {
		t.Fatalf("NextUp: %v", err)
	}
	items, _ := envelope["Items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (%#v)", len(items), envelope)
	}
	if id, _ := items[0]["Id"].(string); id != EncodeEmbyRemoteID(mount.ID, "ep-3") {
		t.Fatalf("Id = %q, want ep-3", id)
	}
}
