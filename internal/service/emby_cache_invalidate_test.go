package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// resetEmbyInvalThrottle 清掉全局失效节流，让测试能断言「这一次调用到底有
// 没有真的清缓存」。测试结束后恢复原值，避免影响同包其他用例。
func resetEmbyInvalThrottle(t *testing.T) {
	t.Helper()
	embyInvalMu.Lock()
	previous := embyInvalLast
	embyInvalLast = time.Time{}
	embyInvalMu.Unlock()
	t.Cleanup(func() {
		embyInvalMu.Lock()
		embyInvalLast = previous
		embyInvalMu.Unlock()
	})
}

func newCachedTestEmbyService(t *testing.T) *EmbyService {
	t.Helper()
	svc := newTestEmbyService(t)
	cache := NewRuntimeCacheService(&config.Config{}, zap.NewNop())
	t.Cleanup(func() { _ = cache.Close() })
	return svc.SetRuntimeCache(cache)
}

// TestRecordProgressKeepsEmbyPayloadCache 覆盖首页加载慢的核心回归点：
// 播放期间客户端每 5-10 秒上报一次进度，如果每次都清空 media:emby:*，
// 客户端刷新首页时 73 个媒体库的 Latest 会全部回源重建（生产环境实测
// 首批 8-25s）。普通进度上报必须保留载荷缓存；只有「已看完」翻转才失效。
func TestRecordProgressKeepsEmbyPayloadCache(t *testing.T) {
	svc := newCachedTestEmbyService(t)
	const mediaID = "ep-1"
	media := model.Media{
		Base:        model.Base{ID: mediaID},
		Title:       "第 1 集",
		Path:        `/media/tv/Show/Season 01/S01E01.mkv`,
		DurationSec: 100,
	}
	if err := svc.repo.DB.Create(&media).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}

	key := svc.embyLatestCacheKey("user-1", "", 16)
	svc.cache.SetJSONWithStale(t.Context(), key, embyLatestCacheValue{
		Items: []map[string]any{{"Id": "cached-item"}},
	}, time.Minute, time.Hour)

	resetEmbyInvalThrottle(t)
	// 60s / 100s：播放中途
	if err := svc.RecordProgressWithSession(t.Context(), "user-1", mediaID, 600_000_000, 1_000_000_000, "session-1"); err != nil {
		t.Fatalf("record progress: %v", err)
	}
	if _, stale, ok := svc.cachedLatestItemsWithStale(t.Context(), key); !ok || stale {
		t.Fatal("普通进度上报不应让 Emby 列表载荷缓存失效")
	}

	resetEmbyInvalThrottle(t)
	// 98s / 100s：进入片尾 30s 内的「已看完」区间
	if err := svc.RecordProgressWithSession(t.Context(), "user-1", mediaID, 980_000_000, 1_000_000_000, "session-2"); err != nil {
		t.Fatalf("record completed progress: %v", err)
	}
	if _, _, ok := svc.cachedLatestItemsWithStale(t.Context(), key); ok {
		t.Fatal("看完状态翻转后应让 Emby 列表载荷缓存失效")
	}
}

// TestFindSeriesGroupCachesSiblingVirtualSeries 覆盖 N+1 回归点：一次全表
// 扫描算出了整库的剧集分组，若只缓存命中的那一部，同一页的 16 张卡片和
// 16 张海报会各自重扫一遍 media 表。批量记忆后同批分组都应可命中。
func TestFindSeriesGroupCachesSiblingVirtualSeries(t *testing.T) {
	svc := newTestEmbyService(t)
	lib := model.Library{Name: "剧集", Path: `/media/tv`, Type: "tv", Enabled: true}
	if err := svc.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}

	shows := []string{"Alpha", "Bravo", "Charlie"}
	rows := make([]model.Media, 0, len(shows)*2)
	for _, show := range shows {
		for ep := 1; ep <= 2; ep++ {
			rows = append(rows, model.Media{
				Base:       model.Base{ID: show + "-" + strconv.Itoa(ep)},
				LibraryID:  lib.ID,
				Title:      show,
				Path:       fmt.Sprintf("/media/tv/%s/Season 01/S01E%02d.mkv", show, ep),
				SeasonNum:  1,
				EpisodeNum: ep,
			})
		}
	}
	for i := range rows {
		if err := svc.repo.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("create media: %v", err)
		}
	}

	seriesIDs := map[string]string{}
	for i := range rows {
		if rows[i].EpisodeNum == 1 {
			seriesIDs[rows[i].Title] = svc.seriesIDForMedia(t.Context(), &rows[i])
		}
	}
	target, ok := seriesIDs["Alpha"]
	if !ok {
		t.Fatal("missing fixture series")
	}
	if _, found, err := svc.findSeriesGroup(t.Context(), target, ""); err != nil || !found {
		t.Fatalf("findSeriesGroup(%s) = (%v, %v), want found", target, found, err)
	}

	for show, id := range seriesIDs {
		if show == "Alpha" {
			continue
		}
		if _, cached := svc.cachedSeriesGroup(id); !cached {
			t.Fatalf("同一次扫描算出的 %s 应一并进入缓存，否则每张卡片/海报都会重扫整库", show)
		}
	}
}

