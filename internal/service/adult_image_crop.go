package service

import (
	"bytes"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // register PNG decoder
	"strings"

	"github.com/metatube-community/metatube-sdk-go/detector"
)

const adultPosterRatio = 2.0 / 3.0

var findPrimaryFaceAxisRatio = func(img image.Image, ratio float64, advanced bool) (float64, bool) {
	return detector.FindPrimaryFaceAxisRatio(img, ratio, advanced)
}

// IsAdultMediaPathOrMetadata reports whether media is adult based on path, mediaType, nsfw flag, or adult code.
func IsAdultMediaPathOrMetadata(path, mediaType string, nsfw bool) bool {
	if nsfw {
		return true
	}
	if normalizeOrganizeMediaType(mediaType) == "adult" {
		return true
	}
	if AdultCodeFromMediaPath(path) != "" {
		return true
	}
	return false
}

// IsAdultArtworkURL reports whether the URL is likely an adult cover/poster image.
func IsAdultArtworkURL(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, "javbus") ||
		strings.Contains(lower, "javdb") ||
		strings.Contains(lower, "jdbstatic") ||
		strings.Contains(lower, "busjav") ||
		strings.Contains(lower, "dmmbus") ||
		strings.Contains(lower, "cdnbus") ||
		strings.Contains(lower, "javsee") ||
		strings.Contains(lower, "/covers/") ||
		strings.Contains(lower, "pics.dmm.co.jp")
}

// CropAdultCoverPoster checks if the image data is a wide full-jacket DVD cover (width > height * 1.15).
// If so, it uses MetaTube's Pigo face detector to position a 2:3 poster crop.
// When no face is detected, it falls back to the conventional right-side front cover.
// If the image is already portrait or cannot be decoded, it safely returns the original data.
func CropAdultCoverPoster(data []byte) ([]byte, string, error) {
	if len(data) == 0 {
		return data, "", nil
	}
	src, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, format, err
	}
	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return data, format, nil
	}
	// If the image is already portrait or square, keep it as-is.
	if float64(width) <= float64(height)*1.15 {
		return data, format, nil
	}

	position := 1.0
	if detected, ok := findPrimaryFaceAxisRatio(src, adultPosterRatio, true); ok {
		position = detected
	}
	cropped := cropImageAtPosition(src, adultPosterRatio, position)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, cropped, &jpeg.Options{Quality: 92}); err != nil {
		return data, format, err
	}
	return buf.Bytes(), "image/jpeg", nil
}

func cropImageAtPosition(src image.Image, ratio, position float64) image.Image {
	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	cropWidth, cropHeight := width, height
	x, y := 0, 0
	if candidate := int(float64(height) * ratio); candidate < width {
		cropWidth = candidate
		x = max(min(int(float64(width)*position)-cropWidth/2, width-cropWidth), 0)
	} else if candidate := int(float64(width) / ratio); candidate < height {
		cropHeight = candidate
		y = max(min(int(float64(height)*position)-cropHeight/2, height-cropHeight), 0)
	}
	cropRect := image.Rect(0, 0, cropWidth, cropHeight).
		Add(image.Pt(x, y)).
		Add(bounds.Min)
	if sub, ok := src.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return sub.SubImage(cropRect)
	}
	dst := image.NewRGBA(image.Rect(0, 0, cropRect.Dx(), cropRect.Dy()))
	draw.Draw(dst, dst.Bounds(), src, cropRect.Min, draw.Src)
	return dst
}
