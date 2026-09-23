package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// segmentSourceFixture 提供一个带社区库假服务端、可选探测桩、以及一条已有本地
// 文件的媒体行，用来验证「数据来源选择」这一层。
type segmentSourceFixture struct {
	svc    *MediaSegmentService
	repos  *repository.Container
	calls  *int32
	prober *stubProber
	media  *model.Media
}

func newSegmentSourceFixture(t *testing.T, prober *stubProber) *segmentSourceFixture {
	t.Helper()
	repos := repository.New(newServiceTestDB(t))
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(introDBMoviePayload))
	}))
	t.Cleanup(server.Close)

	svc := NewMediaSegmentService(zap.NewNop(), repos).
		SetIntroDB(NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL).SetRetryDelay(0))

	dir := t.TempDir()
	path := filepath.Join(dir, "inception.mkv")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model.Media{Base: model.Base{ID: "mv-1"}, Title: "盗梦空间", Path: path, TMDbID: 27205}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}

	if prober != nil {
		probeSvc := NewMediaProbeService(zap.NewNop(), repos, nil)
		probeSvc.probe = prober
		svc.SetProbe(probeSvc)
	}
	return &segmentSourceFixture{svc: svc, repos: repos, calls: &calls, prober: prober, media: m}
}

// seedChapters 模拟「已经提取过并且拿到了章节」：写入章节区间 + 探测行（探测行
// 是「已经探过」的标记，客户端靠它停止轮询）。
func (f *segmentSourceFixture) seedChapters(t *testing.T, rows ...model.MediaSegment) {
	t.Helper()
	seeded := make([]model.MediaSegment, 0, len(rows))
	for _, row := range rows {
		row.MediaID = f.media.ID
		row.Source = SegmentSourceFFprobe
		seeded = append(seeded, row)
	}
	if err := f.repos.MediaSegment.ReplaceForMedia(t.Context(), f.media.ID, SegmentSourceFFprobe, seeded); err != nil {
		t.Fatal(err)
	}
	if err := f.repos.MediaProbe.Upsert(t.Context(), &model.MediaProbe{
		MediaID:      f.media.ID,
		ProbedAt:     time.Now(),
		ChapterCount: len(seeded),
	}); err != nil {
		t.Fatal(err)
	}
}

func chapterIntroRow() model.MediaSegment {
	return model.MediaSegment{Kind: model.SegmentKindIntro, StartMs: 228_664, EndMs: 246_143}
}

// auto 档在有章节时必须整体采用章节，并且不打社区库。
func TestSegmentsForPlaybackAutoPrefersChapters(t *testing.T) {
	f := newSegmentSourceFixture(t, &stubProber{result: chapterProbeResult()})
	f.seedChapters(t, chapterIntroRow())

	for _, source := range []string{SegmentSourceAuto, SegmentSourceFFprobe, "未知值"} {
		result, err := f.svc.SegmentsForPlayback(t.Context(), f.media, source)
		if err != nil {
			t.Fatalf("source %q: %v", source, err)
		}
		if len(result.Segments) != 1 || result.Segments[0].StartMs != 228_664 {
			t.Fatalf("source %q segments = %#v, want the chapter row", source, result.Segments)
		}
		if result.Segments[0].Source != SegmentSourceFFprobe {
			t.Fatalf("source %q returned a %q row", source, result.Segments[0].Source)
		}
		if result.Pending {
			t.Fatalf("source %q reported pending although the probe is cached", source)
		}
	}
	if got := atomic.LoadInt32(f.calls); got != 0 {
		t.Fatalf("provider calls = %d, want 0: chapters must not trigger TheIntroDB", got)
	}
}

