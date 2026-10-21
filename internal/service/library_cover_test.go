package service

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
)

func solidJPEG(t *testing.T, c color.Color, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func decodeJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode composed cover: %v", err)
	}
	return img
}

// TestComposeLibraryCoverSplitsIntoSideBySideColumns 保证多张海报是竖切成
// 左右分栏（左→右顺序），而不是 2×2 田字格。
func TestComposeLibraryCoverSplitsIntoSideBySideColumns(t *testing.T) {
	images := [][]byte{
		solidJPEG(t, color.RGBA{R: 220, A: 255}, 200, 300),
		solidJPEG(t, color.RGBA{G: 220, A: 255}, 200, 300),
		solidJPEG(t, color.RGBA{B: 220, A: 255}, 200, 300),
		solidJPEG(t, color.RGBA{R: 220, G: 220, A: 255}, 200, 300),
	}
	out, err := composeLibraryCover(images, 480, 270)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	img := decodeJPEG(t, out)
	if img.Bounds().Dx() != 480 || img.Bounds().Dy() != 270 {
		t.Fatalf("size = %v, want 480x270", img.Bounds())
	}
	// 四张图 → 四条竖栏；同一栏从顶部到底部颜色一致（竖切，不是上下分割）。
	y := img.Bounds().Dy() / 2
	checks := []struct {
		name     string
		x        int
		dominant func(color.RGBA) bool
	}{
		{"column 1 red", 60, func(c color.RGBA) bool { return c.R > c.G+40 && c.R > c.B+40 }},
		{"column 2 green", 180, func(c color.RGBA) bool { return c.G > c.R+40 && c.G > c.B+40 }},
		{"column 3 blue", 300, func(c color.RGBA) bool { return c.B > c.R+40 && c.B > c.G+40 }},
		{"column 4 yellow", 420, func(c color.RGBA) bool { return c.R > c.B+40 && c.G > c.B+40 }},
	}
	for _, check := range checks {
		for _, yy := range []int{2, y, img.Bounds().Dy() - 3} {
			r, g, b, _ := img.At(check.x, yy).RGBA()
			got := color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}
			if !check.dominant(got) {
				t.Fatalf("%s at y=%d: pixel = %v", check.name, yy, got)
			}
		}
	}
}

// TestComposeLibraryCoverHandlesFewerPosters 覆盖 1/2/3 张成员的布局。
func TestComposeLibraryCoverHandlesFewerPosters(t *testing.T) {
	cases := []struct {
		name  string
		count int
	}{
		{"single", 1},
		{"pair", 2},
		{"triple", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			images := make([][]byte, 0, tc.count)
			for i := 0; i < tc.count; i++ {
				images = append(images, solidJPEG(t, color.RGBA{R: 200, G: 120, A: 255}, 300, 450))
			}
			out, err := composeLibraryCover(images, 320, 180)
			if err != nil {
				t.Fatalf("compose: %v", err)
			}
			img := decodeJPEG(t, out)
			if img.Bounds().Dx() != 320 || img.Bounds().Dy() != 180 {
				t.Fatalf("size = %v, want 320x180", img.Bounds())
			}
		})
	}
}

// TestComposeLibraryCoverSkipsUndecodableSources 保证一张坏图不会让整张
// 封面失败：其余可解码的成员仍然会拼出来。
func TestComposeLibraryCoverSkipsUndecodableSources(t *testing.T) {
	images := [][]byte{
		[]byte("not an image"),
		solidJPEG(t, color.RGBA{G: 200, A: 255}, 200, 300),
	}
	out, err := composeLibraryCover(images, 240, 135)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected composed cover bytes")
	}
	if _, err := composeLibraryCover([][]byte{[]byte("nope")}, 240, 135); err == nil {
		t.Fatal("expected error when no source is decodable")
	}
}

func TestLibraryCoverCanvasKeepsSixteenByNine(t *testing.T) {
	cases := []struct {
		inW, inH   int
		wantW      int
		wantH      int
		checkRatio bool
	}{
		{0, 0, 480, 270, true},
		{640, 480, 640, 360, true},   // 4:3 盒子 → 装进 16:9
		{2000, 0, 1920, 1080, true},  // 超上限 → 夹紧
		{40, 0, 96, 54, true},        // 低于下限 → 抬到最小宽度
		{0, 540, 960, 540, false},    // 只给高度 → 按 16:9 推宽度
	}
	for _, tc := range cases {
		w, h := libraryCoverCanvas(tc.inW, tc.inH)
		if w != tc.wantW || h != tc.wantH {
			t.Fatalf("libraryCoverCanvas(%d,%d) = %dx%d, want %dx%d", tc.inW, tc.inH, w, h, tc.wantW, tc.wantH)
		}
		if tc.checkRatio && w != 0 && h != 0 {
			ratio := float64(w) / float64(h)
			if ratio < 1.7 || ratio > 1.85 {
				t.Fatalf("ratio for %dx%d = %.3f, want ~1.78", tc.inW, tc.inH, ratio)
			}
		}
	}
}

