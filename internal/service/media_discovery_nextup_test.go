package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// seedEpisode 插入一集，并把播放历史指向 `watched`（nil 表示没有历史）。
//
// position_ms / duration_ms 只是占位值，NextUp 只看历史行的 completed 字段：
// 未看完的那一集本身就是「接下来该看的一集」。
func seedEpisode(
	t *testing.T,
	repos *repository.Container,
	libID, seriesID, title string,
	season, episode int,
	watchedAt *time.Time,
	completed bool,
) *model.Media {
	t.Helper()
	m := &model.Media{
		LibraryID:  libID,
		SeriesID:   seriesID,
		Title:      title,
		SeasonNum:  season,
		EpisodeNum: episode,
		Path:       "/media/" + seriesID + "/S" + strconv.Itoa(season) + "E" + strconv.Itoa(episode) + ".mkv",
	}
	if err := repos.DB.WithContext(context.Background()).Create(m).Error; err != nil {
		t.Fatal(err)
	}
	if watchedAt != nil || completed {
		// 标记「已看完」也会产生一条历史行，因此 completed 为真时同样要写历史，
		// 否则夹具与真实数据不一致（真实库里已看完一定有行）。
		watched := time.Now().Add(-time.Hour)
		if watchedAt != nil {
			watched = *watchedAt
		}
		h := &model.PlaybackHistory{
			UserID:     "user-1",
			MediaID:    m.ID,
			PositionMs: 1000,
			DurationMs: 2000,
			WatchedAt:  watched,
			Completed:  completed,
		}
		if err := repos.DB.WithContext(context.Background()).Create(h).Error; err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func nextUpIDs(t *testing.T, svc *MediaDiscoveryService, visibility MediaVisibility) []string {
	t.Helper()
	rows, err := svc.NextUpCandidates(context.Background(), "user-1", 20, visibility)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, episodeLabel(row))
	}
	return out
}

// episodeLabel 把一集渲染成 "S1E2"，方便断言。
func episodeLabel(m model.Media) string {
	return "S" + strconv.Itoa(m.SeasonNum) + "E" + strconv.Itoa(m.EpisodeNum)
}

func TestNextUpPicksNextEpisode(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "tv")
	watchedAt := time.Now().Add(-time.Hour)

	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 1, &watchedAt, true) // 第 1 集已看完
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 2, nil, false)
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 3, nil, false)

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	got := nextUpIDs(t, svc, MediaVisibility{IncludeNSFW: true})
	if len(got) != 1 || got[0] != "S1E2" {
		t.Fatalf("next up = %v, want [S1E2]", got)
	}
}

// 回归：只播了几秒就退出（未看完）时，「接下来该看的一集」仍是这一集本身。
// 客户端（Yamby 等）剧集详情页的「继续播放」直接取 NextUp 第一条，跳集会播错集。
func TestNextUpKeepsPartiallyWatchedEpisode(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "tv")
	watchedAt := time.Now().Add(-time.Minute)

	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 1, nil, true)
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 2, &watchedAt, false) // 第 2 集只看了几秒
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 3, nil, false)

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	got := nextUpIDs(t, svc, MediaVisibility{IncludeNSFW: true})
	if len(got) != 1 || got[0] != "S1E2" {
		t.Fatalf("next up = %v, want [S1E2] (未看完的那一集不能跳过)", got)
	}
}

// 未看完的是这部剧的最后一集时也要返回它，不能因为「后面没有集了」而返回空。
func TestNextUpKeepsPartiallyWatchedFinalEpisode(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "tv")
	watchedAt := time.Now().Add(-time.Minute)

	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 1, nil, true)
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 2, &watchedAt, false) // 最后一集未看完

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	got := nextUpIDs(t, svc, MediaVisibility{IncludeNSFW: true})
	if len(got) != 1 || got[0] != "S1E2" {
		t.Fatalf("next up = %v, want [S1E2]", got)
	}
}

// 电影不进 NextUp：NextUp 的语义是「下一集」，电影由 Resume 接口负责。
func TestNextUpSkipsMovies(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "movie")
	watchedAt := time.Now().Add(-time.Hour)

	seedEpisode(t, repos, libID, "", "电影", 0, 0, &watchedAt, false)

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	if got := nextUpIDs(t, svc, MediaVisibility{IncludeNSFW: true}); len(got) != 0 {
		t.Fatalf("next up = %v, want empty", got)
	}
}

// 同一部剧有多条未看完历史时，只能出一条，且指向最靠后的已看集的下一集。
// 同一部剧有多条未看完历史时只能出一条，且指向最近看过的那一集。
//
// 最近那一集（S1E2）本身还没看完，所以它就是「接下来该看的一集」；
// S1E1 只是更早的中间进度，不能据此跳到 S1E3。
func TestNextUpOneEntryPerSeries(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "tv")
	older := time.Now().Add(-48 * time.Hour)
	newer := time.Now().Add(-2 * time.Hour)

	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 1, &older, false)
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 2, &newer, false)
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 3, nil, false)

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	got := nextUpIDs(t, svc, MediaVisibility{IncludeNSFW: true})
	if len(got) != 1 || got[0] != "S1E2" {
		t.Fatalf("next up = %v, want [S1E2]", got)
	}
}

