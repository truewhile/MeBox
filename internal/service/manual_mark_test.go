package service

import (
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func newManualMarkFixture(t *testing.T) (*MediaSegmentService, *repository.Container, *model.Media) {
	t.Helper()
	repos := repository.New(newServiceTestDB(t))
	svc := NewMediaSegmentService(zap.NewNop(), repos)
	m := &model.Media{
		Base:       model.Base{ID: "ep-1"},
		Title:      "Show",
		Path:       "/tv/show/s01e01.mkv",
		LibraryID:  "lib-1",
		SeasonNum:  1,
		EpisodeNum: 1,
	}
	if err := repos.DB.Create(m).Error; err != nil {
		t.Fatal(err)
	}
	return svc, repos, m
}

func TestSetManualMarkWritesIntroBoundary(t *testing.T) {
	svc, repos, m := newManualMarkFixture(t)
	if err := svc.SetManualMark(t.Context(), m, ManualMarkRequest{
		Kind: model.SegmentKindIntro, PositionMs: 90_000,
	}); err != nil {
		t.Fatalf("set mark: %v", err)
	}
	rows, err := repos.MediaSegment.ListByMedia(t.Context(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want 1", rows)
	}
	if rows[0].Kind != model.SegmentKindIntro || rows[0].Source != SegmentSourceManual {
		t.Fatalf("row = %#v, want manual intro", rows[0])
	}
	if rows[0].StartMs != 0 || rows[0].EndMs != 90_000 {
		t.Fatalf("row = %#v, want 0-90000ms", rows[0])
	}
}

func TestSetManualMarkClearRemovesOnlyThatKind(t *testing.T) {
	svc, repos, m := newManualMarkFixture(t)
	ctx := t.Context()
	if err := svc.SetManualMark(ctx, m, ManualMarkRequest{Kind: model.SegmentKindIntro, PositionMs: 90_000}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetManualMark(ctx, m, ManualMarkRequest{Kind: model.SegmentKindCredits, PositionMs: 1_200_000}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetManualMark(ctx, m, ManualMarkRequest{Kind: model.SegmentKindIntro, Clear: true}); err != nil {
		t.Fatal(err)
	}
	rows, err := repos.MediaSegment.ListByMedia(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Kind != model.SegmentKindCredits {
		t.Fatalf("rows = %#v, want only the credits mark to survive", rows)
	}
}

func TestSetManualMarkRejectsInvalidRequests(t *testing.T) {
	svc, _, m := newManualMarkFixture(t)
	cases := []ManualMarkRequest{
		{Kind: "recap", PositionMs: 1000},   // 只支持 intro/credits
		{Kind: "preview", PositionMs: 1000}, // 同上
		{Kind: model.SegmentKindIntro, PositionMs: 0},
		{Kind: model.SegmentKindIntro, PositionMs: -5},
	}
	for _, req := range cases {
		if err := svc.SetManualMark(t.Context(), m, req); err == nil {
			t.Fatalf("req %#v: want an error", req)
		}
	}
}

// 同一季的其它集应自动继承手动标记，但用户已显式标过的集不能被覆盖。
func TestSetManualMarkPropagatesToSeasonSiblings(t *testing.T) {
	svc, repos, m := newManualMarkFixture(t)
	ctx := t.Context()
	sibling := &model.Media{
		Base:       model.Base{ID: "ep-2"},
		Title:      "Show",
		Path:       "/tv/show/s01e02.mkv",
		LibraryID:  "lib-1",
		SeasonNum:  1,
		EpisodeNum: 2,
	}
	other := &model.Media{
		Base:       model.Base{ID: "ep-3"},
		Title:      "Show",
		Path:       "/tv/show/s01e03.mkv",
		LibraryID:  "lib-1",
		SeasonNum:  1,
		EpisodeNum: 3,
	}
	if err := repos.DB.Create(sibling).Error; err != nil {
		t.Fatal(err)
	}
	if err := repos.DB.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	// other 已经有自己的手动片头标记，不该被覆盖。
	if err := svc.SetManualMark(ctx, other, ManualMarkRequest{
		Kind: model.SegmentKindIntro, PositionMs: 45_000,
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.SetManualMark(ctx, m, ManualMarkRequest{
		Kind: model.SegmentKindIntro, PositionMs: 90_000,
	}); err != nil {
		t.Fatal(err)
	}

	sibRows, err := repos.MediaSegment.ListByMedia(ctx, sibling.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sibRows) != 1 || sibRows[0].EndMs != 90_000 {
		t.Fatalf("sibling rows = %#v, want the propagated 90000ms mark", sibRows)
	}

	otherRows, err := repos.MediaSegment.ListByMedia(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherRows) != 1 || otherRows[0].EndMs != 45_000 {
		t.Fatalf("other rows = %#v, want its own 45000ms mark preserved", otherRows)
	}
}

// 手动标记优先级最高：即使外部源给了不同位置，播放时也只输出手动那条。
func TestManualMarkOutranksExternalSource(t *testing.T) {
	svc, repos, m := newManualMarkFixture(t)
	ctx := t.Context()
	if err := repos.MediaSegment.ReplaceForMedia(ctx, m.ID, IntroDBSource, []model.MediaSegment{{
		MediaID: m.ID, Kind: model.SegmentKindIntro, StartMs: 0, EndMs: 30_000, Source: IntroDBSource,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetManualMark(ctx, m, ManualMarkRequest{
		Kind: model.SegmentKindIntro, PositionMs: 90_000,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.mergeForPlayback(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Source != SegmentSourceManual || rows[0].EndMs != 90_000 {
		t.Fatalf("rows = %#v, want the manual mark to win", rows)
	}
}
