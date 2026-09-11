package service

import (
	"os"
	"path/filepath"
	"strings"
)

// transferSidecarNFO moves/copies/links the .nfo sidecar alongside its media
// using the same transfer mode, so metadata follows the organized file.
func transferSidecarNFO(srcMedia, dstMedia string, mode TransferMode) error {
	src := nfoPath(srcMedia)
	dst := nfoPath(dstMedia)
	if src == dst {
		return nil
	}
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { // #nosec G301 -- sidecar media directories must remain readable by NAS/player users.
		return err
	}
	return transferFile(src, dst, mode)
}

// artworkSidecarSuffixes lists the poster/backdrop sidecar name suffixes that
// scrapers write next to a media file. It is deliberately conservative: we only
// follow the canonical "<base>-poster" / "<base>-backdrop" names and their
// Emby/Jellyfin variants, all scoped to the same base name as the media.
var artworkSidecarSuffixes = []string{
	"-poster", ".poster",
	"-backdrop", ".backdrop",
	"-fanart", ".fanart", "-landscape",
	"-cover", ".cover", "-thumb", ".thumb",
}

// artworkSidecarExtensions are the image extensions a sidecar may use. Both
// "base-poster.jpg" (bare separator) and "base.poster.jpg" (dotted separator)
// rely on suffix matching, so we probe the common image extensions.
var artworkSidecarExtensions = []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp", ".tbn"}

// transferSidecarArtwork moves/copies/links the scraped poster/backdrop
// sidecar files alongside its media using the same transfer mode, mirroring
// transferSidecarNFO. Previously organize moved only the .nfo and left posters
// behind in the old folder; this keeps artwork with the organized file.
func transferSidecarArtwork(srcMedia, dstMedia string, mode TransferMode) error {
	srcDir := filepath.Dir(srcMedia)
	bases := mediaSidecarBaseVariants(srcMedia)
	if len(bases) == 0 {
		return nil
	}
	// Find every existing sidecar by probing suffix + extension combinations.
	sources := make([]string, 0, len(artworkSidecarSuffixes)*len(artworkSidecarExtensions)*len(bases))
	seen := map[string]struct{}{}
	for _, base := range bases {
		for _, suffix := range artworkSidecarSuffixes {
			for _, ext := range artworkSidecarExtensions {
				path := filepath.Join(srcDir, base+suffix+ext)
				key := strings.ToLower(filepath.Clean(path))
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				if _, err := os.Stat(path); err == nil {
					sources = append(sources, path)
				}
			}
		}
	}
	if len(sources) == 0 {
		return nil
	}
	dstDir := filepath.Dir(dstMedia)
	if err := os.MkdirAll(dstDir, 0o755); err != nil { // #nosec G301 -- sidecar media directories must remain readable by NAS/player users.
		return err
	}
	dstBase := mediaSidecarBase(dstMedia)
	if dstBase == "" {
		dstBase = strings.TrimSuffix(filepath.Base(dstMedia), filepath.Ext(dstMedia))
	}
	srcAdultCode := AdultCodeFromMediaPath(srcMedia)
	for _, src := range sources {
		name := filepath.Base(src)
		// Remap any legacy "Title.mkv-poster.jpg" onto the shared destination stem.
		suffix := ""
		lowerName := strings.ToLower(name)
		for _, base := range bases {
			prefix := strings.ToLower(base)
			if strings.HasPrefix(lowerName, prefix) {
				suffix = name[len(base):]
				break
			}
		}
		dstName := name
		if srcAdultCode != "" && strings.HasPrefix(strings.ToLower(name), strings.ToLower(srcAdultCode)) {
			// Adult sidecars are keyed by code (e.g. ADN-188-poster.jpg) and should
			// follow the media without being renamed to the display title stem.
		} else if suffix != "" && dstBase != "" {
			dstName = dstBase + suffix
		}
		dst := filepath.Join(dstDir, dstName)
		if strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
			continue
		}
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := transferFile(src, dst, mode); err != nil {
			return err
		}
	}
	return nil
}