// TestLatestPayloadKeepsStaleCopyForIdleClients 保证「最新添加」载荷在新鲜期
// 之后仍有可用的陈旧副本：客户端空闲很久后再打开首页时，先拿陈旧结果并由
// 后台重建，而不是让全部媒体库同时回源。
func TestLatestPayloadKeepsStaleCopyForIdleClients(t *testing.T) {
	svc := newCachedTestEmbyService(t)
	key := svc.embyLatestCacheKey("user-1", "", 16)
	svc.cache.SetJSONWithStale(t.Context(), key, embyLatestCacheValue{
		Items: []map[string]any{{"Id": "stale-item"}},
	}, time.Nanosecond, embyLatestStaleWindow)
	time.Sleep(2 * time.Millisecond)

	items, stale, ok := svc.cachedLatestItemsWithStale(t.Context(), key)
	if !ok || !stale {
		t.Fatalf("expected a stale hit, got ok=%v stale=%v", ok, stale)
	}
	if len(items) != 1 || items[0]["Id"] != "stale-item" {
		t.Fatalf("unexpected stale payload: %#v", items)
	}
}

// mediaScanCounter 统计真正落到 media 表上的查询次数，用来断言「整库数据
// 只扫一次」：GORM 的 Trace 回调里能拿到最终 SQL。
type mediaScanCounter struct {
	logger.Interface
	mu    sync.Mutex
	scans int
}

func (c *mediaScanCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	if strings.Contains(sql, "FROM `media`") {
		c.mu.Lock()
		c.scans++
		c.mu.Unlock()
	}
}

func (c *mediaScanCounter) Scans() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scans
}

// TestVirtualSeriesLookupsShareOneScan 覆盖冷启动风暴的回归点：客户端刷新
// 首页时 73 个库的卡片和海报各自要一个虚拟 series 分组。修好之后，第一次
// 扫描把整库分组都写进缓存，同一页剩下的查询都不该再落 SQL；随后一个不存在
// 的虚拟 ID 也不该反复触发整库重扫。
func TestVirtualSeriesLookupsShareOneScan(t *testing.T) {
	counter := &mediaScanCounter{Interface: logger.Discard}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: counter})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	svc := NewEmbyService(&config.Config{}, zap.NewNop(), repository.New(db))

	lib := model.Library{Name: "剧集", Path: `/media/tv`, Type: "tv", Enabled: true}
	if err := svc.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	shows := []string{"Alpha", "Bravo", "Charlie"}
	rows := make([]model.Media, 0, len(shows)*2)
	for _, show := range shows {
		for ep := 1; ep <= 2; ep++ {
			rows = append(rows, model.Media{
				Base:       model.Base{ID: show + "-" + strconv.Itoa(ep)},
				LibraryID:  lib.ID,
				Title:      show,
				Path:       fmt.Sprintf("/media/tv/%s/Season 01/S01E%02d.mkv", show, ep),
				SeasonNum:  1,
				EpisodeNum: ep,
			})
		}
	}
	for i := range rows {
		if err := svc.repo.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("create media: %v", err)
		}
	}
	seriesIDs := map[string]string{}
	for i := range rows {
		if rows[i].EpisodeNum == 1 {
			seriesIDs[rows[i].Title] = svc.seriesIDForMedia(t.Context(), &rows[i])
		}
	}

	if _, found, err := svc.findSeriesGroup(t.Context(), seriesIDs["Alpha"], ""); err != nil || !found {
		t.Fatalf("findSeriesGroup(Alpha) = (%v, %v), want found", found, err)
	}
	if got := counter.Scans(); got != 1 {
		t.Fatalf("首次查询应恰好扫描 media 一次，实际 %d 次", got)
	}

	for _, show := range []string{"Bravo", "Charlie"} {
		if _, found, err := svc.findSeriesGroup(t.Context(), seriesIDs[show], ""); err != nil || !found {
			t.Fatalf("findSeriesGroup(%s) = (%v, %v), want found", show, found, err)
		}
	}
	if got := counter.Scans(); got != 1 {
		t.Fatalf("同批分组应命中缓存，不应再扫 media，实际累计 %d 次", got)
	}

	if _, found, err := svc.findSeriesGroup(t.Context(), embyVirtualSeriesPrefix+"missing", ""); err != nil || found {
		t.Fatalf("未知虚拟 ID 应为 not found，实际 found=%v err=%v", found, err)
	}
	if got := counter.Scans(); got != 1 {
		t.Fatalf("刚扫过整库后，未知 ID 不应重扫 media，实际累计 %d 次", got)
	}
}
