package service

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"testing"
)

func createTestImage(width, height int, leftColor, rightColor color.Color) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, image.Rect(0, 0, width/2, height), &image.Uniform{C: leftColor}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(width/2, 0, width, height), &image.Uniform{C: rightColor}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	return buf.Bytes()
}

func TestCropAdultCoverPosterWideLandscape(t *testing.T) {
	// Create an 800x538 wide DVD jacket cover
	origBytes := createTestImage(800, 538, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255})

	croppedBytes, ctype, err := CropAdultCoverPoster(origBytes)
	if err != nil {
		t.Fatalf("CropAdultCoverPoster error: %v", err)
	}
	if ctype != "image/jpeg" {
		t.Fatalf("expected image/jpeg, got %s", ctype)
	}

	croppedImg, _, err := image.Decode(bytes.NewReader(croppedBytes))
	if err != nil {
		t.Fatalf("decode cropped image: %v", err)
	}
	bounds := croppedImg.Bounds()
	croppedWidth := bounds.Dx()
	croppedHeight := bounds.Dy()

	if croppedHeight != 538 {
		t.Fatalf("expected height 538, got %d", croppedHeight)
	}
	if croppedWidth <= 0 || croppedWidth >= 800 {
		t.Fatalf("unexpected cropped width: %d", croppedWidth)
	}
	// MetaTube's default primary-image ratio is 2:3.
	ratio := float64(croppedWidth) / float64(croppedHeight)
	if ratio < 0.65 || ratio > 0.68 {
		t.Fatalf("expected portrait ratio ~0.667, got %f (%dx%d)", ratio, croppedWidth, croppedHeight)
	}

	// Verify the cropped image contains the right side color (blue), not the left side color (red)
	midPixel := croppedImg.At(croppedWidth/2, croppedHeight/2)
	r, _, b, _ := midPixel.RGBA()
	if b < r {
		t.Fatalf("expected right side image content (dominant blue), got r=%d b=%d", r, b)
	}
}

func TestCropAdultCoverPosterDecodesWebP(t *testing.T) {
	wideWebP, err := base64.StdEncoding.DecodeString("UklGRjwAAABXRUJQVlA4IDAAAADQAQCdASoJAAYAAUAmJaACdLoB+AADsAD+8ut//NgVzXPv9//S4P0uD9Lg/9KQAAA=")
	if err != nil {
		t.Fatal(err)
	}
	croppedBytes, ctype, err := CropAdultCoverPoster(wideWebP)
	if err != nil {
		t.Fatalf("crop WebP: %v", err)
	}
	if ctype != "image/jpeg" {
		t.Fatalf("cropped WebP type = %q, want image/jpeg", ctype)
	}
	cropped, _, err := image.Decode(bytes.NewReader(croppedBytes))
	if err != nil {
		t.Fatal(err)
	}
	if got := float64(cropped.Bounds().Dx()) / float64(cropped.Bounds().Dy()); got < 0.65 || got > 0.68 {
		t.Fatalf("cropped WebP ratio = %.3f, want 2:3", got)
	}
}

func TestCropAdultCoverPosterUsesDetectedPanelForSpecialNumber(t *testing.T) {
	originalDetector := findPrimaryFaceAxisRatio
	findPrimaryFaceAxisRatio = func(image.Image, float64, bool) (float64, bool) {
		return 0.25, true
	}
	defer func() {
		findPrimaryFaceAxisRatio = originalDetector
	}()

	origBytes := createTestImage(900, 600, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255})
	// 素人/FC2 这类番号构图不固定，允许人脸检测决定取哪一侧面板。
	croppedBytes, _, err := CropAdultCoverPosterForCode(origBytes, "FC2-PPV-123456")
	if err != nil {
		t.Fatal(err)
	}
	croppedImg, _, err := image.Decode(bytes.NewReader(croppedBytes))
	if err != nil {
		t.Fatal(err)
	}
	r, _, b, _ := croppedImg.At(croppedImg.Bounds().Dx()/2, croppedImg.Bounds().Dy()/2).RGBA()
	if r <= b {
		t.Fatalf("special-number crop did not follow detected left-side axis: r=%d b=%d", r, b)
	}
}

