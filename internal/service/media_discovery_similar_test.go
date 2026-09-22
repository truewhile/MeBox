package service

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func seedSimilarMedia(t *testing.T, repos *repository.Container, libID, title, genres string, year int, rating float32) *model.Media {
	t.Helper()
	m := &model.Media{
		LibraryID: libID,
		Title:     title,
		Genres:    genres,
		Year:      year,
		Rating:    rating,
		Path:      "/media/" + libID + "/" + title + ".mkv",
	}
	if err := repos.DB.WithContext(context.Background()).Create(m).Error; err != nil {
		t.Fatal(err)
	}
	return m
}

func similarTitles(t *testing.T, svc *MediaDiscoveryService, sourceID string, visibility MediaVisibility) []string {
	t.Helper()
	rows, err := svc.SimilarCandidates(context.Background(), sourceID, 12, visibility)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Title)
	}
	return out
}

func containsTitle(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// 相似推荐必须排除自己，也要排除同剧的其他集（否则详情页会推荐本剧的其它集）。
func TestSimilarExcludesSelfAndSameSeries(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "tv")

	source := seedSimilarMedia(t, repos, libID, "剧一 E01", "Action", 2020, 8)
	source.SeriesID = "series-1"
	source.SeasonNum, source.EpisodeNum = 1, 1
	if err := repos.DB.WithContext(context.Background()).Save(source).Error; err != nil {
		t.Fatal(err)
	}

	sibling := seedSimilarMedia(t, repos, libID, "剧一 E02", "Action", 2020, 8)
	sibling.SeriesID = "series-1"
	sibling.SeasonNum, sibling.EpisodeNum = 1, 2
	if err := repos.DB.WithContext(context.Background()).Save(sibling).Error; err != nil {
		t.Fatal(err)
	}

	// 同库另一部剧的第 1 集：这才是剧集详情页该推荐的内容。
	other := seedSimilarMedia(t, repos, libID, "另一部动作剧", "Action", 2021, 7)
	other.SeriesID = "series-2"
	other.SeasonNum, other.EpisodeNum = 1, 1
	if err := repos.DB.WithContext(context.Background()).Save(other).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	got := similarTitles(t, svc, source.ID, MediaVisibility{IncludeNSFW: true})

	if containsTitle(got, "剧一 E01") {
		t.Fatalf("result must exclude the source itself: %v", got)
	}
	if containsTitle(got, "剧一 E02") {
		t.Fatalf("result must exclude other episodes of the same series: %v", got)
	}
	if !containsTitle(got, "另一部动作剧") {
		t.Fatalf("result = %v, want it to contain 另一部动作剧", got)
	}
}

// 类型重合度高的条目要排在前面。
func TestSimilarPrefersGenreOverlap(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "movie")

	source := seedSimilarMedia(t, repos, libID, "源片", "Action,Adventure", 2010, 7)
	seedSimilarMedia(t, repos, libID, "同类型", "Action,Adventure", 2010, 7)
	seedSimilarMedia(t, repos, libID, "弱相关", "Comedy", 2010, 7)

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	got := similarTitles(t, svc, source.ID, MediaVisibility{IncludeNSFW: true})

	if len(got) < 2 {
		t.Fatalf("result = %v, want at least 2 entries", got)
	}
	if got[0] != "同类型" {
		t.Fatalf("result = %v, want 同类型 ranked first", got)
	}
}

// 不可见媒体库的条目不能被推荐。
func TestSimilarRespectsVisibility(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	sourceLib := seedDiscoveryLibrary(t, repos, "movie")
	hiddenLib := seedDiscoveryLibrary(t, repos, "movie")

	source := seedSimilarMedia(t, repos, sourceLib, "源片", "Action", 2010, 7)
	seedSimilarMedia(t, repos, hiddenLib, "隐藏片", "Action", 2010, 7)

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	got := similarTitles(t, svc, source.ID, MediaVisibility{IncludeNSFW: true, AllowedLibraryIDs: []string{sourceLib}})

	if containsTitle(got, "隐藏片") {
		t.Fatalf("hidden library leaked into similar: %v", got)
	}
}

// 源条目不可见或不存在时返回空，不报错：客户端不该因此看到 500。
func TestSimilarUnknownSourceReturnsEmpty(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	svc := NewMediaDiscoveryService(zap.NewNop(), repos)

	rows, err := svc.SimilarCandidates(context.Background(), "missing", 12, MediaVisibility{IncludeNSFW: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %v, want empty", rows)
	}
}

// limit 生效，且不返回重复条目。
func TestSimilarHonoursLimit(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "movie")
	source := seedSimilarMedia(t, repos, libID, "源片", "Action", 2010, 7)
	for i := 0; i < 5; i++ {
		seedSimilarMedia(t, repos, libID, "候选"+string(rune('A'+i)), "Action", 2010, 7)
	}

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	rows, err := svc.SimilarCandidates(context.Background(), source.ID, 3, MediaVisibility{IncludeNSFW: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.ID] {
			t.Fatalf("duplicate row %q in result", row.Title)
		}
		seen[row.ID] = true
	}
}
