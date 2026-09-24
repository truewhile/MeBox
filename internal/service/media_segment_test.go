package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func newSegmentServiceFixture(t *testing.T, handler http.HandlerFunc) (*MediaSegmentService, *repository.Container, *int32) {
	t.Helper()
	repos := repository.New(newServiceTestDB(t))
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	svc := NewMediaSegmentService(zap.NewNop(), repos).
		SetIntroDB(NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL))
	return svc, repos, &calls
}

func writeJSONBody(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

// 剧集必须用「剧集级」TMDb id 查询，而 Media.TMDbID 存的是单集自己的 id：
// 刮削写的是 episode 的 tmdb id（见 local_metadata_test.go 的约束）。
func TestQueryIDsUsesSeriesTMDbForEpisodes(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	svc := NewMediaSegmentService(zap.NewNop(), repos)
	ctx := t.Context()

	if err := repos.DB.Create(&model.Series{
		Base: model.Base{ID: "s-1"}, Title: "Breaking Bad", TMDbID: 1396,
	}).Error; err != nil {
		t.Fatal(err)
	}
	episode := &model.Media{
		Base:       model.Base{ID: "ep-1"},
		SeriesID:   "s-1",
		SeasonNum:  1,
		EpisodeNum: 2,
		TMDbID:     4375419, // 单集 id，不是剧集 id
	}
	tmdbID, season, episodeNum := svc.queryIDs(ctx, episode)
	if tmdbID != 1396 {
		t.Fatalf("tmdbID = %d, want the series id 1396 (not the episode id)", tmdbID)
	}
	if season != 1 || episodeNum != 2 {
		t.Fatalf("season/episode = %d/%d, want 1/2", season, episodeNum)
	}
}

func TestQueryIDsForMovieUsesOwnTMDb(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	svc := NewMediaSegmentService(zap.NewNop(), repos)

	tmdbID, season, episode := svc.queryIDs(t.Context(), &model.Media{
		Base: model.Base{ID: "mv-1"}, TMDbID: 27205,
	})
	if tmdbID != 27205 || season != 0 || episode != 0 {
		t.Fatalf("query = (%d,%d,%d), want (27205,0,0)", tmdbID, season, episode)
	}
}

func TestQueryIDsIsNotResolvableBeforeScrape(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	svc := NewMediaSegmentService(zap.NewNop(), repos)
	ctx := t.Context()

	// 剧集还没关联 Series：解析不出来，但也不能当成「查过且没有」。
	if tmdbID, _, _ := svc.queryIDs(ctx, &model.Media{
		Base: model.Base{ID: "ep-orphan"}, SeasonNum: 1, EpisodeNum: 1,
	}); tmdbID != 0 {
		t.Fatalf("tmdbID = %d, want 0", tmdbID)
	}
	// 没刮削过的电影同理。
	if tmdbID, _, _ := svc.queryIDs(ctx, &model.Media{Base: model.Base{ID: "mv-noscrape"}}); tmdbID != 0 {
		t.Fatalf("tmdbID = %d, want 0", tmdbID)
	}
}

// 部分刮削路径（生产环境动漫库实测如此）不建 Series 行，而是把「剧集级」
// TMDb id 直接写在 Media.TMDbID 上：同一剧名下各集共用同一个 id。
// 原先这类行一律解析不出 id，整个动漫库等于查不到任何片段。
func TestQueryIDsFallsBackToMediaTMDbWhenSiblingsShareIt(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	svc := NewMediaSegmentService(zap.NewNop(), repos)
	ctx := t.Context()

	episodes := []*model.Media{
		{Base: model.Base{ID: "ep-6"}, LibraryID: "lib-anime", Title: "便·当", Path: "/anime/ben-to/S01E06.mkv", SeasonNum: 1, EpisodeNum: 6, TMDbID: 61970},
		{Base: model.Base{ID: "ep-7"}, LibraryID: "lib-anime", Title: "便·当", Path: "/anime/ben-to/S01E07.mkv", SeasonNum: 1, EpisodeNum: 7, TMDbID: 61970},
	}
	for _, ep := range episodes {
		if err := repos.DB.Create(ep).Error; err != nil {
			t.Fatal(err)
		}
	}

	tmdbID, season, episode := svc.queryIDs(ctx, episodes[0])
	if tmdbID != 61970 || season != 1 || episode != 6 {
		t.Fatalf("query = (%d,%d,%d), want (61970,1,6): a shared id is a series id", tmdbID, season, episode)
	}
}

// 反例（重要）：另一些刮削路径把「单集自己的」id 写在 Media.TMDbID 上，
// 每集都不同。这种 id 不能当剧集 id 用——拿它去查会命中完全不相干的片子。
func TestQueryIDsRejectsPerEpisodeTMDbWithoutSiblings(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	svc := NewMediaSegmentService(zap.NewNop(), repos)
	ctx := t.Context()

	episodes := []*model.Media{
		{Base: model.Base{ID: "ep-1"}, LibraryID: "lib-tv", Title: "某剧", Path: "/tv/some/S01E01.mkv", SeasonNum: 1, EpisodeNum: 1, TMDbID: 4_375_419},
		{Base: model.Base{ID: "ep-2"}, LibraryID: "lib-tv", Title: "某剧", Path: "/tv/some/S01E02.mkv", SeasonNum: 1, EpisodeNum: 2, TMDbID: 4_375_420},
	}
	for _, ep := range episodes {
		if err := repos.DB.Create(ep).Error; err != nil {
			t.Fatal(err)
		}
	}

	if tmdbID, _, _ := svc.queryIDs(ctx, episodes[0]); tmdbID != 0 {
		t.Fatalf("tmdbID = %d, want 0: a per-episode id must not be used as a series id", tmdbID)
	}
}

// Series 行存在时永远优先，哪怕 Media.TMDbID 看起来也像个共用 id。
func TestQueryIDsPrefersSeriesTMDbOverSharedMediaTMDb(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	svc := NewMediaSegmentService(zap.NewNop(), repos)
	ctx := t.Context()

	if err := repos.DB.Create(&model.Series{
		Base: model.Base{ID: "s-1"}, Title: "便·当", TMDbID: 1396,
	}).Error; err != nil {
		t.Fatal(err)
	}
	episodes := []*model.Media{
		{Base: model.Base{ID: "ep-a"}, SeriesID: "s-1", LibraryID: "lib-anime", Title: "便·当", Path: "/anime/ben-to/S01E06.mkv", SeasonNum: 1, EpisodeNum: 6, TMDbID: 61970},
		{Base: model.Base{ID: "ep-b"}, SeriesID: "s-1", LibraryID: "lib-anime", Title: "便·当", Path: "/anime/ben-to/S01E07.mkv", SeasonNum: 1, EpisodeNum: 7, TMDbID: 61970},
	}
	for _, ep := range episodes {
		if err := repos.DB.Create(ep).Error; err != nil {
			t.Fatal(err)
		}
	}

	if tmdbID, _, _ := svc.queryIDs(ctx, episodes[0]); tmdbID != 1396 {
		t.Fatalf("tmdbID = %d, want the Series id 1396", tmdbID)
	}
}

// 关联了 Series 但那条 Series 没刮到 id 时，仍然走 Media.TMDbID 兜底。
func TestQueryIDsFallsBackWhenSeriesHasNoTMDb(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	svc := NewMediaSegmentService(zap.NewNop(), repos)
	ctx := t.Context()

	if err := repos.DB.Create(&model.Series{
		Base: model.Base{ID: "s-2"}, Title: "便·当", TMDbID: 0,
	}).Error; err != nil {
		t.Fatal(err)
	}
	episodes := []*model.Media{
		{Base: model.Base{ID: "ep-c"}, SeriesID: "s-2", Path: "/anime/ben-to/S01E06.mkv", SeasonNum: 1, EpisodeNum: 6, TMDbID: 61970},
		{Base: model.Base{ID: "ep-d"}, SeriesID: "s-2", Path: "/anime/ben-to/S01E07.mkv", SeasonNum: 1, EpisodeNum: 7, TMDbID: 61970},
	}
	for _, ep := range episodes {
		if err := repos.DB.Create(ep).Error; err != nil {
			t.Fatal(err)
		}
	}

	if tmdbID, _, _ := svc.queryIDs(ctx, episodes[0]); tmdbID != 61970 {
		t.Fatalf("tmdbID = %d, want the shared Media id 61970", tmdbID)
	}
}

func TestListForPlaybackQueriesEpisodesWithSharedSeriesTMDb(t *testing.T) {
	svc, repos, calls := newSegmentServiceFixture(t, writeJSONBody(introDBTVPayload))
	ctx := t.Context()
	episodes := []*model.Media{
		{Base: model.Base{ID: "ep-x"}, LibraryID: "lib-anime", Title: "便·当",
			Path: "/anime/ben-to/S01E06.mkv", SeasonNum: 1, EpisodeNum: 6, TMDbID: 61970},
		{Base: model.Base{ID: "ep-y"}, LibraryID: "lib-anime", Title: "便·当",
			Path: "/anime/ben-to/S01E07.mkv", SeasonNum: 1, EpisodeNum: 7, TMDbID: 61970},
	}
	for _, ep := range episodes {
		if err := repos.DB.Create(ep).Error; err != nil {
			t.Fatal(err)
		}
	}

	rows, err := svc.ListForPlayback(ctx, episodes[0])
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want the two spans the provider returned", rows)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
}

func TestListForPlaybackFetchesOnceThenServesCache(t *testing.T) {
	svc, repos, calls := newSegmentServiceFixture(t, writeJSONBody(introDBMoviePayload))
	ctx := t.Context()
	m := &model.Media{Base: model.Base{ID: "mv-1"}, Path: "/movies/inception.mkv", TMDbID: 27205}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		rows, err := svc.ListForPlayback(ctx, m)
		if err != nil {
			t.Fatalf("call #%d: %v", i+1, err)
		}
		if len(rows) != 1 {
			t.Fatalf("call #%d rows = %#v, want 1", i+1, rows)
		}
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (later plays must hit the local cache)", got)
	}
	got, err := repos.MediaSegment.ListByMedia(ctx, "mv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != model.SegmentKindIntro || got[0].StartMs != 0 || got[0].EndMs != 38_000 {
		t.Fatalf("persisted rows = %#v", got)
	}
	if got[0].Source != IntroDBSource {
		t.Fatalf("source = %q, want %q", got[0].Source, IntroDBSource)
	}
}

