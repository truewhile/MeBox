package service

import (
	"bytes"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // register PNG decoder
	"strings"

	"github.com/metatube-community/metatube-sdk-go/common/number"
	"github.com/metatube-community/metatube-sdk-go/detector"
	_ "golang.org/x/image/webp" // register WebP decoder for AVE covers
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

// CropAdultCoverPoster checks if the image data is a wide full-jacket DVD cover (width > height * 1.15)
// and crops the conventional right-side front cover out of it. It is the code-agnostic entry point;
// prefer CropAdultCoverPosterForCode when the movie number is known.
// If the image is already portrait or cannot be decoded, it safely returns the original data.
func CropAdultCoverPoster(data []byte) ([]byte, string, error) {
	return CropAdultCoverPosterForCode(data, "")
}

// CropAdultCoverPosterForCode crops a 2:3 poster out of a wide full-jacket DVD cover.
//
// Layout matters: a regular DVD jacket is [封底 | 书脊 | 封面] unfolded right-to-left, so the
// front cover is always the right-most panel — exactly what upstream MetaTube does with its
// default position 1.0. Face detection is only meaningful for "special" numbers
// (FC2 / HEYZO / 素人 / 无码) whose cover is a snapshot whose composition varies
// (see metatube-sdk-go/common/number.RequiresFaceDetection).
//
// Running the detector on a regular jacket is actively harmful: the back cover of those jackets is a
// montage of sample stills that is packed with faces, so the dominant face cluster lands on the montage
// (or right on the spine), and a crop centred there straddles the seam giving "half back cover + half
// front cover" posters.
func CropAdultCoverPosterForCode(data []byte, code string) ([]byte, string, error) {
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
	if adultCoverNeedsFaceDetection(code) {
		if detected, ok := findPrimaryFaceAxisRatio(src, adultPosterRatio, true); ok {
			position = snapAdultCoverPosition(detected)
		}
	}
	cropped := cropImageAtPosition(src, adultPosterRatio, position)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, cropped, &jpeg.Options{Quality: 92}); err != nil {
		return data, format, err
	}
	return buf.Bytes(), "image/jpeg", nil
}

// adultCoverNeedsFaceDetection reports whether the cover composition has to be probed with the face
// detector, mirroring upstream MetaTube: only "special" numbers (FC2 / HEYZO / 素人 / 无码) qualify,
// regular DVD numbers are always cropped at the right-most panel.
func adultCoverNeedsFaceDetection(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	return number.RequiresFaceDetection(code)
}

// snapAdultCoverPosition quantises a detected face axis onto a whole panel. A wide jacket only has two
// legal framings — the left panel or the right panel — so the crop window must never be centred on the
// detected axis itself: when that axis sits on/near the spine the result is a poster showing the right
// half of the back cover plus the left sliver of the front cover.
func snapAdultCoverPosition(position float64) float64 {
	if position >= 0.5 {
		return 1.0 // right panel: flush with the right edge
	}
	return 0.0 // left panel: flush with the left edge
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
