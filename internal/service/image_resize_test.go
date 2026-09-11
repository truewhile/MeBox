package service

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"os"
	"testing"
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

func TestResizeImageDataLeavesImagesWithinBoundsUntouched(t *testing.T) {
	data := encodeTestPNG(t, 200, 300, 255)
	out, _, unchanged, err := resizeImageData(data, imageResizeOptions{MaxWidth: 400, MaxHeight: 600})
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if !unchanged {
		t.Fatal("expected an image already within bounds to be returned unchanged")
	}
	if !bytes.Equal(out, data) {
		t.Fatal("expected the original bytes to be returned verbatim")
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
