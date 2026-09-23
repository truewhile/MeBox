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
