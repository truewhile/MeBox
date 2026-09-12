package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func newDanmakuTestService(t *testing.T) *DanmakuService {
	t.Helper()
	// 独立临时文件库，避免测试间通过共享内存库串数据。
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "danmaku-test.db")), &gorm.Config{})
	require.NoError(t, err)
	// 需要 users 表：弹幕合并偏好按用户存储在 user 行上。
	require.NoError(t, db.AutoMigrate(&model.Setting{}, &model.Media{}, &model.User{}))
	repos := repository.New(db)
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return NewDanmakuService(zap.NewNop(), repos)
}

// seedDanmakuMedia inserts a media row so the service can resolve a name and
// episode number.
func seedDanmakuMedia(t *testing.T, svc *DanmakuService, id, title, originalName string, episodeNum int) {
	t.Helper()
	m := model.Media{Title: title}
	if id != "" {
		m.ID = id
	}
	if originalName != "" {
		m.OriginalName = originalName
	}
	if episodeNum > 0 {
		m.EpisodeNum = episodeNum
	}
	require.NoError(t, svc.repo.DB.Create(&m).Error)
}

// danmakuSourceServer records the search request (name + episode params) and
// serves episodes + Bilibili-format XML comments, following the dandanplay
// protocol. The search payload can be customized per test (default: one anime,
// one episode).
type danmakuSourceServer struct {
	server         *httptest.Server
	lastSearch     string // full query (anime=...&episode=...)
	searchResponse string // JSON body served for /api/v2/search/episodes
}

func newDanmakuSourceServer(t *testing.T) *danmakuSourceServer {
	return newDanmakuSourceServerWithSearch(
		t,
		`{"hasMore":false,"animes":[{"animeId":1001,"animeTitle":"测试动画","episodes":[{"episodeId":25484,"episodeTitle":"第1话"}]}]}`,
	)
}

func newDanmakuSourceServerWithSearch(t *testing.T, searchResponse string) *danmakuSourceServer {
	t.Helper()
	ds := &danmakuSourceServer{searchResponse: searchResponse}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		ds.lastSearch = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, ds.searchResponse)
	})
	mux.HandleFunc("/api/v2/comment/25484", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><i><d p="0.5,1,16777215,user1">弹幕A</d><d p="1.0,5,255,user2">弹幕B</d><d p="1.5,4,65280,user3">弹幕C</d></i>`)
	})
	mux.HandleFunc("/api/v2/comment/99999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><i><d p="2.0,1,16777215,userX">显式指定弹幕</d></i>`)
	})
	ds.server = httptest.NewServer(mux)
	t.Cleanup(ds.server.Close)
	return ds
}

func (ds *danmakuSourceServer) URL() string { return ds.server.URL }

func TestDanmakuConfigDefaults(t *testing.T) {
	svc := newDanmakuTestService(t)
	cfg := svc.Config(context.Background())
	require.True(t, cfg.Enabled)
	require.Equal(t, "1", cfg.Opacity)
	require.Equal(t, "24", cfg.FontSize)
	require.Equal(t, "1", cfg.Area)
	require.Empty(t, cfg.Source)
}

func TestDanmakuConfigReadsPersistedValues(t *testing.T) {
	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuEnabledKey, "false"))
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, "https://dm.example.com"))
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuOpacityKey, "0.7"))

	cfg := svc.Config(ctx)
	require.False(t, cfg.Enabled)
	require.Equal(t, "https://dm.example.com", cfg.Source)
	require.Equal(t, "0.7", cfg.Opacity)
}

func TestDanmakuFetchDisablesWhenToggleOff(t *testing.T) {
	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuEnabledKey, "false"))
	seedDanmakuMedia(t, svc, "m1", "测试动画", "", 0)

	res, err := svc.Fetch(ctx, "m1", "", "")
	require.NoError(t, err)
	require.False(t, res.Enabled)
	require.Empty(t, res.Raw)
	// 禁用时无弹幕数据可探测，source_type 保持未知（交给播放器兜底）。
	require.Equal(t, "auto", res.SourceType)
}

