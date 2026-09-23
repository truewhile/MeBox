package repository

import (
	"testing"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

func TestMediaProbeUpsertReplacesAndGetReturnsLatest(t *testing.T) {
	repos := newSegmentTestRepos(t)
	ctx := t.Context()

	first := &model.MediaProbe{
		MediaID: "m-1", Signature: "sig-1", Source: "local",
		Container: "matroska,webm", DurationSec: 1451, ChapterCount: 2,
		Payload: `{"container":"matroska,webm"}`, ProbedAt: time.Now(),
	}
	if err := repos.MediaProbe.Upsert(ctx, first); err != nil {
		t.Fatalf("upsert #1: %v", err)
	}
	second := &model.MediaProbe{
		MediaID: "m-1", Signature: "sig-2", Source: "strm",
		Container: "mp4", DurationSec: 900, ChapterCount: 0,
		Payload: `{"container":"mp4"}`, ProbedAt: time.Now(),
	}
	if err := repos.MediaProbe.Upsert(ctx, second); err != nil {
		t.Fatalf("upsert #2: %v", err)
	}

	got, err := repos.MediaProbe.Get(ctx, "m-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("probe row missing")
	}
	if got.Container != "mp4" || got.DurationSec != 900 || got.Signature != "sig-2" || got.Payload != `{"container":"mp4"}` {
		t.Fatalf("probe = %#v, want the second upsert to win", got)
	}
	// 重复探测不能累积重复行：media_id 是唯一索引。
	var count int64
	if err := repos.DB.Model(&model.MediaProbe{}).Where("media_id = ?", "m-1").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows = %d, want 1", count)
	}
}

func TestMediaProbeGetReturnsNilWhenMissing(t *testing.T) {
	repos := newSegmentTestRepos(t)
	got, err := repos.MediaProbe.Get(t.Context(), "nope")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("probe = %#v, want nil", got)
	}
}

func TestMediaProbeMarkFailureKeepsPreviousPayload(t *testing.T) {
	repos := newSegmentTestRepos(t)
	ctx := t.Context()

	if err := repos.MediaProbe.Upsert(ctx, &model.MediaProbe{
		MediaID: "m-1", Container: "matroska,webm", DurationSec: 1451,
		ChapterCount: 2, Payload: `{"container":"matroska,webm"}`, ProbedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.MediaProbe.MarkFailure(ctx, "m-1", "ffprobe full: exit status 1", time.Now()); err != nil {
		t.Fatal(err)
	}

	got, err := repos.MediaProbe.Get(ctx, "m-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.LastError != "ffprobe full: exit status 1" {
		t.Fatalf("probe = %#v, want the recorded failure", got)
	}
	// 一次失败的重探不该把上一次成功拿到的媒体信息抹掉。
	if got.Container != "matroska,webm" || got.DurationSec != 1451 || got.Payload == "" || got.ChapterCount != 2 {
		t.Fatalf("a failed re-probe wiped the previous summary: %#v", got)
	}
}

func TestMediaProbeMarkFailureInsertsRowWhenAbsent(t *testing.T) {
	repos := newSegmentTestRepos(t)
	ctx := t.Context()

	// 没有历史成功记录时，失败也必须落一行：否则冷却期没有时间戳，
	// 每次播放都会重跑一次注定失败的探测。
	if err := repos.MediaProbe.MarkFailure(ctx, "m-2", "boom", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := repos.MediaProbe.Get(ctx, "m-2")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.LastError != "boom" || got.ProbedAt.IsZero() {
		t.Fatalf("probe = %#v, want a failure row with a timestamp", got)
	}
}
