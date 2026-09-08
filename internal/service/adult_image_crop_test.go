package service

import (
	"bytes"
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

func TestCropAdultCoverPosterCentersDetectedFace(t *testing.T) {
	originalDetector := findPrimaryFaceAxisRatio
	findPrimaryFaceAxisRatio = func(image.Image, float64, bool) (float64, bool) {
		return 0.25, true
	}
	defer func() {
		findPrimaryFaceAxisRatio = originalDetector
	}()

	origBytes := createTestImage(900, 600, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255})
	croppedBytes, _, err := CropAdultCoverPoster(origBytes)
	if err != nil {
		t.Fatal(err)
	}
	croppedImg, _, err := image.Decode(bytes.NewReader(croppedBytes))
	if err != nil {
		t.Fatal(err)
	}
	r, _, b, _ := croppedImg.At(croppedImg.Bounds().Dx()/2, croppedImg.Bounds().Dy()/2).RGBA()
	if r <= b {
		t.Fatalf("face-positioned crop did not follow detected left-side axis: r=%d b=%d", r, b)
	}
}

func TestCropAdultCoverPosterKeepsPortrait(t *testing.T) {
	// Create an already vertical portrait image (500x700)
	origBytes := createTestImage(500, 700, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255})

	croppedBytes, _, err := CropAdultCoverPoster(origBytes)
	if err != nil {
		t.Fatalf("CropAdultCoverPoster error: %v", err)
	}

	croppedImg, _, err := image.Decode(bytes.NewReader(croppedBytes))
	if err != nil {
		t.Fatalf("decode image: %v", err)
	}
	if croppedImg.Bounds().Dx() != 500 || croppedImg.Bounds().Dy() != 700 {
		t.Fatalf("portrait image was unexpectedly resized: %dx%d", croppedImg.Bounds().Dx(), croppedImg.Bounds().Dy())
	}
}