func TestListForPlaybackCachesMisses(t *testing.T) {
	svc, repos, calls := newSegmentServiceFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	ctx := t.Context()
	m := &model.Media{Base: model.Base{ID: "mv-2"}, Path: "/movies/nobody-knows.mkv", TMDbID: 424242}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		rows, err := svc.ListForPlayback(ctx, m)
		if err != nil {
			t.Fatalf("call #%d: %v", i+1, err)
		}
		if len(rows) != 0 {
			t.Fatalf("call #%d rows = %#v, want none", i+1, rows)
		}
	}
	// 负缓存是必需的：否则每次播放这部片都会重新打一次外网。
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (a miss must be cached too)", got)
	}
	ledger, err := repos.MediaSegment.GetFetch(ctx, "mv-2", IntroDBSource)
	if err != nil {
		t.Fatal(err)
	}
	if ledger == nil || ledger.Found {
		t.Fatalf("ledger = %#v, want a recorded miss", ledger)
	}
}

func TestListForPlaybackSkipsProviderWithoutExternalID(t *testing.T) {
	svc, repos, calls := newSegmentServiceFixture(t, writeJSONBody(introDBMoviePayload))
	ctx := t.Context()
	m := &model.Media{Base: model.Base{ID: "mv-3"}, Path: "/movies/unscraped.mkv"}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := svc.ListForPlayback(ctx, m); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("provider calls = %d, want 0 without a tmdb id", got)
	}
	// 关键：解析不出外部 ID 时不能写负缓存，否则刮削完成后就永远不会再查了。
	ledger, err := repos.MediaSegment.GetFetch(ctx, "mv-3", IntroDBSource)
	if err != nil {
		t.Fatal(err)
	}
	if ledger != nil {
		t.Fatalf("ledger = %#v, want none while metadata is still missing", ledger)
	}
}