// 跨季时下一集应是下一季的第一集。
func TestNextUpCrossesSeason(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "tv")
	watchedAt := time.Now().Add(-time.Hour)

	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 12, &watchedAt, true) // 第 1 季最后一集已看完
	seedEpisode(t, repos, libID, "series-1", "剧一", 2, 1, nil, false)
	seedEpisode(t, repos, libID, "series-1", "剧一", 2, 2, nil, false)

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	got := nextUpIDs(t, svc, MediaVisibility{IncludeNSFW: true})
	if len(got) != 1 || got[0] != "S2E1" {
		t.Fatalf("next up = %v, want [S2E1]", got)
	}
}

// 已标记看完的下一集要跳过。
func TestNextUpSkipsCompletedEpisode(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "tv")
	watchedAt := time.Now().Add(-time.Hour)

	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 1, &watchedAt, true) // 已看完，下一集是 S1E3
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 2, nil, true)        // 已看完
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 3, nil, false)

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	got := nextUpIDs(t, svc, MediaVisibility{IncludeNSFW: true})
	if len(got) != 1 || got[0] != "S1E3" {
		t.Fatalf("next up = %v, want [S1E3]", got)
	}
}

// 追到最后一集且已看完时没有下一集，结果为空而不是重复返回最后一集。
func TestNextUpEmptyAtSeriesEnd(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "tv")
	watchedAt := time.Now().Add(-time.Hour)

	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 3, &watchedAt, true)

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	if got := nextUpIDs(t, svc, MediaVisibility{IncludeNSFW: true}); len(got) != 0 {
		t.Fatalf("next up = %v, want empty", got)
	}
}

// 不可见媒体库里的下一集不能被推荐出去。
func TestNextUpRespectsVisibility(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	visibleLib := seedDiscoveryLibrary(t, repos, "tv")
	hiddenLib := seedDiscoveryLibrary(t, repos, "tv")
	watchedAt := time.Now().Add(-time.Hour)

	seedEpisode(t, repos, visibleLib, "series-1", "剧一", 1, 1, &watchedAt, true)
	seedEpisode(t, repos, hiddenLib, "series-1", "剧一", 1, 2, nil, false)

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	got := nextUpIDs(t, svc, MediaVisibility{IncludeNSFW: true, AllowedLibraryIDs: []string{visibleLib}})
	if len(got) != 0 {
		t.Fatalf("next up = %v, want empty (hidden library)", got)
	}
}

// 最后一集看完（completed=true）后，下一部剧仍应出现在 NextUp 中，
// 而不是因为没有 completed=false 的历史而消失（bug 1 回归测试）。
func TestNextUpAfterCompletedEpisode(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "tv")
	watchedAt := time.Now().Add(-time.Hour)

	// S1E1 已看完，S1E2 尚未开始。
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 1, &watchedAt, true)
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 2, nil, false)
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 3, nil, false)

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	got := nextUpIDs(t, svc, MediaVisibility{IncludeNSFW: true})
	if len(got) != 1 || got[0] != "S1E2" {
		t.Fatalf("next up after completed S1E1 = %v, want [S1E2]", got)
	}
}

// 整部剧看完（所有集都 completed=true）时不应出现在 NextUp（没有下一集）。
func TestNextUpSeriesFullyWatchedIsEmpty(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "tv")
	watchedAt := time.Now().Add(-time.Hour)

	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 1, nil, true)
	seedEpisode(t, repos, libID, "series-1", "剧一", 1, 2, &watchedAt, true) // 最近看完的最后一集

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	if got := nextUpIDs(t, svc, MediaVisibility{IncludeNSFW: true}); len(got) != 0 {
		t.Fatalf("next up for fully-watched series = %v, want empty", got)
	}
}

// 多部剧时按最近观看时间排序。
func TestNextUpOrdersByRecency(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "tv")
	older := time.Now().Add(-24 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)

	seedEpisode(t, repos, libID, "series-a", "剧A", 1, 1, &older, false)
	seedEpisode(t, repos, libID, "series-a", "剧A", 1, 2, nil, false)
	seedEpisode(t, repos, libID, "series-b", "剧B", 1, 1, &newer, false)
	seedEpisode(t, repos, libID, "series-b", "剧B", 1, 2, nil, false)

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	rows, err := svc.NextUpCandidates(context.Background(), "user-1", 20, MediaVisibility{IncludeNSFW: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].SeriesID != "series-b" {
		t.Fatalf("first row series = %q, want series-b (most recently watched)", rows[0].SeriesID)
	}
}
