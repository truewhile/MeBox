package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/database"
	"github.com/truewhile/MeBox/internal/model"
)

func newSegmentTestRepos(t *testing.T) *Container {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db)
}

func TestReplaceForMediaIsIdempotentAcrossRefreshes(t *testing.T) {
	repos := newSegmentTestRepos(t)
	ctx := t.Context()

	rows := []model.MediaSegment{
		{MediaID: "m-1", Kind: model.SegmentKindIntro, StartMs: 228_664, EndMs: 246_143, Source: "theintrodb"},
		{MediaID: "m-1", Kind: model.SegmentKindCredits, StartMs: 3_431_000, EndMs: 0, Source: "theintrodb"},
	}
	// 重复刷新不能因为 (media_id, kind, start_ms, source) 唯一索引而失败，也不能累积重复行。
	for i := 0; i < 2; i++ {
		if err := repos.MediaSegment.ReplaceForMedia(ctx, "m-1", "theintrodb", rows); err != nil {
			t.Fatalf("replace #%d: %v", i+1, err)
		}
	}
	got, err := repos.MediaSegment.ListByMedia(ctx, "m-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 after two refreshes of the same source", len(got))
	}

	// 替换只影响同一来源：另一个来源的数据必须保留。
	other := []model.MediaSegment{
		{MediaID: "m-1", Kind: model.SegmentKindIntro, StartMs: 10, EndMs: 20, Source: "manual"},
	}
	if err := repos.MediaSegment.ReplaceForMedia(ctx, "m-1", "manual", other); err != nil {
		t.Fatal(err)
	}
	got, err = repos.MediaSegment.ListByMedia(ctx, "m-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3 (2 theintrodb + 1 manual)", len(got))
	}
	if got[0].Source != "manual" || got[0].StartMs != 10 {
		t.Fatalf("rows should be ordered by start_ms, got first = %#v", got[0])
	}
}

func TestReplaceForMediaAllowsSameRangeFromDifferentSources(t *testing.T) {
	repos := newSegmentTestRepos(t)
	ctx := t.Context()
	span := model.MediaSegment{MediaID: "m-1", Kind: model.SegmentKindIntro, StartMs: 1_000, EndMs: 2_000}

	provider := span
	provider.Source = "theintrodb"
	if err := repos.MediaSegment.ReplaceForMedia(ctx, "m-1", "theintrodb", []model.MediaSegment{provider}); err != nil {
		t.Fatal(err)
	}
	// 人工修正给出完全相同的区间：唯一索引含 source，两个来源必须能共存。
	manual := span
	manual.Source = "manual"
	if err := repos.MediaSegment.ReplaceForMedia(ctx, "m-1", "manual", []model.MediaSegment{manual}); err != nil {
		t.Fatalf("same range from another source: %v", err)
	}
	got, err := repos.MediaSegment.ListByMedia(ctx, "m-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (one per source)", len(got))
	}
}

func TestReplaceForMediaClearsRowsWhenLookupReturnsNothing(t *testing.T) {
	repos := newSegmentTestRepos(t)
	ctx := t.Context()

	rows := []model.MediaSegment{
		{MediaID: "m-1", Kind: model.SegmentKindIntro, StartMs: 1_000, EndMs: 2_000, Source: "theintrodb"},
	}
	if err := repos.MediaSegment.ReplaceForMedia(ctx, "m-1", "theintrodb", rows); err != nil {
		t.Fatal(err)
	}
	// 提供方后来把这段数据删掉了，本地必须跟着清空，否则会一直跳一个不存在的片头。
	if err := repos.MediaSegment.ReplaceForMedia(ctx, "m-1", "theintrodb", nil); err != nil {
		t.Fatal(err)
	}
	got, err := repos.MediaSegment.ListByMedia(ctx, "m-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("rows = %d, want 0 after an empty refresh", len(got))
	}
}

func TestUpsertFetchKeepsOneRowPerMediaAndSource(t *testing.T) {
	repos := newSegmentTestRepos(t)
	ctx := t.Context()
	now := time.Now()

	if err := repos.MediaSegment.UpsertFetch(ctx, &model.MediaSegmentFetch{
		MediaID: "m-1", Source: "theintrodb", FetchedAt: now, Found: false,
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	got, err := repos.MediaSegment.GetFetch(ctx, "m-1", "theintrodb")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Found {
		t.Fatalf("first lookup should be recorded as a miss, got %#v", got)
	}

	later := now.Add(time.Hour)
	if err := repos.MediaSegment.UpsertFetch(ctx, &model.MediaSegmentFetch{
		MediaID: "m-1", Source: "theintrodb", FetchedAt: later, Found: true,
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	var count int64
	if err := repos.DB.Model(&model.MediaSegmentFetch{}).
		Where("media_id = ? AND source = ?", "m-1", "theintrodb").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("fetch ledger rows = %d, want 1", count)
	}
	got, err = repos.MediaSegment.GetFetch(ctx, "m-1", "theintrodb")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.Found {
		t.Fatalf("ledger should be updated in place, got %#v", got)
	}
}

func TestListSeasonSiblingsSharesLibraryTitleTMDb(t *testing.T) {
	repos := newSegmentTestRepos(t)
	ctx := t.Context()
	eps := []*model.Media{
		{Base: model.Base{ID: "a"}, LibraryID: "lib", Title: "Show", Path: "/a", SeasonNum: 1, EpisodeNum: 1, TMDbID: 99},
		{Base: model.Base{ID: "b"}, LibraryID: "lib", Title: "Show", Path: "/b", SeasonNum: 1, EpisodeNum: 2, TMDbID: 99},
		{Base: model.Base{ID: "c"}, LibraryID: "lib", Title: "Show", Path: "/c", SeasonNum: 2, EpisodeNum: 1, TMDbID: 99},
	}
	for _, ep := range eps {
		if err := repos.DB.Create(ep).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := repos.Media.ListSeasonSiblings(ctx, eps[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("siblings = %#v, want only same-season ep b", got)
	}
}

func TestListPrewarmCandidatesOrdersRecentPlaysFirst(t *testing.T) {
	repos := newSegmentTestRepos(t)
	ctx := t.Context()
	now := time.Now()
	for _, m := range []*model.Media{
		{Base: model.Base{ID: "cold"}, Path: "/cold", TMDbID: 1},
		{Base: model.Base{ID: "hot"}, Path: "/hot", TMDbID: 2},
	} {
		if err := repos.DB.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := repos.DB.Create(&model.PlaybackHistory{
		Base: model.Base{ID: "ph1"}, UserID: "u", MediaID: "hot",
	}).Error; err != nil {
		t.Fatal(err)
	}
	got, err := repos.MediaSegment.ListPrewarmCandidates(
		ctx, "theintrodb", now.Add(-time.Hour), now.Add(-time.Hour), 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("candidates = %#v, want both", got)
	}
	if got[0].ID != "hot" {
		t.Fatalf("first = %s, want hot (recently played)", got[0].ID)
	}
}