// theintrodb 档必须忽略章节，并且不触发任何探测。
func TestSegmentsForPlaybackIntroDBOnlyIgnoresChapters(t *testing.T) {
	prober := &stubProber{result: chapterProbeResult()}
	f := newSegmentSourceFixture(t, prober)
	f.seedChapters(t, chapterIntroRow())

	result, err := f.svc.SegmentsForPlayback(t.Context(), f.media, SegmentSourceTheIntroDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Segments) != 1 || result.Segments[0].Source != IntroDBSource {
		t.Fatalf("segments = %#v, want the community row", result.Segments)
	}
	if result.Segments[0].StartMs != 0 || result.Segments[0].EndMs != 38_000 {
		t.Fatalf("segments = %#v, want the movie intro from the provider", result.Segments)
	}
	if result.Pending {
		t.Fatal("theintrodb-only must never report pending")
	}
	// 只用社区库时不该为一次用不上的章节提取去跑 3 秒 ffprobe。
	if got := prober.callCount(); got != 0 {
		t.Fatalf("probe calls = %d, want 0", got)
	}
}

// auto 档没有章节时回落到社区库，同时告诉客户端「章节还在提取」。
func TestSegmentsForPlaybackAutoFallsBackAndReportsPending(t *testing.T) {
	prober := &stubProber{result: chapterProbeResult()}
	f := newSegmentSourceFixture(t, prober)

	result, err := f.svc.SegmentsForPlayback(t.Context(), f.media, SegmentSourceAuto)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Segments) != 1 || result.Segments[0].Source != IntroDBSource {
		t.Fatalf("segments = %#v, want the community fallback", result.Segments)
	}
	if !result.Pending {
		t.Fatal("auto without chapters must report that a probe is running")
	}
	// 异步提取确实排上了队（这里只验证调度；落库由 media_probe_test.go 覆盖）。
	waitForCondition(t, 5*time.Second, func() bool { return prober.callCount() >= 1 })
}

// ffprobe 档没有章节数据时返回空，但会起一次提取并报告 pending。
func TestSegmentsForPlaybackFFprobeOnlyReturnsEmptyWhilePending(t *testing.T) {
	prober := &stubProber{result: chapterProbeResult()}
	f := newSegmentSourceFixture(t, prober)

	result, err := f.svc.SegmentsForPlayback(t.Context(), f.media, SegmentSourceFFprobe)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Segments) != 0 {
		t.Fatalf("segments = %#v, want none before extraction finishes", result.Segments)
	}
	if !result.Pending {
		t.Fatal("ffprobe-only must report pending while the extraction runs")
	}
	if got := atomic.LoadInt32(f.calls); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

// 提取失败也要有个结果：不能在每次播放时无限重试，客户端也不该一直轮询。
func TestSegmentsForPlaybackStopsPendingAfterFailedProbe(t *testing.T) {
	prober := &stubProber{result: chapterProbeResult()}
	f := newSegmentSourceFixture(t, prober)
	if err := f.repos.MediaProbe.MarkFailure(t.Context(), f.media.ID, "probe exploded", time.Now()); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.SegmentsForPlayback(t.Context(), f.media, SegmentSourceFFprobe)
	if err != nil {
		t.Fatal(err)
	}
	if result.Pending {
		t.Fatal("a recent failure must stop the client from polling")
	}
	if got := prober.callCount(); got != 0 {
		t.Fatalf("probe calls = %d, want 0 inside the failure cooldown", got)
	}
}

// 没有注入探测服务时（ffprobe 不可用的精简部署）不能谎报 pending。
func TestSegmentsForPlaybackWithoutProbeNeverReportsPending(t *testing.T) {
	f := newSegmentSourceFixture(t, nil)

	result, err := f.svc.SegmentsForPlayback(t.Context(), f.media, SegmentSourceFFprobe)
	if err != nil {
		t.Fatal(err)
	}
	if result.Pending {
		t.Fatal("pending must be false when no prober is wired")
	}
	if len(result.Segments) != 0 {
		t.Fatalf("segments = %#v, want none", result.Segments)
	}
}

// ListForPlayback 仍供 Emby / Jellyfin 兼容接口使用，按 auto 档取数。
func TestListForPlaybackUsesAutoSelection(t *testing.T) {
	f := newSegmentSourceFixture(t, &stubProber{result: chapterProbeResult()})
	f.seedChapters(t, chapterIntroRow())

	rows, err := f.svc.ListForPlayback(t.Context(), f.media)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Source != SegmentSourceFFprobe {
		t.Fatalf("rows = %#v, want the chapter row via auto", rows)
	}
}
