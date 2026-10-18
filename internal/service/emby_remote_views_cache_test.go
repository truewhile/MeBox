package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// TestRemoteLibrariesFastFailAndNegativeCache 覆盖首页可用的底线保证：
// 上游 Emby 整体挂起时，聚合远程库必须在一个很短的预算内失败并降级为
// 「没有远程库」，并且在负缓存窗口内不再重复打上游。
func TestRemoteLibrariesFastFailAndNegativeCache(t *testing.T) {
	oldBudget, oldTTL := embyRemoteViewsBudget, embyRemoteViewsFailureTTL
	embyRemoteViewsBudget = 200 * time.Millisecond
	embyRemoteViewsFailureTTL = time.Minute
	t.Cleanup(func() {
		embyRemoteViewsBudget = oldBudget
		embyRemoteViewsFailureTTL = oldTTL
	})

	release := make(chan struct{})
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		// 模拟 Cloudflare 522 前近 20 秒的挂起：请求一直不返回。
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}))
	defer upstream.Close()
	defer close(release)

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{})
	repos := repository.New(db)
	remote := NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, NewCryptoService("", zap.NewNop())).
		SetRuntimeCache(NewRuntimeCacheService(nil, zap.NewNop()))

	rawConfig, _ := json.Marshal(map[string]string{
		"url":            upstream.URL,
		"api_key":        "test-api-key",
		"remote_user_id": "uid-1",
	})
	acct := &model.StrmAccount{
		Base:     model.Base{ID: "acct-hang"},
		Name:     "挂起的远端",
		Provider: model.StrmProviderEmbyRemote,
		Config:   string(rawConfig),
		Enabled:  true,
	}
	if err := repos.StrmAccount.Create(t.Context(), acct); err != nil {
		t.Fatalf("create account: %v", err)
	}
	mount := &model.EmbyMount{
		Base:           model.Base{ID: "mount-hang"},
		AccountID:      acct.ID,
		RemoteViewID:   "view-1",
		RemoteViewName: "卡住的库",
		CollectionType: "movies",
		Enabled:        true,
	}
	if err := repos.EmbyMount.Create(t.Context(), mount); err != nil {
		t.Fatalf("create mount: %v", err)
	}

	start := time.Now()
	views, err := remote.RemoteLibraries(t.Context())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RemoteLibraries should degrade instead of failing: %v", err)
	}
	if len(views) != 0 {
		t.Fatalf("expected no remote libraries while upstream hangs, got %d", len(views))
	}
	if elapsed > 2*time.Second {
		t.Fatalf("RemoteLibraries took %v, want within the short budget", elapsed)
	}
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatal("expected upstream to be probed once")
	}

	// 负缓存窗口内：不再打上游，且立即返回空结果。
	before := atomic.LoadInt32(&hits)
	start = time.Now()
	views, err = remote.RemoteLibraries(t.Context())
	elapsed = time.Since(start)
	if err != nil {
		t.Fatalf("second RemoteLibraries should not fail: %v", err)
	}
	if len(views) != 0 {
		t.Fatalf("expected no remote libraries from negative cache, got %d", len(views))
	}
	if elapsed > time.Second {
		t.Fatalf("negative cache should answer immediately, took %v", elapsed)
	}
	if got := atomic.LoadInt32(&hits); got != before {
		t.Fatalf("negative cache should skip upstream, hits went %d -> %d", before, got)
	}
}

// TestRemoteViewsNotNegativeCachedOnClientCancel 保证客户端主动断开不会污染
// 负缓存：否则一个用户切页就会让所有人在 30 秒内看不到远程库。
func TestRemoteViewsNotNegativeCachedOnClientCancel(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}))
	defer upstream.Close()
	defer close(release)

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{})
	repos := repository.New(db)
	cache := NewRuntimeCacheService(nil, zap.NewNop())
	remote := NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, NewCryptoService("", zap.NewNop())).
		SetRuntimeCache(cache)

	rawConfig, _ := json.Marshal(map[string]string{
		"url":            upstream.URL,
		"api_key":        "test-api-key",
		"remote_user_id": "uid-1",
	})
	acct := &model.StrmAccount{
		Base:     model.Base{ID: "acct-cancel"},
		Provider: model.StrmProviderEmbyRemote,
		Config:   string(rawConfig),
		Enabled:  true,
	}
	if err := repos.StrmAccount.Create(t.Context(), acct); err != nil {
		t.Fatalf("create account: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := remote.RemoteViews(ctx, acct); err == nil {
		t.Fatal("expected canceled context to fail")
	}

	var failed bool
	if cache.GetJSON(t.Context(), remote.remoteCacheKey("views-fail", acct.ID), &failed) && failed {
		t.Fatal("client cancel must not write the negative cache")
	}
}