// 常规 DVD 番号的封套是 [封底 | 书脊 | 封面] 横图，封底那半张是样本剧照拼贴、充满人脸。
// 做人脸检测会让主簇落在拼贴或书脊上，裁出「半张封底 + 半张封面」。
// 这里模拟检测器给出跨缝的 0.42，要求常规番号完全忽略检测结果，只裁最右侧封面面板。
func TestCropAdultCoverPosterIgnoresFaceDetectionForStandardNumber(t *testing.T) {
	originalDetector := findPrimaryFaceAxisRatio
	findPrimaryFaceAxisRatio = func(image.Image, float64, bool) (float64, bool) {
		return 0.42, true
	}
	defer func() {
		findPrimaryFaceAxisRatio = originalDetector
	}()

	origBytes := createTestImage(900, 600, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255})
	croppedBytes, _, err := CropAdultCoverPosterForCode(origBytes, "SSNI-677")
	if err != nil {
		t.Fatal(err)
	}
	croppedImg, _, err := image.Decode(bytes.NewReader(croppedBytes))
	if err != nil {
		t.Fatal(err)
	}
	r, _, b, _ := croppedImg.At(croppedImg.Bounds().Dx()/2, croppedImg.Bounds().Dy()/2).RGBA()
	if b <= r {
		t.Fatalf("standard-number crop should be the right-most panel (blue), got r=%d b=%d", r, b)
	}
}

// 即使需要人脸定位，裁切框也必须吸附到整块面板，绝不能跨在书脊上。
func TestCropAdultCoverPosterSnapsToPanelForSpecialNumber(t *testing.T) {
	originalDetector := findPrimaryFaceAxisRatio
	findPrimaryFaceAxisRatio = func(image.Image, float64, bool) (float64, bool) {
		return 0.42, true // 介于两个面板之间：落在书脊上
	}
	defer func() {
		findPrimaryFaceAxisRatio = originalDetector
	}()

	origBytes := createTestImage(900, 600, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255})
	croppedBytes, _, err := CropAdultCoverPosterForCode(origBytes, "HEYZO-1234")
	if err != nil {
		t.Fatal(err)
	}
	croppedImg, _, err := image.Decode(bytes.NewReader(croppedBytes))
	if err != nil {
		t.Fatal(err)
	}
	bounds := croppedImg.Bounds()
	// 左侧面板 = 纯红；任何偏蓝像素都意味着裁切框越过了中线。
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 8 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 8 {
			r, _, b, _ := croppedImg.At(x, y).RGBA()
			if b > r {
				t.Fatalf("crop straddles the panel seam at (%d,%d): r=%d b=%d", x, y, r, b)
			}
		}
	}
}

func TestCropAdultCoverPosterKeepsPortrait(t *testing.T) {
	// Create an already vertical portrait image (500x700)
	origBytes := createTestImage(500, 700, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255})

	croppedBytes, ctype, err := CropAdultCoverPoster(origBytes)
	if err != nil {
		t.Fatalf("CropAdultCoverPoster error: %v", err)
	}
	// 未裁剪时必须回传真正的 MIME（image/jpeg），否则侧车文件扩展名会退化成 .img。
	if ctype != "image/jpeg" {
		t.Fatalf("portrait ctype = %q, want image/jpeg", ctype)
	}

	croppedImg, _, err := image.Decode(bytes.NewReader(croppedBytes))
	if err != nil {
		t.Fatalf("decode image: %v", err)
	}
	if croppedImg.Bounds().Dx() != 500 || croppedImg.Bounds().Dy() != 700 {
		t.Fatalf("portrait image was unexpectedly resized: %dx%d", croppedImg.Bounds().Dx(), croppedImg.Bounds().Dy())
	}
}