func TestListForPlaybackKeepsCacheWhenProviderFails(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(introDBMoviePayload))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	repos := repository.New(newServiceTestDB(t))
	svc := NewMediaSegmentService(zap.NewNop(), repos).
		SetIntroDB(NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL))
	ctx := t.Context()
	m := &model.Media{Base: model.Base{ID: "mv-4"}, Path: "/movies/flaky.mkv", TMDbID: 27205}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}

	if rows, err := svc.ListForPlayback(ctx, m); err != nil || len(rows) != 1 {
		t.Fatalf("first call rows=%#v err=%v", rows, err)
	}
	// 让缓存过期，制造一次会失败的刷新。
	if err := repos.DB.Model(&model.MediaSegmentFetch{}).
		Where("media_id = ?", "mv-4").
		Update("fetched_at", time.Now().Add(-segmentFoundTTL-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := svc.ListForPlayback(ctx, m)
	if err != nil {
		t.Fatalf("provider failure must not surface as an error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want the previous cache kept", rows)
	}
}

func TestListForPlaybackRespectsCallerDeadline(t *testing.T) {
	// 第三方客户端（Emby）会在起播路径上同步请求片段，它给的超时必须生效，
	// 不能被一次外网抓取拖住；同时超时不能变成「负缓存」，否则就再也补不上了。
	svc, repos, calls := newSegmentServiceFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(introDBMoviePayload))
	})
	ctx := t.Context()
	m := &model.Media{Base: model.Base{ID: "mv-5"}, Path: "/movies/budget.mkv", TMDbID: 27205}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}

	budgeted, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	rows, err := svc.ListForPlayback(budgeted, m)
	if err != nil {
		t.Fatalf("an exhausted fetch budget must not surface as an error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want none when the caller's budget ran out", rows)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (the attempt was made then abandoned)", got)
	}
	ledger, err := repos.MediaSegment.GetFetch(ctx, "mv-5", IntroDBSource)
	if err != nil {
		t.Fatal(err)
	}
	if ledger != nil {
		t.Fatal("a timed-out fetch must not be recorded as a negative cache entry")
	}

	// 预算正常时（下一次播放）仍然能补上。
	rows, err = svc.ListForPlayback(ctx, m)
	if err != nil || len(rows) != 1 {
		t.Fatalf("second call rows=%#v err=%v, want the fetched segment", rows, err)
	}
}

