package service

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// encodeTestPNG 生成一张结构规则、易于压缩的测试用 PNG。
func encodeTestPNG(t *testing.T, w, h int, alpha uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 16 * 16), G: uint8(y % 16 * 16), B: 200, A: alpha})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return buf.Bytes()
}

func TestParseImageResizeOptionsReadsEmbyParams(t *testing.T) {
	r := httptest.NewRequest("GET", "/emby/Items/x/Images/Primary?maxWidth=400&maxHeight=600&quality=80", nil)
	o := parseImageResizeOptions(r)
	if o.MaxWidth != 400 || o.MaxHeight != 600 || o.Quality != 80 {
		t.Fatalf("unexpected options: %+v", o)
	}
	if !o.active() {
		t.Fatal("expected options to be active")
	}
}

func TestParseImageResizeOptionsIsCaseInsensitive(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?MaxWidth=250&QUALITY=70", nil)
	o := parseImageResizeOptions(r)
	if o.MaxWidth != 250 {
		t.Fatalf("MaxWidth = %d, want 250", o.MaxWidth)
	}
	if o.Quality != 70 {
		t.Fatalf("Quality = %d, want 70", o.Quality)
	}
}

func TestParseImageResizeOptionsAcceptsWidthAndHeightAliases(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?width=320&height=180", nil)
	o := parseImageResizeOptions(r)
	if o.MaxWidth != 320 || o.MaxHeight != 180 {
		t.Fatalf("unexpected options: %+v", o)
	}
}

func TestParseImageResizeOptionsInactiveWithoutDimensions(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?quality=90&tag=abc", nil)
	o := parseImageResizeOptions(r)
	if o.active() {
		t.Fatalf("expected inactive options, got %+v", o)
	}
}

func TestFitSizePreservesAspectAndNeverUpscales(t *testing.T) {
	cases := []struct {
		name                   string
		srcW, srcH, maxW, maxH int
		wantW, wantH           int
	}{
		{"max-width-only", 529, 911, 400, 0, 400, 689},
		{"max-height-only", 529, 911, 0, 500, 290, 500},
		{"never-upscales", 529, 911, 2000, 2000, 529, 911},
		{"both-bounds", 1000, 500, 400, 400, 400, 200},
		{"exact-size", 400, 600, 400, 600, 400, 600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotW, gotH := fitSize(tc.srcW, tc.srcH, tc.maxW, tc.maxH)
			if gotW != tc.wantW || gotH != tc.wantH {
				t.Fatalf("fitSize(%d,%d,%d,%d) = %dx%d, want %dx%d",
					tc.srcW, tc.srcH, tc.maxW, tc.maxH, gotW, gotH, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestResizeImageDataScalesDownAndReencodesAsJPEG(t *testing.T) {
	data := encodeTestPNG(t, 529, 911, 255)
	out, ctype, unchanged, err := resizeImageData(data, imageResizeOptions{MaxWidth: 400, Quality: 90})
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if unchanged {
		t.Fatal("expected the image to be resized")
	}
	if ctype != "image/jpeg" {
		t.Fatalf("content type = %q, want image/jpeg", ctype)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode resized: %v", err)
	}
	if cfg.Width != 400 || cfg.Height != 689 {
		t.Fatalf("resized to %dx%d, want 400x689", cfg.Width, cfg.Height)
	}
	if format != "jpeg" {
		t.Fatalf("format = %q, want jpeg", format)
	}
	// 这里只断言"重新编码生效且输出可解码"。合成图的压缩率不代表真实海报
	// （规则色块 PNG 极小，而 JPEG 压规则图案反而更大）；真实海报的体积
	// 收益在服务器上用线上素材实测。
	if len(out) == 0 {
		t.Fatal("expected a non-empty resized image")
	}
	if len(out) == len(data) {
		t.Fatal("expected the resized image to be re-encoded")
	}
}

// 尺寸已经在目标框内的 PNG 也必须转成 JPEG。刮削来的海报常是 1000×1500、
// 1MB 级的 PNG；此前这种情况原样直出，一处海报墙就要下载几十 MB，外部网络下
// 每张图要十几秒（线上实测单张 1.3MB / 20 多秒）。
func TestResizeImageDataReencodesPNGWithinBounds(t *testing.T) {
	data := encodeTestPNG(t, 200, 300, 255)
	out, ctype, unchanged, err := resizeImageData(data, imageResizeOptions{MaxWidth: 400, MaxHeight: 600, Quality: 80})
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if unchanged {
		t.Fatal("PNG within the target box must still be re-encoded as a compact web format")
	}
	if ctype != "image/jpeg" {
		t.Fatalf("content type = %q, want image/jpeg", ctype)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode re-encoded image: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("format = %q, want jpeg", format)
	}
	if cfg.Width != 200 || cfg.Height != 300 {
		t.Fatalf("re-encoded to %dx%d, want the original 200x300 (no upscaling)", cfg.Width, cfg.Height)
	}
}

// 已经是紧凑 Web 格式且体积很小的源图仍然零成本直出。
func TestResizeImageDataLeavesSmallCompactSourceUntouched(t *testing.T) {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 200, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 16 * 16), G: uint8(y % 16 * 16), B: 200, A: 255})
		}
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("encode test jpeg: %v", err)
	}
	data := buf.Bytes()
	if len(data) > compactImageSkipBytes {
		t.Fatalf("test jpeg is %d bytes, expected a compact source", len(data))
	}
	out, ctype, unchanged, err := resizeImageData(data, imageResizeOptions{MaxWidth: 400, MaxHeight: 600})
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if !unchanged {
		t.Fatal("a compact JPEG already within bounds should be served unchanged")
	}
	if !bytes.Equal(out, data) || ctype != "image/jpeg" {
		t.Fatalf("expected the original jpeg bytes back, got %d bytes of %q", len(out), ctype)
	}
}