// TestLibraryCoverSourcesDedupesSeries 保证封面不会两张都是同一部剧的不同集，
// 并且优先取海报、没有海报时退回剧照、完全没有图的成员被跳过。
func TestLibraryCoverSourcesDedupesSeries(t *testing.T) {
	svc := newTestEmbyService(t)
	lib := model.Library{Name: "剧集", Path: "/media/tv", Type: "tv", Enabled: true}
	if err := svc.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	rows := []model.Media{
		{Base: model.Base{ID: "a1"}, LibraryID: lib.ID, Title: "A", Path: "/media/tv/A/S01E01.mkv", SeriesID: "series-a", PosterURL: "https://img/a1.jpg"},
		{Base: model.Base{ID: "a2"}, LibraryID: lib.ID, Title: "A", Path: "/media/tv/A/S01E02.mkv", SeriesID: "series-a", PosterURL: "https://img/a2.jpg"},
		{Base: model.Base{ID: "b1"}, LibraryID: lib.ID, Title: "B", Path: "/media/tv/B/S01E01.mkv", SeriesID: "series-b", PosterURL: "https://img/b1.jpg"},
		{Base: model.Base{ID: "c1"}, LibraryID: lib.ID, Title: "C", Path: "/media/tv/C/S01E01.mkv", SeriesID: "series-c", BackdropURL: "https://img/c1-backdrop.jpg"},
		{Base: model.Base{ID: "d1"}, LibraryID: lib.ID, Title: "D", Path: "/media/tv/D/S01E01.mkv", SeriesID: "series-d"},
	}
	for i := range rows {
		if err := svc.repo.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("create media: %v", err)
		}
	}
	// 竖切三栏：一部剧只出一张，无图成员被跳过 → 正好 3 张。
	sources := LibraryCoverSources(t.Context(), svc.repo, lib.ID, libraryCoverSourceLimit)
	if len(sources) != 3 {
		t.Fatalf("sources = %v, want 3 (一部剧一张、跳过无图成员)", sources)
	}
	seen := map[string]bool{}
	for _, src := range sources {
		if seen[src] {
			t.Fatalf("duplicate source in %v", sources)
		}
		seen[src] = true
	}
	// 同一部剧只会出现一次：具体保留哪一集由查询顺序决定，但绝不能两张。
	if seen["https://img/a1.jpg"] && seen["https://img/a2.jpg"] {
		t.Fatalf("same series contributed two posters: %v", sources)
	}
	if !seen["https://img/a1.jpg"] && !seen["https://img/a2.jpg"] {
		t.Fatalf("series A is missing from sources: %v", sources)
	}
	if !seen["https://img/c1-backdrop.jpg"] {
		t.Fatalf("member without poster should fall back to backdrop: %v", sources)
	}
}

