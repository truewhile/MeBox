package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// newDanmakuRemoteResolverTest 起一个假的远程 Emby（账号 + 挂载齐备），返回接好
// 弹幕解析器的 builder。
func newDanmakuRemoteResolverTest(t *testing.T, handler http.Handler) *serviceContainerBuilder {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{})
	repos := repository.New(db)
	cfg := &config.Config{}
	remote := NewEmbyRemoteService(cfg, zap.NewNop(), repos, NewCryptoService("", zap.NewNop()))
	remote.SetRuntimeCache(NewRuntimeCacheService(cfg, zap.NewNop()))

	rawConfig, _ := json.Marshal(map[string]string{
		"url":            server.URL,
		"api_key":        "test-api-key",
		"remote_user_id": "uid-1",
	})
	if err := repos.StrmAccount.Create(t.Context(), &model.StrmAccount{
		Base:     model.Base{ID: "acct-1"},
		Name:     "远程 Emby",
		Provider: model.StrmProviderEmbyRemote,
		Config:   string(rawConfig),
		Enabled:  true,
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := repos.EmbyMount.Create(t.Context(), &model.EmbyMount{
		Base:           model.Base{ID: "mount-1"},
		AccountID:      "acct-1",
		RemoteViewID:   "view-1",
		RemoteViewName: "新番连载",
		CollectionType: "tvshows",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("create mount: %v", err)
	}
	return &serviceContainerBuilder{
		cfg:   cfg,
		log:   zap.NewNop(),
		repos: repos,
		c:     &Container{Cfg: cfg, Log: zap.NewNop(), Repo: repos, EmbyRemote: remote},
	}
}

func danmakuResolverTestEpisode(id string) map[string]any {
	return map[string]any{
		"Id":   id,
		"Name": "第 1 集",
		"Type": "Episode",
		"Path": "/media/anime/PSYREN/" + id + ".mkv",
		"MediaSources": []any{
			map[string]any{"Id": id + "-src", "Container": "mkv", "Size": int64(1 << 20)},
		},
	}
}

// 目录类条目（整部剧/季）自身没有媒体源，拿它的 ID 去请求远程 /Videos/{id}/stream 只会
// 回 500，哈希层于是每次都白白跳过。这条用例保证弹幕解析器跟随 /api/stream 的规则先
// 落到第一集：哈希算的是真正要播的那条流。
func TestResolveDanmakuRemoteMediaUsesPlayableChildForContainer(t *testing.T) {
	var requested []string
	b := newDanmakuRemoteResolverTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/emby/Users/uid-1/Items/series-100":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": "series-100", "Name": "PSYREN", "Type": "Series", "IsFolder": true,
			})
		case "/emby/Users/uid-1/Items":
			if got := r.URL.Query().Get("ParentId"); got != "series-100" {
				t.Errorf("ParentId = %q, want series-100", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "season-1", "Type": "Season", "IndexNumber": 1},
					danmakuResolverTestEpisode("ep-1"),
				},
			})
		case "/emby/Users/uid-1/Items/ep-1":
			_ = json.NewEncoder(w).Encode(danmakuResolverTestEpisode("ep-1"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	media, streamURL, err := b.resolveDanmakuRemoteMedia(
		context.Background(), EncodeEmbyRemoteID("mount-1", "series-100"))
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if media == nil {
		t.Fatal("resolve returned no media metadata")
	}
	if !strings.Contains(streamURL, "/Videos/ep-1/stream") {
		t.Fatalf("stream url = %q, want the playable child ep-1", streamURL)
	}
	if strings.Contains(streamURL, "series-100") {
		t.Fatalf("stream url = %q, must not point at the container id (remote Emby answers 500)", streamURL)
	}
	if !strings.Contains(strings.Join(requested, " "), "/Items/ep-1") {
		t.Fatalf("metadata must come from the playable child, requests = %v", requested)
	}
}

// 本身可播的条目（单集/电影）不能被改写：直接取它自己的流地址。
func TestResolveDanmakuRemoteMediaKeepsPlayableItem(t *testing.T) {
	b := newDanmakuRemoteResolverTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/emby/Users/uid-1/Items/ep-2" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(danmakuResolverTestEpisode("ep-2"))
	}))

	media, streamURL, err := b.resolveDanmakuRemoteMedia(
		context.Background(), EncodeEmbyRemoteID("mount-1", "ep-2"))
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if media == nil {
		t.Fatal("resolve returned no media metadata")
	}
	if !strings.Contains(streamURL, "/Videos/ep-2/stream") {
		t.Fatalf("stream url = %q, want /Videos/ep-2/stream", streamURL)
	}
}

// 不是远程伪装 ID 时直接报错，不去猜一个可以拉取的地址。
func TestResolveDanmakuRemoteMediaRejectsPlainID(t *testing.T) {
	b := newDanmakuRemoteResolverTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("plain local ids must not reach the remote Emby")
		w.WriteHeader(http.StatusNotFound)
	}))

	if _, _, err := b.resolveDanmakuRemoteMedia(context.Background(), "local-media-1"); err == nil {
		t.Fatal("expected an error for a non remote-emby id")
	}
}
