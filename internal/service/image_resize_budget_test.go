package service

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// encodeTestJPEG 生成一张便于压缩的测试用 JPEG（比 PNG 更适合构造大尺寸样本）。
func encodeTestJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 32 * 8), G: uint8(y % 32 * 8), B: 150, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatalf("encode test jpeg: %v", err)
	}
	return buf.Bytes()
}

// TestResizeImageDataRejectsOverPixelBudget 锁定解码像素预算：超过上限的
// 原图必须放弃缩放（回退原图直出），而不是在小内存主机上尝试解码。
func TestResizeImageDataRejectsOverPixelBudget(t *testing.T) {
	// 12_000_000 像素预算之上：3000×5000 = 1500 万。
	data := encodeTestJPEG(t, 3000, 5000)

	if _, _, _, err := resizeImageData(data, imageResizeOptions{MaxWidth: 400}); !errors.Is(err, errImageResizeTooLarge) {
		t.Fatalf("expected errImageResizeTooLarge, got %v", err)
	}
}

// TestResizeImageDataAcceptsRealisticPoster 常见高清海报（2892×4096，约
// 1180 万像素）必须仍在预算之内，否则真实海报会退化成直出多兆字节原图。
func TestResizeImageDataAcceptsRealisticPoster(t *testing.T) {
	data := encodeTestJPEG(t, 2892, 4096)

	out, ctype, unchanged, err := resizeImageData(data, imageResizeOptions{MaxWidth: 480, MaxHeight: 600})
	if err != nil {
		t.Fatalf("resizeImageData failed: %v", err)
	}
	if unchanged {
		t.Fatal("expected a resized result, got the original bytes")
	}
	if ctype != "image/jpeg" {
		t.Fatalf("content type = %q, want image/jpeg", ctype)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode resized output: %v", err)
	}
	if cfg.Width != 424 || cfg.Height != 600 {
		t.Fatalf("resized to %dx%d, want 424x600", cfg.Width, cfg.Height)
	}
}

// TestUseCheapScaleInterpolator 大比例缩小走低成本插值，轻微缩小仍保持高质量。
func TestUseCheapScaleInterpolator(t *testing.T) {
	cases := []struct {
		name                   string
		dstW, dstH, srcW, srcH int
		want                   bool
	}{
		{"poster to card", 480, 600, 2892, 4096, true},
		{"hero to strip", 480, 320, 1920, 1080, true},
		{"exactly half is cheap", 960, 540, 1920, 1080, true},
		{"slight shrink", 900, 540, 1000, 600, false},
		{"upscale", 1200, 800, 600, 400, false},
		{"zero source", 100, 100, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := useCheapScaleInterpolator(tc.dstW, tc.dstH, tc.srcW, tc.srcH); got != tc.want {
				t.Fatalf("useCheapScaleInterpolator(%d,%d,%d,%d) = %v, want %v",
					tc.dstW, tc.dstH, tc.srcW, tc.srcH, got, tc.want)
			}
		})
	}
}

// TestIsOpaqueImageDetectsAlpha 透明度检测：全不透明才允许转 JPEG。
func TestIsOpaqueImageDetectsAlpha(t *testing.T) {
	opaque := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			opaque.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	if !isOpaqueImage(opaque) {
		t.Fatal("expected fully opaque image to be reported opaque")
	}

	withAlpha := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			withAlpha.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	withAlpha.Set(2, 2, color.RGBA{R: 10, G: 20, B: 30, A: 128})
	if isOpaqueImage(withAlpha) {
		t.Fatal("expected image with a translucent pixel to be reported non-opaque")
	}

	if isOpaqueImage(nil) {
		t.Fatal("expected nil image to be reported non-opaque")
	}
}