func TestPreferSegmentsBySourcePrefersManualThenIntroDBThenPropagated(t *testing.T) {
	rows := []model.MediaSegment{
		{Kind: model.SegmentKindIntro, StartMs: 1, EndMs: 2, Source: SegmentSourcePropagated},
		{Kind: model.SegmentKindIntro, StartMs: 10, EndMs: 20, Source: IntroDBSource},
		{Kind: model.SegmentKindIntro, StartMs: 100, EndMs: 200, Source: SegmentSourceManual},
		{Kind: model.SegmentKindCredits, StartMs: 1000, EndMs: 0, Source: SegmentSourcePropagated},
		{Kind: model.SegmentKindCredits, StartMs: 2000, EndMs: 0, Source: IntroDBSource},
	}
	got := preferSegmentsBySource(rows)
	if len(got) != 2 {
		t.Fatalf("got %#v, want manual intro + introdb credits", got)
	}
	if got[0].Source != SegmentSourceManual || got[0].StartMs != 100 {
		t.Fatalf("intro = %#v, want manual", got[0])
	}
	if got[1].Source != IntroDBSource || got[1].StartMs != 2000 {
		t.Fatalf("credits = %#v, want theintrodb", got[1])
	}
}

func TestListForPlaybackPropagatesIntroToSeasonSiblings(t *testing.T) {
	svc, repos, _ := newSegmentServiceFixture(t, writeJSONBody(introDBTVPayload))
	svc.SetPrewarmGap(0)
	ctx := t.Context()
	episodes := []*model.Media{
		{Base: model.Base{ID: "ep-1"}, LibraryID: "lib-anime", Title: "便·当",
			Path: "/anime/S01E01.mkv", SeasonNum: 1, EpisodeNum: 1, TMDbID: 61970},
		{Base: model.Base{ID: "ep-2"}, LibraryID: "lib-anime", Title: "便·当",
			Path: "/anime/S01E02.mkv", SeasonNum: 1, EpisodeNum: 2, TMDbID: 61970},
	}
	for _, ep := range episodes {
		if err := repos.DB.Create(ep).Error; err != nil {
			t.Fatal(err)
		}
	}

	rows, err := svc.ListForPlayback(ctx, episodes[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("ep1 rows = %#v, want intro+credits from provider", rows)
	}

	sib, err := repos.MediaSegment.ListByMediaSource(ctx, "ep-2", SegmentSourcePropagated)
	if err != nil {
		t.Fatal(err)
	}
	if len(sib) != 1 || sib[0].Kind != model.SegmentKindIntro {
		t.Fatalf("sibling propagated = %#v, want intro only", sib)
	}
	if sib[0].StartMs != 228_664 || sib[0].EndMs != 246_143 {
		t.Fatalf("propagated window = %#v", sib[0])
	}

	// 兄弟集播放时应直接看到传播来的 intro（即使自己还没打过 IntroDB）。
	if err := repos.MediaSegment.UpsertFetch(ctx, &model.MediaSegmentFetch{
		MediaID: "ep-2", Source: IntroDBSource, FetchedAt: time.Now(), Found: false,
	}); err != nil {
		t.Fatal(err)
	}
	merged, err := svc.ListForPlayback(ctx, episodes[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 || merged[0].Kind != model.SegmentKindIntro || merged[0].Source != SegmentSourcePropagated {
		t.Fatalf("ep2 playback = %#v, want propagated intro", merged)
	}
}

func TestPrewarmFetchesStaleMissesPreferringRecentPlays(t *testing.T) {
	svc, repos, calls := newSegmentServiceFixture(t, writeJSONBody(introDBMoviePayload))
	svc.SetPrewarmGap(0)
	ctx := t.Context()

	old := &model.Media{Base: model.Base{ID: "mv-old"}, Path: "/a.mkv", TMDbID: 111}
	hot := &model.Media{Base: model.Base{ID: "mv-hot"}, Path: "/b.mkv", TMDbID: 27205}
	for _, m := range []*model.Media{old, hot} {
		if err := repos.DB.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 两条都是过期 miss，热播的应优先被预热。
	stale := time.Now().Add(-segmentMissingTTL - time.Hour)
	for _, id := range []string{"mv-old", "mv-hot"} {
		if err := repos.MediaSegment.UpsertFetch(ctx, &model.MediaSegmentFetch{
			MediaID: id, Source: IntroDBSource, FetchedAt: stale, Found: false,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repos.DB.Create(&model.PlaybackHistory{
		Base: model.Base{ID: "h-1"}, UserID: "u1", MediaID: "mv-hot",
	}).Error; err != nil {
		t.Fatal(err)
	}

	n, err := svc.Prewarm(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("attempted = %d, want 1", n)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
	ledger, err := repos.MediaSegment.GetFetch(ctx, "mv-hot", IntroDBSource)
	if err != nil || ledger == nil || !ledger.Found {
		t.Fatalf("hot ledger = %#v err=%v, want found", ledger, err)
	}
	oldLedger, err := repos.MediaSegment.GetFetch(ctx, "mv-old", IntroDBSource)
	if err != nil || oldLedger == nil || oldLedger.Found {
		t.Fatalf("old ledger should remain a miss, got %#v err=%v", oldLedger, err)
	}
}

func TestLedgerFreshUsesLongerTTLWhenDataWasFound(t *testing.T) {
	now := time.Now()
	found := &model.MediaSegmentFetch{FetchedAt: now.Add(-segmentMissingTTL), Found: true}
	if !ledgerFresh(found) {
		t.Fatal("a hit should still be fresh just past the miss TTL")
	}
	miss := &model.MediaSegmentFetch{FetchedAt: now.Add(-segmentMissingTTL), Found: false}
	if ledgerFresh(miss) {
		t.Fatal("a miss should expire after the miss TTL")
	}
	if ledgerFresh(nil) {
		t.Fatal("a missing ledger must not be considered fresh")
	}
}

