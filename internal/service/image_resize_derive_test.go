package service

import (
	"bytes"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestParseResizeVariantSuffixRoundTrip(t *testing.T) {
	opts := imageResizeOptions{MaxWidth: 480, MaxHeight: 600, Quality: 80}
	name := "abc123." + opts.resizeVariantSuffix() + ".img"

	width, height, ok := parseResizeVariantSuffix(name, "abc123")
	if !ok {
		t.Fatalf("expected %q to parse", name)
	}
	if width != 480 || height != 600 {
		t.Fatalf("parsed %dx%d, want 480x600", width, height)
	}

	for _, bad := range []string{"abc123.img", "abc124.480x600q80.img", "abc123.480x600.img", "abc123.axbq80.img"} {
		if _, _, ok := parseResizeVariantSuffix(bad, "abc123"); ok {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestLargerCachedVariantPicksSmallestCoveringVariant(t *testing.T) {
	proxy := &ImageProxy{cacheDir: filepath.Join(t.TempDir(), "cache"), log: zap.NewNop()}
	dir := filepath.Join(proxy.cacheDir, imageResizeCacheSubdir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	key := "deadbeef"
	writeVariant := func(o imageResizeOptions) string {
		path := proxy.resizeCachePath(key, o)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// 更小的档位不能用于派生（会放大），更大的档位里要挑最小的那个。
	writeVariant(imageResizeOptions{MaxWidth: 96, Quality: 60})
	large := writeVariant(imageResizeOptions{MaxWidth: 1920, MaxHeight: 1080, Quality: 80})
	card := writeVariant(imageResizeOptions{MaxWidth: 480, MaxHeight: 600, Quality: 80})

	got := proxy.largerCachedVariant(key, imageResizeOptions{MaxWidth: 160, Quality: 60})
	if got != card {
		t.Fatalf("largerCachedVariant = %q, want the card variant %q", got, card)
	}

	// 目标尺寸已与大档位相同（甚至更大）时没有可用来源。
	if got := proxy.largerCachedVariant(key, imageResizeOptions{MaxWidth: 1920, MaxHeight: 1080, Quality: 80}); got != "" {
		t.Fatalf("expected no derivable source when the target equals the only covering variant, got %q", got)
	}
	if got := proxy.largerCachedVariant(key, imageResizeOptions{MaxWidth: 2400, Quality: 80}); got != "" {
		t.Fatalf("expected no derivable source for oversize targets, got %q", got)
	}
	if large == "" {
		t.Fatal("large variant should have been written")
	}
}

// TestServeResizedDerivesSmallVariantFromCachedCard 小尺寸档位必须能从已缓存
// 的大尺寸档位派生，而不是每次都重新解码原图。
//
// 验证方式：生成卡片档位后，把原图内容替换成同长度、同 mtime 的非图片数据
// （缓存键不变，但原图已无法解码）。若小图请求仍能返回正确的缩略图，就说明它
// 是从缓存档位派生的；否则只能退回原图直出（此处会失败，因为原图已不是图片）。
func TestServeResizedDerivesSmallVariantFromCachedCard(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "poster.png")
	original := encodeTestPNG(t, 1200, 1800, 255)
	if err := os.WriteFile(source, original, 0o644); err != nil {
		t.Fatal(err)
	}
	// 固定 mtime 并读回平台量化后的值，保证替换内容后缓存键不变。
	fixed := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(source, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	srcStat, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}

	proxy := &ImageProxy{cacheDir: filepath.Join(dir, "cache"), log: zap.NewNop()}

	card := imageResizeOptions{MaxWidth: 480, MaxHeight: 600, Quality: 80}
	cardRec := httptest.NewRecorder()
	if !proxy.serveResizedFromFile(cardRec, httptest.NewRequest("GET", "/x?maxWidth=480", nil), source, card) {
		t.Fatal("expected the card variant request to be handled")
	}
	cardCfg, _, err := image.DecodeConfig(bytes.NewReader(cardRec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode card variant: %v", err)
	}
	if cardCfg.Width != 400 || cardCfg.Height != 600 {
		t.Fatalf("card variant = %dx%d, want 400x600", cardCfg.Width, cardCfg.Height)
	}

	// 把原图换成同样长度的非图片数据，并恢复完全相同的 mtime，保持缓存键不变。
	garbage := bytes.Repeat([]byte{0x11}, len(original))
	if err := os.WriteFile(source, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(source, srcStat.ModTime(), srcStat.ModTime()); err != nil {
		t.Fatal(err)
	}

	tiny := imageResizeOptions{MaxWidth: 160, Quality: 60}
	tinyRec := httptest.NewRecorder()
	if !proxy.serveResizedFromFile(tinyRec, httptest.NewRequest("GET", "/x?maxWidth=160", nil), source, tiny) {
		t.Fatal("expected the tiny variant request to be handled")
	}
	tinyCfg, _, err := image.DecodeConfig(bytes.NewReader(tinyRec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode tiny variant: %v (the request fell back to the placeholder instead of deriving from the cached card)", err)
	}
	if tinyCfg.Width != 160 || tinyCfg.Height != 240 {
		t.Fatalf("tiny variant = %dx%d, want 160x240", tinyCfg.Width, tinyCfg.Height)
	}
}

// TestPrefetchCardVariantWarmsResizeCache 刮削预取要顺带生成卡片档位，
// 首个海报墙请求即可命中缩放缓存。
func TestPrefetchCardVariantWarmsResizeCache(t *testing.T) {
	payload := encodeTestPNG(t, 1200, 1800, 255)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	proxy := newStreamImageProxy(t, upstream.URL)
	raw := upstream.URL + "/poster.png"

	if err := proxy.PrefetchCardVariant(t.Context(), raw); err != nil {
		t.Fatalf("PrefetchCardVariant failed: %v", err)
	}

	_, cachePath, _ := proxy.remoteImageCachePathsForValidated(raw)
	stat, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("expected the original to be cached: %v", err)
	}
	variantPath := proxy.resizeCachePath(prefetchCardResizeOptions.resizeSourceKey(cachePath, stat), prefetchCardResizeOptions)
	if _, err := os.Stat(variantPath); err != nil {
		t.Fatalf("expected the card variant to be pre-generated at %s: %v", variantPath, err)
	}

	// 预取失败时仍要返回错误（刮削据此保留旧图）。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer bad.Close()
	failing := newStreamImageProxy(t, bad.URL)
	if err := failing.PrefetchCardVariant(t.Context(), bad.URL+"/poster.png"); err == nil {
		t.Fatal("expected an error for an unreachable candidate image")
	}
}

// TestPrefetchCardVariantSurvivesUnresizableImage 上游返回的图无法缩放时，
// 预取仍视为成功（图片可达即可），不能因此让刮削回退到旧图。
func TestPrefetchCardVariantSurvivesUnresizableImage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(testJPEG)
	}))
	defer upstream.Close()

	proxy := newStreamImageProxy(t, upstream.URL)
	if err := proxy.PrefetchCardVariant(t.Context(), upstream.URL+"/cover.jpg"); err != nil {
		t.Fatalf("PrefetchCardVariant failed: %v", err)
	}
}