func TestResizeImageDataKeepsTransparencyAsPNG(t *testing.T) {
	data := encodeTestPNG(t, 600, 900, 128)
	out, ctype, unchanged, err := resizeImageData(data, imageResizeOptions{MaxWidth: 300, Quality: 90})
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if unchanged {
		t.Fatal("expected the image to be resized")
	}
	if ctype != "image/png" {
		t.Fatalf("content type = %q, want image/png so transparency is preserved", ctype)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode resized: %v", err)
	}
	if format != "png" {
		t.Fatalf("format = %q, want png", format)
	}
	if cfg.Width != 300 || cfg.Height != 450 {
		t.Fatalf("resized to %dx%d, want 300x450", cfg.Width, cfg.Height)
	}
}

func TestImageResizeOptionsCacheKeyVariesWithParametersAndSource(t *testing.T) {
	stat, err := os.Stat("image_resize_test.go")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	base := imageResizeOptions{MaxWidth: 400, Quality: 90}
	if base.resizeCacheKey("a", stat) != base.resizeCacheKey("a", stat) {
		t.Fatal("cache key must be stable for identical inputs")
	}
	if base.resizeCacheKey("a", stat) == base.resizeCacheKey("b", stat) {
		t.Fatal("cache key must differ between sources")
	}
	if base.resizeCacheKey("a", stat) == (imageResizeOptions{MaxWidth: 200, Quality: 90}).resizeCacheKey("a", stat) {
		t.Fatal("cache key must differ when max width changes")
	}
	if base.resizeCacheKey("a", stat) == (imageResizeOptions{MaxWidth: 400, Quality: 60}).resizeCacheKey("a", stat) {
		t.Fatal("cache key must differ when quality changes")
	}
}