// TestLibraryCoverImageCachesComposedBytes 验证生成结果会落盘复用：同一份
// 内容第二次请求不再回源。
func TestLibraryCoverImageCachesComposedBytes(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(solidJPEG(t, color.RGBA{R: 180, G: 140, B: 90, A: 255}, 240, 360))
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	cacheCfg := &config.Config{Cache: config.CacheConfig{CacheDir: filepath.Join(t.TempDir(), "cache")}}
	proxy := NewImageProxy(cacheCfg, zap.NewNop())
	proxy.SetAllowedRemoteHostsProvider(func() []string { return []string{parsed.Host} })

	sources := []string{upstream.URL + "/1.jpg", upstream.URL + "/2.jpg", upstream.URL + "/3.jpg", upstream.URL + "/4.jpg"}
	first, err := proxy.LibraryCoverImage(t.Context(), "lib-1", sources, 480, 270)
	if err != nil {
		t.Fatalf("first cover: %v", err)
	}
	if img := decodeJPEG(t, first); img.Bounds().Dx() != 480 || img.Bounds().Dy() != 270 {
		t.Fatalf("size = %v, want 480x270", img.Bounds())
	}
	hitsAfterFirst := atomic.LoadInt32(&hits)
	if hitsAfterFirst != 4 {
		t.Fatalf("upstream hits = %d, want one per source", hitsAfterFirst)
	}

	second, err := proxy.LibraryCoverImage(t.Context(), "lib-1", sources, 480, 270)
	if err != nil {
		t.Fatalf("second cover: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("cached cover bytes differ from the first build")
	}
	if got := atomic.LoadInt32(&hits); got != hitsAfterFirst {
		t.Fatalf("cache miss: upstream hits %d -> %d", hitsAfterFirst, got)
	}

	// 内容变了要重新生成（成员海报换了新文件）。已缓存的三张海报不会重下，
	// 只有新增的那张会回源。
	changed := append([]string{}, sources[:3]...)
	changed = append(changed, upstream.URL+"/5.jpg")
	if _, err := proxy.LibraryCoverImage(t.Context(), "lib-1", changed, 480, 270); err != nil {
		t.Fatalf("cover after content change: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != hitsAfterFirst+1 {
		t.Fatalf("content change should rebuild with one new fetch, hits = %d", got)
	}

	// 同一批成员的另一个尺寸档位要能共存：Web/App 与 Emby 请求的尺寸不同，
	// 不能互相把对方的成品当「旧内容」删掉。
	if _, err := proxy.LibraryCoverImage(t.Context(), "lib-1", changed, 400, 225); err != nil {
		t.Fatalf("other size cover: %v", err)
	}
	entries, err := os.ReadDir(LibraryCoverCacheDir(cacheCfg.Cache.CacheDir))
	if err != nil {
		t.Fatalf("read cover cache dir: %v", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry.Name())
		}
	}
	if len(files) != 2 {
		t.Fatalf("expected 480x270 and 400x225 variants to coexist, got %v", files)
	}
}

// TestLibraryCoverSourceBytesReadsLocalSidecarPoster 覆盖刮削把海报存成
// /media/... 本地绝对路径的情况：这类来源没有 http(s) host，不能走 Fetch，
// 必须按库根目录白名单直接读盘，否则整张拼图会因为「没有可解码的海报」而
// 生成失败（生产环境所有本地库都是这种来源）。
func TestLibraryCoverSourceBytesReadsLocalSidecarPoster(t *testing.T) {
	libDir := t.TempDir()
	posterPath := filepath.Join(libDir, "poster.jpg")
	if err := os.WriteFile(posterPath, solidJPEG(t, color.RGBA{R: 180, G: 90, A: 255}, 200, 300), 0o644); err != nil {
		t.Fatalf("write poster: %v", err)
	}
	proxy := NewImageProxy(&config.Config{Cache: config.CacheConfig{CacheDir: filepath.Join(t.TempDir(), "cache")}}, zap.NewNop())

	// 未登记为库根目录时必须拒绝读取。
	if _, err := proxy.coverSourceBytes(t.Context(), posterPath); err == nil {
		t.Fatal("expected local path outside the library roots to be rejected")
	}

	proxy.SetLibraryRootsProvider(func() []string { return []string{libDir} })
	data, err := proxy.coverSourceBytes(t.Context(), posterPath)
	if err != nil {
		t.Fatalf("read local poster: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected poster bytes")
	}
	if _, ok := decodeCoverImage(data); !ok {
		t.Fatal("local poster should decode")
	}
}

// TestLibraryCoverSourcesForLibrarySkipsCustomCover 保证管理员填了自定义
// 封面时不会再生成拼图（沿用自定义图）。
func TestLibraryCoverSourcesForLibrarySkipsCustomCover(t *testing.T) {
	svc := newTestEmbyService(t)
	lib := model.Library{Name: "电影", Path: "/media/movies", Type: "movie", CoverURL: "https://example.com/custom.jpg"}
	if err := svc.repo.Library.Create(t.Context(), &lib); err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := svc.repo.DB.Create(&model.Media{
		Base:      model.Base{ID: "m1"},
		LibraryID: lib.ID,
		Title:     "M",
		Path:      "/media/movies/m1.mkv",
		PosterURL: "https://img/m1.jpg",
	}).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}
	sources, found := LibraryCoverSourcesForLibrary(t.Context(), svc.repo, lib.ID)
	if found == nil || found.CoverURL != lib.CoverURL {
		t.Fatalf("library = %#v, want the existing library", found)
	}
	if len(sources) != 0 {
		t.Fatalf("expected no generated sources with a custom cover, got %v", sources)
	}
	if _, blank := LibraryCoverSourcesForLibrary(t.Context(), svc.repo, "missing"); blank != nil {
		t.Fatalf("unknown library should return nil, got %#v", blank)
	}
}
