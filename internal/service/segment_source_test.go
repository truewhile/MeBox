package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// stubIMDbResolver 返回固定 IMDb id，用于验证 IntroDB 的 id 解析链路。
type stubIMDbResolver struct {
	id    string
	calls int32
}

func (s *stubIMDbResolver) ResolveIMDbID(_ context.Context, _ int, _ bool) (string, error) {
	atomic.AddInt32(&s.calls, 1)
	return s.id, nil
}

func TestSegmentSourceOffSkipsExternalLookup(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(introDBMoviePayload))
	}))
	defer server.Close()

	svc := NewMediaSegmentService(zap.NewNop(), repos).
		SetIntroDB(NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL))
	if err := repos.Setting.Set(t.Context(), SegmentSourceSettingKey, SegmentSourceOff); err != nil {
		t.Fatal(err)
	}
	m := &model.Media{Base: model.Base{ID: "mv-off"}, Path: "/movies/off.mkv", TMDbID: 27205}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}

	rows, err := svc.ListForPlayback(t.Context(), m)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 || atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("rows = %#v calls = %d, want no lookup when the source is off", rows, calls)
	}
}

// 默认（未配置设置项）必须保持历史行为：用 TheIntroDB。
func TestSegmentSourceDefaultsToTheIntroDB(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(introDBMoviePayload))
	}))
	defer server.Close()

	svc := NewMediaSegmentService(zap.NewNop(), repos).
		SetIntroDB(NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL))
	m := &model.Media{Base: model.Base{ID: "mv-default"}, Path: "/movies/default.mkv", TMDbID: 27205}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}

	rows, err := svc.ListForPlayback(t.Context(), m)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("rows = %#v calls = %d, want the default provider to run", rows, calls)
	}
}

// 切到 TODb 后，TheIntroDB 不应再被调用，写入的 source 也要跟着变。
func TestSegmentSourceSwitchesToTODb(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	var introCalls, todbCalls int32
	introServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&introCalls, 1)
		_, _ = w.Write([]byte(introDBMoviePayload))
	}))
	defer introServer.Close()
	todbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&todbCalls, 1)
		_, _ = w.Write([]byte(`{"items":[{"marker":[{"marker_type":"intro","time_start":5,"time_end":30}]}]}`))
	}))
	defer todbServer.Close()

	svc := NewMediaSegmentService(zap.NewNop(), repos).
		SetIntroDB(NewIntroDBService(zap.NewNop()).SetBaseURL(introServer.URL)).
		SetTODb(NewTODbService(zap.NewNop()).SetBaseURL(todbServer.URL))
	if err := repos.Setting.Set(t.Context(), SegmentSourceSettingKey, TODbSource); err != nil {
		t.Fatal(err)
	}
	m := &model.Media{Base: model.Base{ID: "mv-todb"}, Path: "/movies/todb.mkv", TMDbID: 27205}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}

	rows, err := svc.ListForPlayback(t.Context(), m)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if atomic.LoadInt32(&introCalls) != 0 {
		t.Fatalf("theintrodb was called %d times, want 0 after switching source", introCalls)
	}
	if atomic.LoadInt32(&todbCalls) != 1 {
		t.Fatalf("todb calls = %d, want 1", todbCalls)
	}
	if len(rows) != 1 || rows[0].Source != TODbSource {
		t.Fatalf("rows = %#v, want one row tagged %s", rows, TODbSource)
	}
	if rows[0].StartMs != 5000 || rows[0].EndMs != 30000 {
		t.Fatalf("row = %#v, want 5000-30000ms", rows[0])
	}
}

// 未知的源标识（降级回滚残留）要退回默认源，而不是静默失效。
func TestSegmentSourceFallsBackForUnknownValue(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(introDBMoviePayload))
	}))
	defer server.Close()

	svc := NewMediaSegmentService(zap.NewNop(), repos).
		SetIntroDB(NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL))
	if err := repos.Setting.Set(t.Context(), SegmentSourceSettingKey, "no-such-source"); err != nil {
		t.Fatal(err)
	}
	m := &model.Media{Base: model.Base{ID: "mv-unknown"}, Path: "/movies/u.mkv", TMDbID: 27205}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListForPlayback(t.Context(), m); err != nil {
		t.Fatalf("list: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("calls = %d, want the default provider to still run", calls)
	}
}

// IntroDB 需要 IMDb id；解析不出来时必须跳过请求，且不能写负缓存
// （否则元数据补齐之后也永远不会再查）。
func TestSegmentIntroDBSkipsWhenIMDbUnavailable(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	svc := NewMediaSegmentService(zap.NewNop(), repos).
		SetIntroDBApp(NewIntroDBAppService(zap.NewNop()).SetBaseURL(server.URL)).
		SetIMDbResolver(&stubIMDbResolver{id: ""})
	if err := repos.Setting.Set(t.Context(), SegmentSourceSettingKey, IntroDBAppSource); err != nil {
		t.Fatal(err)
	}
	m := &model.Media{Base: model.Base{ID: "mv-noimdb"}, Path: "/movies/noimdb.mkv", TMDbID: 27205}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}

	rows, err := svc.ListForPlayback(t.Context(), m)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 || atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("rows = %#v calls = %d, want no request without an imdb id", rows, calls)
	}
	ledger, err := repos.MediaSegment.GetFetch(t.Context(), "mv-noimdb", IntroDBAppSource)
	if err != nil {
		t.Fatal(err)
	}
	if ledger != nil {
		t.Fatalf("ledger = %#v, want no negative cache entry", ledger)
	}
}
