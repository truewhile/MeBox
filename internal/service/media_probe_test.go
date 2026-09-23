package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// stubProber 是 mediaProber 的测试桩：可以拦住探测（gate）用来验证并发去重，
// 也可以直接返回预置结果或错误。
type stubProber struct {
	gate   chan struct{}
	result *FullProbeResult
	err    error

	mu    sync.Mutex
	calls int
	last  ProbeInput
}

func (s *stubProber) ProbeFull(_ context.Context, input ProbeInput) (*FullProbeResult, error) {
	s.mu.Lock()
	s.calls++
	s.last = input
	gate := s.gate
	result := s.result
	err := s.err
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return result, err
}

func (s *stubProber) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *stubProber) lastInput() ProbeInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func chapterProbeResult() *FullProbeResult {
	return &FullProbeResult{
		Container:   "matroska,webm",
		DurationSec: 1451,
		BitRate:     8_000_000,
		Streams: []ProbeStream{
			{Index: 0, Type: "video", Codec: "hevc", Width: 3840, Height: 2160},
			{Index: 1, Type: "audio", Codec: "eac3"},
			{Index: 2, Type: "subtitle", Codec: "ass"},
		},
		Chapters: []ProbeChapter{
			{Index: 0, StartMs: 0, EndMs: 95_000, Title: "Chapter 01"},
			{Index: 1, StartMs: 228_664, EndMs: 246_143, Title: "Opening"},
			{Index: 2, StartMs: 3_431_000, EndMs: 0, Title: "End Credits"},
		},
	}
}