func TestDanmakuFetchWithDandanplaySource(t *testing.T) {
	srv := newDanmakuSourceServer(t)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, srv.URL()))
	seedDanmakuMedia(t, svc, "m2", "测试动画", "", 0)

	res, err := svc.Fetch(ctx, "m2", "", "")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Equal(t, "xml", res.SourceType)
	require.Contains(t, res.Raw, "弹幕A")
	require.Contains(t, res.Raw, `p="0.5,1,16777215,user1"`)
	require.Equal(t, "测试动画", res.AnimeTitle)
	require.Equal(t, "第1话", res.EpisodeTitle)
	require.Equal(t, int64(25484), res.EpisodeID)
	require.Equal(t, "search", res.MatchMode)
}

func TestDanmakuFetchUsesOriginalNameForSearch(t *testing.T) {
	srv := newDanmakuSourceServer(t)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, srv.URL()))
	seedDanmakuMedia(t, svc, "m3", "刮削标题A", "日文原名B", 0)

	res, err := svc.Fetch(ctx, "m3", "", "")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Contains(t, res.Raw, "弹幕A")
	// 集数为 0 时不附加 episode 过滤参数。
	require.NotContains(t, srv.lastSearch, "episode=")
}

func TestDanmakuFetchSearchesByEpisodeNumber(t *testing.T) {
	srv := newDanmakuSourceServer(t)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, srv.URL()))
	// 第 3 话：搜索时应带 episode=3，命中该集弹幕库。
	seedDanmakuMedia(t, svc, "m2", "测试动画", "", 3)

	res, err := svc.Fetch(ctx, "m2", "", "")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Contains(t, srv.lastSearch, "anime=")
	require.Contains(t, srv.lastSearch, "episode=3")
	require.Contains(t, res.Raw, "弹幕A")
}

func TestDanmakuFetchKeywordKeepsEpisode(t *testing.T) {
	srv := newDanmakuSourceServer(t)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, srv.URL()))
	seedDanmakuMedia(t, svc, "m5", "测试动画", "", 5)

	// 手动搜索关键词时保留媒体集数，仍只命中该集。
	res, err := svc.Fetch(ctx, "m5", "另一个名字", "")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Contains(t, srv.lastSearch, "anime=")
	require.Contains(t, srv.lastSearch, "episode=5")
}

func TestDanmakuFetchUsesDefaultSourceWhenEmpty(t *testing.T) {
	require.Equal(t, DanmakuDefaultSource, "https://api.dandanplay.net")
}

func TestDanmakuFetchHandlesSearch404(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 配置源 404 会回退官方，官方同样 404 才能稳定复现错误。
	official := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer official.Close()
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, srv.URL))
	seedDanmakuMedia(t, svc, "m4", "测试动画", "", 0)

	res, err := svc.Fetch(ctx, "m4", "", "")
	require.Error(t, err)
	require.True(t, res.Enabled)
}

// 多部番剧命中时返回候选列表（disambiguation），不擅自选第一个。
func TestDanmakuFetchReturnsCandidatesOnAmbiguity(t *testing.T) {
	srv := newDanmakuSourceServerWithSearch(
		t,
		`{"hasMore":false,"animes":[`+
			`{"animeId":1001,"animeTitle":"测试动画","episodes":[{"episodeId":25484,"episodeTitle":"第1话"}]},`+
			`{"animeId":2002,"animeTitle":"测试动画 剧场版","episodes":[{"episodeId":30001,"episodeTitle":"正片"}]}`+
			`]}`,
	)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, srv.URL()))
	seedDanmakuMedia(t, svc, "m6", "测试动画", "", 0)

	res, err := svc.Fetch(ctx, "m6", "", "")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	// 歧义时不取弹幕，返回候选交由播放器选择。
	require.Empty(t, res.Raw)
	require.Len(t, res.Candidates, 2)
	require.Equal(t, int64(1001), res.Candidates[0].AnimeID)
	require.Equal(t, "测试动画", res.Candidates[0].AnimeTitle)
	require.Len(t, res.Candidates[0].Episodes, 1)
	require.Equal(t, int64(25484), res.Candidates[0].Episodes[0].EpisodeID)
}

