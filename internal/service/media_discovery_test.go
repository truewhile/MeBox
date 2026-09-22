package service

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// newDiscoveryTestDB 建好内存库并返回 repository 容器。
func newDiscoveryTestDB(t *testing.T) *repository.Container {
	t.Helper()
	return repository.New(newServiceTestDB(t))
}

func seedDiscoveryLibrary(t *testing.T, repos *repository.Container, typ string) string {
	t.Helper()
	lib := &model.Library{Name: "库-" + typ, Path: "/media/" + typ, Type: typ, Enabled: true}
	if err := repos.Library.Create(context.Background(), lib); err != nil {
		t.Fatal(err)
	}
	return lib.ID
}

func seedDiscoveryMedia(t *testing.T, repos *repository.Container, m *model.Media) {
	t.Helper()
	if err := repos.DB.WithContext(context.Background()).Create(m).Error; err != nil {
		t.Fatal(err)
	}
}

func findGenre(genres []GenreCount, name string) int {
	for _, g := range genres {
		if g.Name == name {
			return g.Count
		}
	}
	return -1
}

func TestAggregateGenresSplitsAndCounts(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "movie")

	seedDiscoveryMedia(t, repos, &model.Media{
		LibraryID: libID, Title: "A", Path: "/media/movie/a.mkv", Genres: "Action,Drama",
	})
	seedDiscoveryMedia(t, repos, &model.Media{
		LibraryID: libID, Title: "B", Path: "/media/movie/b.mkv", Genres: "action",
	})

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	genres, err := svc.AggregateGenres(context.Background(), MediaVisibility{IncludeNSFW: true}, "")
	if err != nil {
		t.Fatal(err)
	}

	// 大小写不同视为同一类型，展示名保留首次出现的写法。
	if got := findGenre(genres, "Action"); got != 2 {
		t.Fatalf("Action count = %d, want 2 (genres=%+v)", got, genres)
	}
	if got := findGenre(genres, "Drama"); got != 1 {
		t.Fatalf("Drama count = %d, want 1 (genres=%+v)", got, genres)
	}
}

// 中文全角逗号在刮削结果里同样常见，必须一并切分。
func TestAggregateGenresHandlesFullWidthComma(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	libID := seedDiscoveryLibrary(t, repos, "movie")
	seedDiscoveryMedia(t, repos, &model.Media{
		LibraryID: libID, Title: "A", Path: "/media/movie/a.mkv", Genres: "科幻，悬疑",
	})

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	genres, err := svc.AggregateGenres(context.Background(), MediaVisibility{IncludeNSFW: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if findGenre(genres, "科幻") != 1 || findGenre(genres, "悬疑") != 1 {
		t.Fatalf("genres = %+v, want 科幻:1 and 悬疑:1", genres)
	}
}

// libraryID 非空时只统计该库，供媒体库页面的筛选项使用。
func TestAggregateGenresScopedToLibrary(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	movieLib := seedDiscoveryLibrary(t, repos, "movie")
	tvLib := seedDiscoveryLibrary(t, repos, "tv")

	seedDiscoveryMedia(t, repos, &model.Media{
		LibraryID: movieLib, Title: "A", Path: "/media/movie/a.mkv", Genres: "Action",
	})
	seedDiscoveryMedia(t, repos, &model.Media{
		LibraryID: tvLib, Title: "B", Path: "/media/tv/b.mkv", Genres: "Comedy",
	})

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	genres, err := svc.AggregateGenres(context.Background(), MediaVisibility{IncludeNSFW: true}, movieLib)
	if err != nil {
		t.Fatal(err)
	}
	if findGenre(genres, "Comedy") != -1 {
		t.Fatalf("Comedy must not appear when scoped to the movie library: %+v", genres)
	}
	if findGenre(genres, "Action") != 1 {
		t.Fatalf("Action count = %d, want 1", findGenre(genres, "Action"))
	}
}

// 可见性过滤必须生效：不可见的库不应泄漏类型统计。
func TestAggregateGenresRespectsVisibility(t *testing.T) {
	repos := newDiscoveryTestDB(t)
	allowedLib := seedDiscoveryLibrary(t, repos, "movie")
	otherLib := seedDiscoveryLibrary(t, repos, "movie")

	seedDiscoveryMedia(t, repos, &model.Media{
		LibraryID: allowedLib, Title: "A", Path: "/media/a/a.mkv", Genres: "Action",
	})
	seedDiscoveryMedia(t, repos, &model.Media{
		LibraryID: otherLib, Title: "B", Path: "/media/b/b.mkv", Genres: "Secret",
	})

	svc := NewMediaDiscoveryService(zap.NewNop(), repos)
	genres, err := svc.AggregateGenres(context.Background(),
		MediaVisibility{IncludeNSFW: true, AllowedLibraryIDs: []string{allowedLib}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if findGenre(genres, "Secret") != -1 {
		t.Fatalf("hidden library leaked into genres: %+v", genres)
	}
}