// newProbeFixture 建一条本地媒体（真实落在临时目录里，因为 probeInput 会 stat
// 它）和一个可注入桩的 MediaProbeService。
func newProbeFixture(t *testing.T) (*MediaProbeService, *repository.Container, *model.Media) {
	t.Helper()
	repos := repository.New(newServiceTestDB(t))
	dir := t.TempDir()
	path := filepath.Join(dir, "S01E01.mkv")
	if err := os.WriteFile(path, []byte("not-really-a-video"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model.Media{
		Base:       model.Base{ID: "ep-1"},
		LibraryID:  "lib-anime",
		SeriesID:   "s-1",
		Title:      "某剧",
		Path:       path,
		SeasonNum:  1,
		EpisodeNum: 1,
	}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewMediaProbeService(zap.NewNop(), repos, nil)
	return svc, repos, m
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met before the deadline")
}

func TestMediaProbeRunPersistsProbeRow(t *testing.T) {
	svc, repos, m := newProbeFixture(t)
	prober := &stubProber{result: chapterProbeResult()}
	svc.probe = prober

	// 直接调 run（而不是 EnsureAsync）以获得确定性的断言：这里要验证的是落库，
	// 不是调度。
	svc.run(context.Background(), m)

	if got := prober.lastInput().Source; got != m.Path {
		t.Fatalf("probe source = %q, want the local path %q", got, m.Path)
	}

	probe, err := repos.MediaProbe.Get(t.Context(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if probe == nil {
		t.Fatal("probe row missing")
	}
	if probe.Container != "matroska,webm" || probe.DurationSec != 1451 || probe.ChapterCount != 3 {
		t.Fatalf("probe summary = %#v", probe)
	}
	if probe.VideoStreams != 1 || probe.AudioStreams != 1 || probe.SubtitleStreams != 1 {
		t.Fatalf("stream counts = %d/%d/%d", probe.VideoStreams, probe.AudioStreams, probe.SubtitleStreams)
	}
	if probe.Width != 3840 || probe.Height != 2160 || probe.VideoCodec != "hevc" || probe.AudioCodec != "eac3" {
		t.Fatalf("probe primaries = %#v", probe)
	}
	if probe.Source != "local" || probe.Signature == "" {
		t.Fatalf("probe source/signature = %q/%q", probe.Source, probe.Signature)
	}
	if probe.LastError != "" {
		t.Fatalf("last error = %q, want empty", probe.LastError)
	}
	if !strings.Contains(probe.Payload, "hevc") {
		t.Fatalf("payload should carry the parsed streams: %s", probe.Payload)
	}

	// 时长回填：STRM / 云盘媒体扫描时拿不到时长，末段区间要靠它换算结束时间。
	var refreshed model.Media
	if err := repos.DB.First(&refreshed, "id = ?", m.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.DurationSec != 1451 {
		t.Fatalf("media duration = %d, want the probed 1451", refreshed.DurationSec)
	}
}

func TestMediaProbeFailureKeepsPreviousSummary(t *testing.T) {
	svc, repos, m := newProbeFixture(t)
	prober := &stubProber{result: chapterProbeResult()}
	svc.probe = prober
	svc.run(context.Background(), m)

	prober.err = errors.New("ffprobe full: exit status 1")
	svc.run(context.Background(), m)

	probe, err := repos.MediaProbe.Get(t.Context(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if probe == nil || probe.LastError == "" {
		t.Fatal("a failed re-probe must record the error")
	}
	// 关键：失败只更新时间与错误信息，上一次成功的媒体信息必须留着——
	// 否则一次失败的重探会把已经拿到的时长（片尾区间换算要用）抹掉。
	if probe.DurationSec != 1451 || probe.Container != "matroska,webm" || probe.Payload == "" {
		t.Fatalf("a failed re-probe wiped the previous summary: %#v", probe)
	}
}

// 探测有结论后就不再重探；失败要等冷却期过去才允许重试。
func TestMediaProbeSettledSkipsReprobeButStaleFailureRetries(t *testing.T) {
	if !mediaProbeSettled(&model.MediaProbe{ProbedAt: time.Now()}) {
		t.Fatal("a completed probe is settled")
	}
	if !mediaProbeSettled(&model.MediaProbe{ProbedAt: time.Now(), LastError: "boom"}) {
		t.Fatal("a recent failure is settled: it must not be retried on every play")
	}
	if mediaProbeSettled(&model.MediaProbe{ProbedAt: time.Now().Add(-mediaProbeFailureRetry - time.Minute), LastError: "boom"}) {
		t.Fatal("a failure past the cooldown should be retried")
	}
	if mediaProbeSettled(nil) {
		t.Fatal("a missing probe row is not settled")
	}
}

func TestMediaProbeEnsureAsyncDedupesInFlightProbes(t *testing.T) {
	svc, _, m := newProbeFixture(t)
	gate := make(chan struct{})
	prober := &stubProber{result: chapterProbeResult(), gate: gate}
	svc.probe = prober

	if !svc.EnsureAsync(m) {
		t.Fatal("first EnsureAsync should enqueue a probe")
	}
	// 同一条媒体在跑的时候不能重复排队：否则每次播放请求都会再起一次 3 秒探测。
	if svc.EnsureAsync(m) {
		t.Fatal("second EnsureAsync must report that a probe is already running")
	}

	close(gate)
	waitForCondition(t, 5*time.Second, func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return len(svc.inFlight) == 0
	})
	if got := prober.callCount(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
	// 跑完之后允许再次排队（例如失败冷却期到了之后的重试）。
	if !svc.EnsureAsync(m) {
		t.Fatal("EnsureAsync should enqueue again once the previous run finished")
	}
	waitForCondition(t, 5*time.Second, func() bool { return prober.callCount() == 2 })
}

func TestMediaProbeEnsureAsyncWithoutProberIsNotPending(t *testing.T) {
	svc, _, m := newProbeFixture(t)
	// probe 未注入（ffprobe 不可用时就是这样）：不能谎报「提取中」，否则客户端
	// 会白轮询一轮。
	if svc.EnsureAsync(m) {
		t.Fatal("EnsureAsync must return false without a prober")
	}
}

func TestMediaProbeInputRejectsMissingLocalFile(t *testing.T) {
	svc, _, _ := newProbeFixture(t)
	_, err := svc.probeInput(t.Context(), &model.Media{
		Base: model.Base{ID: "missing"}, Path: filepath.Join(t.TempDir(), "nope.mkv"),
	})
	if !errors.Is(err, ErrMediaNotFound) {
		t.Fatalf("err = %v, want ErrMediaNotFound", err)
	}
}

func TestMediaProbeInputResolvesStrmWithHeaders(t *testing.T) {
	svc := &MediaProbeService{}
	svc.SetPlayTargetResolver(func(_ context.Context, raw, userAgent string) (*StrmPlayResult, error) {
		if raw != "https://pan.example.com/api/strm/play/115?v=1" {
			t.Fatalf("resolver got raw = %q", raw)
		}
		if userAgent != "" {
			t.Fatalf("userAgent = %q, want empty for a background probe", userAgent)
		}
		return &StrmPlayResult{RedirectURL: "https://cdn.example.com/a.mkv"}, nil
	})
	input, err := svc.probeInput(t.Context(), &model.Media{
		Base:    model.Base{ID: "strm-1"},
		Path:    "/media/a.mkv.strm",
		STRMURL: "https://pan.example.com/api/strm/play/115?v=1",
	})
	if err != nil {
		t.Fatalf("probeInput: %v", err)
	}
	if input.Source != "https://cdn.example.com/a.mkv" {
		t.Fatalf("source = %q, want the resolved direct link", input.Source)
	}
}

// 签名不能把播放目标（含 pickcode）再抄一份进数据库，所以整体做哈希。
func TestMediaProbeSignatureHidesPlayTargetAndTracksFileChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(path, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	local := &model.Media{Base: model.Base{ID: "l-1"}, Path: path}
	first := mediaProbeSignature(local)
	if len(first) != 32 {
		t.Fatalf("signature = %q, want a 32-char hex digest", first)
	}
	if strings.Contains(first, "movie.mkv") {
		t.Fatalf("signature leaked the path: %q", first)
	}
	if err := os.WriteFile(path, []byte("v2-changed-size"), 0o600); err != nil {
		t.Fatal(err)
	}
	if mediaProbeSignature(local) == first {
		t.Fatal("the signature must change when the file changes")
	}

	strm := &model.Media{
		Base:    model.Base{ID: "s-1"},
		Path:    "/media/movie.mkv.strm",
		STRMURL: "https://pan.example.com/api/strm/play/115?pickcode=secret-pickcode",
	}
	strmSig := mediaProbeSignature(strm)
	if strings.Contains(strmSig, "secret-pickcode") || strings.Contains(strmSig, "pan.example.com") {
		t.Fatalf("strm signature leaked the play target: %q", strmSig)
	}
	if strmSig == "" {
		t.Fatal("strm signature must not be empty")
	}
}
