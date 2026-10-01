package service

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

// 播放请求要顺带把媒体信息（主要是 STRM 媒体的时长）补齐，但探测不能影响片段：
// ListForPlayback 只读社区库，探测在返回前才异步排上队。
func TestListForPlaybackTriggersMediaProbe(t *testing.T) {
	probeSvc, repos, m := newProbeFixture(t)
	prober := &stubProber{result: chapterProbeResult()}
	probeSvc.probe = prober

	segments := NewMediaSegmentService(zap.NewNop(), repos).SetProbe(probeSvc)

	rows, err := segments.ListForPlayback(t.Context(), m)
	if err != nil {
		t.Fatalf("ListForPlayback: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want none without a provider", rows)
	}

	// 探测是异步的：等它跑完并落库。
	waitForCondition(t, 5*time.Second, func() bool { return prober.callCount() >= 1 })
	probeRow, err := repos.MediaProbe.Get(t.Context(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if probeRow == nil {
		t.Fatal("the playback path should have probed the media info")
	}

	// 已经有结论的媒体不该被反复探测：每次播放重跑一次 2～4.5 秒的远端读取太贵。
	if _, err := segments.ListForPlayback(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := prober.callCount(); got != 1 {
		t.Fatalf("probe calls = %d, want 1 (a settled probe must not repeat)", got)
	}
}

// 探测失败也要有结论：冷却期内不再重探。
func TestListForPlaybackDoesNotReprobeWithinFailureCooldown(t *testing.T) {
	probeSvc, repos, m := newProbeFixture(t)
	prober := &stubProber{result: chapterProbeResult()}
	probeSvc.probe = prober
	if err := repos.MediaProbe.MarkFailure(t.Context(), m.ID, "boom", time.Now()); err != nil {
		t.Fatal(err)
	}

	segments := NewMediaSegmentService(zap.NewNop(), repos).SetProbe(probeSvc)
	if _, err := segments.ListForPlayback(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := prober.callCount(); got != 0 {
		t.Fatalf("probe calls = %d, want 0 inside the cooldown", got)
	}
}

// 没注入探测服务时（ffprobe 不可用的精简部署）播放链路必须照常工作。
func TestListForPlaybackWithoutProbeStillWorks(t *testing.T) {
	_, repos, m := newProbeFixture(t)
	segments := NewMediaSegmentService(zap.NewNop(), repos)
	rows, err := segments.ListForPlayback(t.Context(), m)
	if err != nil {
		t.Fatalf("ListForPlayback: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want none", rows)
	}
}