// 显式指定 episodeId 时跳过搜索，直接拉取该弹幕库。
func TestDanmakuFetchWithExplicitEpisodeID(t *testing.T) {
	srv := newDanmakuSourceServer(t)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, srv.URL()))
	// 媒体名与搜索无关也能用显式 episodeId 命中。
	res, err := svc.Fetch(ctx, "m2", "", "99999")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Contains(t, res.Raw, "显式指定弹幕")
	require.Empty(t, res.Candidates)
	require.Equal(t, int64(99999), res.EpisodeID)
	require.Equal(t, "manual", res.MatchMode)
}
func TestDetectDanmakuSourceType(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`<?xml version="1.0"?><i><d p="1,1,25,16777215,x,y,z,w">text</d></i>`, "xml"},
		{`<d p="1,1,16777215,u">text</d>`, "xml"},
		{`{"count":2,"comments":[{"p":"0.00,1,16777215,[u]","m":"hi","t":0}]}`, "json"},
		{`[{"time":1,"text":"x"}]`, "json"},
		{"", "auto"},
		{"   \n  ", "auto"},
	}
	for _, c := range cases {
		if got := detectDanmakuSourceType(c.raw); got != c.want {
			t.Errorf("detectDanmakuSourceType(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// 弹弹play JSON 源：comment 接口返回 {"count":N,"comments":[{p,m,t}]}，
// Fetch 应自动探测 source_type=json 并原样透传。
func TestDanmakuFetchDetectsJSONSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/search/episodes":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"hasMore":false,"animes":[{"animeId":1001,"animeTitle":"测试动画","episodes":[{"episodeId":25484,"episodeTitle":"第1话"}]}]}`)
		case "/api/v2/comment/25484":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"count":2,"comments":[{"cid":1,"p":"0.00,1,16777215,[u]","m":"第一弹","t":0},{"cid":2,"p":"3.50,5,16711680,[u]","m":"顶部弹幕","t":3.5}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, srv.URL))
	seedDanmakuMedia(t, svc, "mjson", "测试动画", "", 0)

	res, err := svc.Fetch(ctx, "mjson", "", "")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Equal(t, "json", res.SourceType)
	require.Contains(t, res.Raw, `"m":"第一弹"`)
	require.Empty(t, res.Candidates)

	// XML 源仍按 XML 探测。
	srvXML := newDanmakuSourceServer(t)
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, srvXML.URL()))
	res2, err := svc.Fetch(ctx, "mjson", "", "")
	require.NoError(t, err)
	require.Equal(t, "xml", res2.SourceType)
	require.Contains(t, res2.Raw, "弹幕A")
}

// 同一集在缓存 TTL 内重复请求时不应再访问上游（搜索和评论都只发一次）。
func TestDanmakuFetchCachesRepeatedRequests(t *testing.T) {
	var searchCalls, commentCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&searchCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hasMore":false,"animes":[{"animeId":1001,"animeTitle":"测试动画","episodes":[{"episodeId":25484,"episodeTitle":"第1话"}]}]}`)
	})
	mux.HandleFunc("/api/v2/comment/25484", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&commentCalls, 1)
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">缓存测试</d></i>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, srv.URL))
	seedDanmakuMedia(t, svc, "cache-media", "测试动画", "", 1)

	first, err := svc.Fetch(ctx, "cache-media", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, first.Raw)

	second, err := svc.Fetch(ctx, "cache-media", "", "")
	require.NoError(t, err)
	require.Equal(t, first.Raw, second.Raw)
	require.EqualValues(t, 1, atomic.LoadInt32(&searchCalls))
	require.EqualValues(t, 1, atomic.LoadInt32(&commentCalls))
}

// 并发的同一集请求应被 singleflight 合并，上游只被访问一次。
func TestDanmakuFetchCoalescesConcurrentRequests(t *testing.T) {
	var searchCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&searchCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hasMore":false,"animes":[{"animeId":1001,"animeTitle":"测试动画","episodes":[{"episodeId":25484,"episodeTitle":"第1话"}]}]}`)
	})
	mux.HandleFunc("/api/v2/comment/25484", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">并发测试</d></i>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, srv.URL))
	seedDanmakuMedia(t, svc, "concurrent-media", "测试动画", "", 1)

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Fetch(ctx, "concurrent-media", "", "")
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, atomic.LoadInt32(&searchCalls))
}
