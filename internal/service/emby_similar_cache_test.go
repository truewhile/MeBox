package service

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
)

// totalRecordCount 兼容两种来源：直算出来的是 int64，经过进程内缓存 JSON
// 往返后是 float64。两者序列化成 Emby 响应时完全一致。
func totalRecordCount(t *testing.T, out map[string]any) int {
	t.Helper()
	switch v := out["TotalRecordCount"].(type) {
	case int64:
		return int(v)
	case int:
		return v
	case float64:
		return int(v)
	default:
		t.Fatalf("unexpected TotalRecordCount type %T (%v)", out["TotalRecordCount"], out["TotalRecordCount"])
		return 0
	}
}

// 相似推荐的结果要缓存：详情页每次打开都会请求它，重建要走「取候选池 + 内存打分」
// （实测冷 340ms / 热 70ms）。
func TestSimilarItemsServesCachedPayload(t *testing.T) {
	svc := newTestEmbyService(t)
	repos := svc.repo
	svc.SetRuntimeCache(NewRuntimeCacheService(&config.Config{}, zap.NewNop()))

	libID := seedDiscoveryLibrary(t, repos, "movie")
	source := seedSimilarMedia(t, repos, libID, "源片", "Action", 2010, 7)
	seedSimilarMedia(t, repos, libID, "候选甲", "Action", 2010, 7)
	seedSimilarMedia(t, repos, libID, "候选乙", "Comedy", 2011, 6)

	ctx := context.Background()
	first, err := svc.SimilarItems(ctx, source.ID, "user-1", 10)
	if err != nil {
		t.Fatalf("first SimilarItems: %v", err)
	}
	want := totalRecordCount(t, first)
	if want == 0 {
		t.Fatalf("first call returned no candidates, test data is wrong: %+v", first)
	}

	// 把候选全部删掉：第二次如果还返回原结果，只可能是命中缓存。
	if err := repos.DB.Where("id <> ?", source.ID).Delete(&model.Media{}).Error; err != nil {
		t.Fatal(err)
	}

	second, err := svc.SimilarItems(ctx, source.ID, "user-1", 10)
	if err != nil {
		t.Fatalf("second SimilarItems: %v", err)
	}
	if got := totalRecordCount(t, second); got != want {
		t.Fatalf("cached call returned %d items, want %d (cache miss?)", got, want)
	}

	// 另一个用户（不同的可见性）不能复用别人的缓存：这里应当重新查询并得到 0。
	other, err := svc.SimilarItems(ctx, source.ID, "user-2", 10)
	if err != nil {
		t.Fatalf("other user SimilarItems: %v", err)
	}
	if got := totalRecordCount(t, other); got != 0 {
		t.Fatalf("other user got %d items, want 0 (per-user cache key)", got)
	}
}

// limit 不同必须分开缓存，否则一次小 limit 请求会污染后续更大的请求。
func TestSimilarItemsCacheSeparatesLimit(t *testing.T) {
	svc := newTestEmbyService(t)
	repos := svc.repo
	svc.SetRuntimeCache(NewRuntimeCacheService(&config.Config{}, zap.NewNop()))

	libID := seedDiscoveryLibrary(t, repos, "movie")
	source := seedSimilarMedia(t, repos, libID, "源片", "Action", 2010, 7)
	for _, title := range []string{"甲", "乙", "丙", "丁"} {
		seedSimilarMedia(t, repos, libID, "候选"+title, "Action", 2010, 7)
	}

	ctx := context.Background()
	small, err := svc.SimilarItems(ctx, source.ID, "user-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := totalRecordCount(t, small); got != 2 {
		t.Fatalf("limit=2 returned %d items, want 2", got)
	}
	large, err := svc.SimilarItems(ctx, source.ID, "user-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := totalRecordCount(t, large); got != 4 {
		t.Fatalf("limit=4 returned %d items, want 4 (limit must be part of the cache key)", got)
	}
}

// 没有注入缓存时（测试/精简部署）也必须正常工作。
func TestSimilarItemsWithoutCacheStillWorks(t *testing.T) {
	svc := newTestEmbyService(t)
	libID := seedDiscoveryLibrary(t, svc.repo, "movie")
	source := seedSimilarMedia(t, svc.repo, libID, "源片", "Action", 2010, 7)
	seedSimilarMedia(t, svc.repo, libID, "候选甲", "Action", 2010, 7)

	out, err := svc.SimilarItems(context.Background(), source.ID, "user-1", 10)
	if err != nil {
		t.Fatalf("SimilarItems: %v", err)
	}
	if got := totalRecordCount(t, out); got == 0 {
		t.Fatalf("expected candidates without a cache, got %+v", out)
	}
}
