package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// 客户端刷新首页时会用 IncludeItemTypes=Movie,Series,Video 一次取「最新」行。
// 电影库必须返回电影，而不是因为请求里带了 Series 就整行判空。
func TestEmbyItemsMixedTypesKeepsMoviesInMovieLibrary(t *testing.T) {
	router, svc, _ := newEmbyDiscoveryEnv(t)
	if err := svc.Repo.DB.AutoMigrate(&model.Series{}, &model.PlayProfile{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	libID := seedEmbyLibrary(t, svc, "movie")
	if err := svc.Repo.DB.WithContext(context.Background()).Create(&model.Media{
		Base:        model.Base{ID: "movie-1"},
		LibraryID:   libID,
		Title:       "Movie One",
		Path:        "/media/movie/one.mp4",
		Year:        2024,
		ReleaseDate: "2024-01-01",
	}).Error; err != nil {
		t.Fatalf("create movie: %v", err)
	}

	path := "/emby/Items?ParentId=" + libID +
		"&IncludeItemTypes=Movie,Series,Video&Recursive=true" +
		"&SortBy=DateCreated,SortName&SortOrder=Descending&Limit=12&StartIndex=0"
	w := embyGet(t, router, path, signedTestToken(t, "test-secret"))
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}

	items := decodeItemsEnvelope(t, w.Body.Bytes())
	if len(items) != 1 {
		t.Fatalf("expected the movie to be returned, got %d items: %s", len(items), w.Body.String())
	}
	if items[0]["Id"] != "movie-1" || items[0]["Type"] != "Movie" {
		t.Fatalf("unexpected item payload: %#v", items[0])
	}
}

// 同一组混合类型在剧集库上仍要折叠成剧集卡片，修复不能把这条路径弄回单集。
func TestEmbyItemsMixedTypesKeepsSeriesCardsInTvLibrary(t *testing.T) {
	router, svc, _ := newEmbyDiscoveryEnv(t)
	if err := svc.Repo.DB.AutoMigrate(&model.Series{}, &model.PlayProfile{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	libID := seedEmbyLibrary(t, svc, "tv")
	for _, ep := range []struct {
		id   string
		num  int
		path string
	}{
		{id: "ep-1", num: 1, path: "/media/tv/show/S01E01.mkv"},
		{id: "ep-2", num: 2, path: "/media/tv/show/S01E02.mkv"},
	} {
		if err := svc.Repo.DB.WithContext(context.Background()).Create(&model.Media{
			Base:       model.Base{ID: ep.id},
			LibraryID:  libID,
			SeriesID:   "series-1",
			Title:      "Show",
			Path:       ep.path,
			SeasonNum:  1,
			EpisodeNum: ep.num,
			Year:       2024,
		}).Error; err != nil {
			t.Fatalf("create episode: %v", err)
		}
	}

	path := "/emby/Items?ParentId=" + libID +
		"&IncludeItemTypes=Movie,Series,Video&Recursive=true" +
		"&SortBy=DateCreated,SortName&SortOrder=Descending&Limit=12&StartIndex=0"
	w := embyGet(t, router, path, signedTestToken(t, "test-secret"))
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}

	items := decodeItemsEnvelope(t, w.Body.Bytes())
	if len(items) != 1 {
		t.Fatalf("expected one series card, got %d items: %s", len(items), w.Body.String())
	}
	if items[0]["Type"] != "Series" {
		t.Fatalf("expected Series card, got %#v", items[0])
	}
}

// Emby 的 SpecialFeatures 响应是 BaseItemDto[]，客户端按 List 强转；
// 返回 {"Items":[]} 会直接抛 type cast 异常。
func TestEmbySpecialFeaturesReturnsBareArray(t *testing.T) {
	router, _, _ := newEmbyDiscoveryEnv(t)
	w := embyGet(t, router, "/emby/Items/movie-1/SpecialFeatures", signedTestToken(t, "test-secret"))
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	if trimmed := bytes.TrimSpace(w.Body.Bytes()); len(trimmed) == 0 || trimmed[0] != '[' {
		t.Fatalf("SpecialFeatures must be a JSON array, got %s", w.Body.String())
	}
	var items []any
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode array: %v body=%s", err, w.Body.String())
	}
	if len(items) != 0 {
		t.Fatalf("expected empty array, got %d entries", len(items))
	}
}
