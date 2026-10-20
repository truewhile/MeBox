package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// newPlayableIDTestService 起一个假的远程 Emby，并建好账号。
func newPlayableIDTestService(t *testing.T, handler http.Handler) (*EmbyRemoteService, *model.StrmAccount) {
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
	return remote, acct
}

func playableIDTestEpisode(id string, index, sources int) map[string]any {
	item := map[string]any{
		"Id":                id,
		"Name":              "第" + strconv.Itoa(index) + "集",
		"Type":              "Episode",
		"SeriesId":          "series-100",
		"ParentIndexNumber": 1,
		"IndexNumber":       index,
	}
	if sources > 0 {
		item["MediaSources"] = []any{map[string]any{"Id": id + "-source"}}
	}
	return item
}

// 整部剧的 ID 自身没有媒体源，远程 Emby 对它的取流请求会回 500
// （Unable to cast 'Series' to 'IHasMediaSources'）。这条用例保证我们会换成第一集，
// 而不是把这种 ID 直接交给远程。
func TestPlayableRemoteIDResolvesContainerToFirstEpisode(t *testing.T) {
	var itemsCalls int64
	remote, acct := newPlayableIDTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/emby/Users/uid-1/Items/series-100":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": "series-100", "Name": "剧一", "Type": "Series", "IsFolder": true,
			})
		case "/emby/Users/uid-1/Items/ep-2":
			_ = json.NewEncoder(w).Encode(playableIDTestEpisode("ep-2", 2, 1))
		case "/emby/Users/uid-1/Items":
			atomic.AddInt64(&itemsCalls, 1)
			if got := r.URL.Query().Get("ParentId"); got != "series-100" {
				t.Errorf("ParentId = %q, want series-100", got)
			}
			// 故意乱序，并让第一条没有媒体源：只有真按季/集排序、且检查过媒体源，
			// 才会选中 ep-1。
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "season-1", "Type": "Season", "IndexNumber": 1},
					playableIDTestEpisode("ep-3", 3, 1),
					playableIDTestEpisode("ep-1", 1, 1),
					playableIDTestEpisode("ep-2", 2, 1),
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	ctx := context.Background()
	if got := remote.PlayableRemoteID(ctx, acct, "series-100"); got != "ep-1" {
		t.Fatalf("series id resolved to %q, want ep-1", got)
	}
	// 播放器对同一个地址会反复发 Range 请求，解析结果必须命中缓存，不再打远程。
	if got := remote.PlayableRemoteID(ctx, acct, "series-100"); got != "ep-1" {
		t.Fatalf("cached resolve = %q, want ep-1", got)
	}
	if n := atomic.LoadInt64(&itemsCalls); n != 1 {
		t.Fatalf("episode query issued %d times, want 1 (cached)", n)
	}
}

// 本身带媒体源的条目（电影/单集）必须原样返回，不能被改写。
func TestPlayableRemoteIDKeepsPlayableItem(t *testing.T) {
	var queries int64
	remote, acct := newPlayableIDTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&queries, 1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/emby/Users/uid-1/Items/ep-2" {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(playableIDTestEpisode("ep-2", 2, 1))
	}))

	if got := remote.PlayableRemoteID(context.Background(), acct, "ep-2"); got != "ep-2" {
		t.Fatalf("playable item = %q, want ep-2", got)
	}
	if n := atomic.LoadInt64(&queries); n != 1 {
		t.Fatalf("upstream queries = %d, want 1", n)
	}
}

// 远程不可用/查不到条目时保持原 ID：让原有流程去报错，别把播放地址改成空。
func TestPlayableRemoteIDKeepsOriginalOnFailure(t *testing.T) {
	remote, acct := newPlayableIDTestService(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	if got := remote.PlayableRemoteID(context.Background(), acct, "series-100"); got != "series-100" {
		t.Fatalf("failure path = %q, want the original id", got)
	}
}

// 容器下没有任何带媒体源的子条目时同样返回原 ID（下一层会给友好报错）。
func TestPlayableRemoteIDKeepsContainerWithoutPlayableChild(t *testing.T) {
	remote, acct := newPlayableIDTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/emby/Users/uid-1/Items" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "season-1", "Type": "Season", "IndexNumber": 1},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "series-100", "Type": "Series", "IsFolder": true})
	}))

	if got := remote.PlayableRemoteID(context.Background(), acct, "series-100"); got != "series-100" {
		t.Fatalf("container without playable child = %q, want the original id", got)
	}
}