func TestImageResizeOptionsEncodingQualityFallsBackToDefault(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want int
	}{
		{0, imageResizeDefaultQuality},
		{-5, imageResizeDefaultQuality},
		{500, imageResizeDefaultQuality},
		{1, 1},
		{100, 100},
		{75, 75},
	} {
		if got := (imageResizeOptions{Quality: tc.in}).encodingQuality(); got != tc.want {
			t.Fatalf("encodingQuality(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestServeResizedFromFileCachesScaledResult(t *testing.T) {
	dir := t.TempDir()
	mediaDir := dir + string(os.PathSeparator) + "media"
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := mediaDir + string(os.PathSeparator) + "poster.png"
	if err := os.WriteFile(src, encodeTestPNG(t, 529, 911, 255), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	proxy := &ImageProxy{cacheDir: dir + string(os.PathSeparator) + "cache"}
	opts := imageResizeOptions{MaxWidth: 400, Quality: 90}

	first := httptest.NewRecorder()
	if !proxy.serveResizedFromFile(first, httptest.NewRequest("GET", "/x?maxWidth=400", nil), src, opts) {
		t.Fatal("expected serveResizedFromFile to handle the request")
	}
	if got := first.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("content type = %q, want image/jpeg", got)
	}

	// 第二次请求应命中磁盘缓存，返回与首次完全相同的字节。
	second := httptest.NewRecorder()
	if !proxy.serveResizedFromFile(second, httptest.NewRequest("GET", "/x?maxWidth=400", nil), src, opts) {
		t.Fatal("expected second call to be served")
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatal("expected the cached scaled image to be reused")
	}
	if len(first.Body.Bytes()) == 0 {
		t.Fatal("expected a non-empty body")
	}
}

func TestServeResizedFromFileUsesCacheBeforeDecodingSource(t *testing.T) {
	dir := t.TempDir()
	mediaDir := dir + string(os.PathSeparator) + "media"
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := mediaDir + string(os.PathSeparator) + "poster.png"
	original := encodeTestPNG(t, 529, 911, 255)
	if err := os.WriteFile(src, original, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	proxy := &ImageProxy{cacheDir: dir + string(os.PathSeparator) + "cache"}
	opts := imageResizeOptions{MaxWidth: 400, Quality: 90}

	first := httptest.NewRecorder()
	if !proxy.serveResizedFromFile(first, httptest.NewRequest("GET", "/x?maxWidth=400", nil), src, opts) {
		t.Fatal("expected first call to be served")
	}

	stat, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	// Same size and mtime keep the resize cache key stable, but the source is
	// now invalid image data. A correct implementation serves the cached
	// thumbnail before reading/decoding the source again.
	broken := bytes.Repeat([]byte{0}, len(original))
	if err := os.WriteFile(src, broken, 0o644); err != nil {
		t.Fatalf("overwrite source: %v", err)
	}
	if err := os.Chtimes(src, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatalf("restore mtime: %v", err)
	}

	second := httptest.NewRecorder()
	if !proxy.serveResizedFromFile(second, httptest.NewRequest("GET", "/x?maxWidth=400", nil), src, opts) {
		t.Fatal("expected second call to be served from resize cache")
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatal("expected cached thumbnail to be reused without decoding the source")
	}
}

func TestServeResizedFromFileSkipsDecodeForCompactJPEG(t *testing.T) {
	dir := t.TempDir()
	src := dir + string(os.PathSeparator) + "poster.jpg"
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 800, 1200)), &jpeg.Options{Quality: 40}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	if buf.Len() == 0 || buf.Len() > compactImageSkipBytes {
		t.Fatalf("test jpeg is %d bytes, want a compact poster", buf.Len())
	}
	if err := os.WriteFile(src, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	proxy := &ImageProxy{cacheDir: dir + string(os.PathSeparator) + "cache"}
	rec := httptest.NewRecorder()
	if !proxy.serveResizedFromFile(rec, httptest.NewRequest("GET", "/x?maxWidth=120", nil), src, imageResizeOptions{MaxWidth: 120}) {
		t.Fatal("expected compact jpeg to be served")
	}
	if !bytes.Equal(rec.Body.Bytes(), buf.Bytes()) {
		t.Fatal("expected the original jpeg, not a decoded thumbnail")
	}
}

// 排队上限：槽位一直不空时，请求最多等 resizeQueueWait，然后回退原图，
// 不会被无限期挂住。这条约束保护的是请求本身，而不是「不等就直出原图」。
func TestServeResizedFromFileFallsBackWhenResizeQueueStaysBusy(t *testing.T) {
	dir := t.TempDir()
	src := dir + string(os.PathSeparator) + "backdrop.jpg"
	data := make([]byte, compactImageSkipBytes+1024)
	data[0], data[1], data[2] = 0xff, 0xd8, 0xff
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	proxy := &ImageProxy{cacheDir: dir + string(os.PathSeparator) + "cache", resizeQueueWait: 50 * time.Millisecond}
	var releases []func()
	for i := 0; i < imageResizeConcurrency(); i++ {
		release, ok := proxy.acquireResizeSlot(context.Background())
		if !ok {
			t.Fatal("expected a resize slot")
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()

	start := time.Now()
	handled := proxy.serveResizedFromFile(httptest.NewRecorder(), httptest.NewRequest("GET", "/x?maxWidth=480", nil), src, imageResizeOptions{MaxWidth: 480})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("request waited %v with every slot held; the queue wait must be bounded", elapsed)
	}
	if handled {
		t.Fatal("expected a permanently busy queue to fall back so the caller can serve the original")
	}
}

// 槽位只是短暂忙碌时必须排队等待并产出缩略图，而不是直出多兆字节原图。
// 这是线上「海报墙每张图 20 多秒」的根因：并发加载时槽位全忙，旧逻辑把
// 1MB 级 PNG 原样下发。
// encodeNoisyPNG 生成难以压缩的 PNG：规则色块会被压到 1KB 级别，测不出
// 「大体积 PNG 被原样下发」这条回归。
func encodeNoisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(0x9e3779b9)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed = seed*1664525 + 1013904223
			img.Set(x, y, color.RGBA{R: uint8(seed >> 24), G: uint8(seed >> 16), B: uint8(seed >> 8), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode noisy png: %v", err)
	}
	return buf.Bytes()
}

func TestServeResizedFromFileWaitsForSlotInsteadOfServingOriginal(t *testing.T) {
	dir := t.TempDir()
	src := dir + string(os.PathSeparator) + "poster.png"
	original := encodeNoisyPNG(t, 378, 540)
	if len(original) <= compactImageSkipBytes {
		t.Fatalf("test poster is %d bytes, want a source above the compact threshold", len(original))
	}
	if err := os.WriteFile(src, original, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	proxy := &ImageProxy{cacheDir: dir + string(os.PathSeparator) + "cache"}
	var releases []func()
	for i := 0; i < imageResizeConcurrency(); i++ {
		release, ok := proxy.acquireResizeSlot(context.Background())
		if !ok {
			t.Fatal("expected a resize slot")
		}
		releases = append(releases, release)
	}
	go func() {
		time.Sleep(40 * time.Millisecond)
		releases[0]()
	}()

	rec := httptest.NewRecorder()
	if !proxy.serveResizedFromFile(rec, httptest.NewRequest("GET", "/x?maxWidth=480&maxHeight=600", nil), src, imageResizeOptions{MaxWidth: 480, MaxHeight: 600}) {
		t.Fatal("expected the request to wait for the slot and serve a thumbnail")
	}
	body := rec.Body.Bytes()
	if bytes.Equal(body, original) {
		t.Fatal("served the multi-hundred-KB PNG original instead of a re-encoded thumbnail")
	}
	if rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("content type = %q, want image/jpeg", rec.Header().Get("Content-Type"))
	}
	// 同一请求第二次必须直接命中缩放缓存。
	rec2 := httptest.NewRecorder()
	if !proxy.serveResizedFromFile(rec2, httptest.NewRequest("GET", "/x?maxWidth=480&maxHeight=600", nil), src, imageResizeOptions{MaxWidth: 480, MaxHeight: 600}) {
		t.Fatal("expected the cached thumbnail to be reused")
	}
	if !bytes.Equal(rec2.Body.Bytes(), body) {
		t.Fatal("expected the cached thumbnail bytes to be identical")
	}
}
