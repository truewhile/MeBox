package service

import (
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// 「最近添加」必须只做投影扫描，但仍然要返回完整媒体行、正确的集数和
// 可用于剧集导航的 key —— 这些语义是 /libraries/{id}/series/episodes 的
// 前置条件，任何一项退化成「近似值」都会让点开卡片后选集为空。
func TestListRecentSeriesCardsHydratesRowsAndKeepsExactCounts(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.Media{})
	repos := repository.New(db)
	lib := model.Library{Name: "动漫", Path: "/media/anime", Type: "anime", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	rows := make([]model.Media, 0, 64)
	// 长剧：48 集，最新一集入库时间最晚，必须排在首位。
	for i := 1; i <= 48; i++ {
		rows = append(rows, model.Media{
			Base:        model.Base{ID: fmt.Sprintf("long-%02d", i), CreatedAt: now.Add(time.Duration(i) * time.Minute)},
			LibraryID:   lib.ID,
			Title:       "长剧",
			Path:        fmt.Sprintf("/media/anime/长剧/Season 1/长剧.S01E%02d.mkv", i),
			Overview:    "简介内容",
			Genres:      "Animation",
			PosterURL:   "/media/anime/长剧/poster.jpg",
			SeasonNum:   1,
			EpisodeNum:  i,
			ReleaseDate: "2026-01-01",
		})
	}
	// 另一部短剧，入库更早。
	for i := 1; i <= 2; i++ {
		rows = append(rows, model.Media{
			Base:        model.Base{ID: fmt.Sprintf("short-%02d", i), CreatedAt: now.Add(-time.Hour)},
			LibraryID:   lib.ID,
			Title:       "短剧",
			Path:        fmt.Sprintf("/media/anime/短剧/Season 1/短剧.S01E%02d.mkv", i),
			SeasonNum:   1,
			EpisodeNum:  i,
			ReleaseDate: "2025-05-05",
		})
	}
	if err := db.CreateInBatches(&rows, 50).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos)
	cards, err := svc.ListRecentSeriesCards(t.Context(), 2, MediaVisibility{IncludeNSFW: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 {
		t.Fatalf("cards = %d, want 2", len(cards))
	}
	if cards[0].Count != 48 {
		t.Fatalf("长剧 count = %d, want 48 (不能只统计扫描窗口内的行)", cards[0].Count)
	}
	// 投影行没有 overview/genres，返回前必须补齐完整体。
	if cards[0].Rep.Overview == "" || cards[0].Rep.Genres == "" {
		t.Fatalf("representative row was not hydrated: %#v", cards[0].Rep)
	}
	if cards[0].Rep.LibraryName != lib.Name {
		t.Fatalf("library metadata missing on hydrated row: %q", cards[0].Rep.LibraryName)
	}

	// 卡片 key 必须能解析出同一部剧的全部剧集（详情页选集依赖它）。
	episodes, err := svc.ListLibrarySeriesEpisodes(t.Context(), lib.ID, cards[0].Key, MediaVisibility{IncludeNSFW: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 48 {
		t.Fatalf("episodes for card key = %d, want 48", len(episodes))
	}
}

// 结果缓存命中后必须返回同样的卡片，且不能因为缓存把调用方可见的切片搞乱。
func TestListRecentSeriesCardsCachesResult(t *testing.T) {
	db := newServiceTestDB(t, &model.Library{}, &model.Media{})
	repos := repository.New(db)
	lib := model.Library{Name: "电影", Path: "/media/movies", Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := db.Create(&model.Media{
			Base:        model.Base{ID: fmt.Sprintf("movie-%d", i), CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour)},
			LibraryID:   lib.ID,
			Title:       fmt.Sprintf("电影 %d", i),
			Path:        fmt.Sprintf("/media/movies/电影 %d/movie.mkv", i),
			ReleaseDate: "2024-01-01",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	cache := NewRuntimeCacheService(&config.Config{}, zap.NewNop())
	t.Cleanup(func() { cache.Close() })
	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos).SetRuntimeCache(cache)

	first, err := svc.ListRecentSeriesCards(t.Context(), 2, MediaVisibility{IncludeNSFW: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.ListRecentSeriesCards(t.Context(), 2, MediaVisibility{IncludeNSFW: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("first = %d second = %d, want 2/2", len(first), len(second))
	}
	if first[0].Key != second[0].Key || first[0].Count != second[0].Count {
		t.Fatalf("cached result mismatch: %#v vs %#v", first[0], second[0])
	}
}
