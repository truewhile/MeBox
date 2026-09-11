package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
)

// writeMediaArtworkFilesAfterScrape downloads the scraped poster/backdrop into
// the media folder as Jellyfin/Emby sidecars (<base>-poster.jpg /
// <base>-backdrop.jpg), scoped by the media file's base name so multiple movies
// sharing one directory never overwrite each other's artwork.
//
// Cloud media (cloud:// paths) are skipped entirely — the media folder is not
// writable for cloud mounts, and sidecars would be meaningless. Mirrors the
// local/cloud gating used by writeMediaNFOAfterScrape.
func (s *ScraperService) writeMediaArtworkFilesAfterScrape(ctx context.Context, m *model.Media, lib *model.Library) {
	if s == nil || s.images == nil || m == nil {
		return
	}
	cloudMedia := isCloudMediaPath(m.Path) || (lib != nil && isCloudMediaPath(lib.Path))
	if cloudMedia {
		return
	}
	// Reload the media from the DB so we use the freshly-saved poster/backdrop
	// URLs rather than the stale in-memory values (the caller updates the row
	// before invoking this).
	refreshed, err := s.repo.Media.FindByID(ctx, m.ID)
	if err != nil || refreshed == nil {
		return
	}
	dir := filepath.Dir(resolveMappedDestinationPath(refreshed.Path))
	if dir == "" || dir == "." {
		return
	}
	// Scope sidecar names by the shared media stem (e.g. A.mkv.strm / A.mp4.strm -> A-poster.jpg)
	// so multi-version files in one folder share artwork and never diverge by container.
	base := mediaSidecarBase(refreshed.Path)
	if base == "" || base == "." {
		return
	}
	isAdult := shouldCropAdultPoster(refreshed, lib)
	artworkUpdates := map[string]any{}
	shouldPersistLocalArtworkURL := func(raw string) bool {
		return isAdult || !isHTTPish(raw)
	}
	if refreshed.PosterURL != "" {
		if dst := s.downloadArtworkToPathWithOptions(ctx, dir, base+"-poster", refreshed.PosterURL, isAdult); dst != "" {
			if shouldPersistLocalArtworkURL(refreshed.PosterURL) {
				artworkUpdates["poster_url"] = filepath.Join(filepath.Dir(refreshed.Path), filepath.Base(dst))
			}
		}
	}
	if refreshed.BackdropURL != "" {
		if dst := s.downloadArtworkToPathWithOptions(ctx, dir, base+"-backdrop", refreshed.BackdropURL, false); dst != "" {
			if shouldPersistLocalArtworkURL(refreshed.BackdropURL) {
				artworkUpdates["backdrop_url"] = filepath.Join(filepath.Dir(refreshed.Path), filepath.Base(dst))
			}
		}
	} else if isAdult && refreshed.PosterURL != "" {
		// 番号海报原图为完整封套横图，在无独立背景图时直接作为背景图写出
		if dst := s.downloadArtworkToPathWithOptions(ctx, dir, base+"-backdrop", refreshed.PosterURL, false); dst != "" {
			artworkUpdates["backdrop_url"] = filepath.Join(filepath.Dir(refreshed.Path), filepath.Base(dst))
		}
	}
	if len(artworkUpdates) > 0 {
		if err := s.repo.DB.WithContext(ctx).Model(&model.Media{}).
			Where("id = ?", refreshed.ID).Updates(artworkUpdates).Error; err != nil {
			s.log.Warn("save local scraped artwork paths failed", zap.String("media_id", refreshed.ID), zap.Error(err))
		}
	}
}

func (s *ScraperService) downloadArtworkToPath(ctx context.Context, dir, name, raw string) string {
	return s.downloadArtworkToPathWithOptions(ctx, dir, name, raw, false)
}

// shouldCropAdultPoster keeps adult-cover handling independent of which
// metadata provider won. A code-numbered title may match TMDb first, so the
// provider's NSFW flag or artwork host alone is not sufficient.
func shouldCropAdultPoster(media *model.Media, lib *model.Library) bool {
	if media == nil {
		return false
	}
	mediaType := ""
	if lib != nil {
		mediaType = lib.Type
	}
	return IsAdultMediaPathOrMetadata(media.Path, mediaType, media.NSFW) ||
		IsAdultArtworkURL(media.PosterURL)
}

// downloadArtworkToPathWithOptions fetches an artwork URL via the image proxy cache and
// writes it under dir/<name>.<ext>. For adult posters, it crops the right half of the cover.
func (s *ScraperService) downloadArtworkToPathWithOptions(ctx context.Context, dir, name, raw string, cropAdultPoster bool) string {
	var (
		data  []byte
		ctype string
		err   error
	)
	switch {
	case isHTTPish(raw):
		data, ctype, err = s.images.Fetch(ctx, raw)
	case isLocalImagePath(raw):
		data, err = os.ReadFile(sanitizeLocalPath(resolveMappedDestinationPath(raw)))
		if err == nil {
			ctype = detectContentType(data)
		}
	default:
		return ""
	}
	if err != nil || len(data) == 0 {
		s.log.Warn("scrape artwork download failed",
			zap.String("name", name),
			zap.String("url", raw),
			zap.Error(err))
		return ""
	}
	if !isImageContentType(ctype) || isTransparentPlaceholderData(data) {
		return ""
	}
	// Upstreams sometimes report non-standard values such as image/jpg.
	// Use the decoded bytes as the source of truth so sidecars receive a
	// standard extension instead of the legacy .img fallback.
	if detected := detectContentType(data); isImageContentType(detected) {
		ctype = detected
	}
	if cropAdultPoster {
		if cropped, croppedType, err := CropAdultCoverPoster(data); err == nil && len(cropped) > 0 {
			data = cropped
			ctype = croppedType
		}
	}
	return s.writeArtworkDataToPath(dir, name, ctype, data)
}

// writeArtworkDataToPath writes in-memory artwork bytes to dir/<name>.<ext>
// using a temp file + rename so readers never observe a partial file. Returns
// the destination path, or "" if the write failed.
func (s *ScraperService) writeArtworkDataToPath(dir, name, ctype string, data []byte) string {
	if len(data) == 0 {
		return ""
	}
	dir = sanitizeLocalPath(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.log.Warn("scrape artwork mkdir failed", zap.String("dir", dir), zap.Error(err))
		return ""
	}
	dst := filepath.Join(dir, name+imageExtForContentType(ctype))
	tmp, err := os.CreateTemp(dir, "img-*.tmp")
	if err != nil {
		s.log.Warn("scrape artwork temp create failed", zap.String("dir", dir), zap.Error(err))
		return ""
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		s.log.Warn("scrape artwork write failed", zap.String("dst", dst), zap.Error(err))
		return ""
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		s.log.Warn("scrape artwork close failed", zap.String("dst", dst), zap.Error(err))
		return ""
	}

	// On Windows os.Rename does not replace dst. Serialize remove+rename so two
	// concurrent scrapes cannot leave the previous uncropped DVD cover behind.
	s.artworkWriteMu.Lock()
	err = os.Remove(dst)
	if err == nil || os.IsNotExist(err) {
		err = os.Rename(tmp.Name(), dst)
	}
	s.artworkWriteMu.Unlock()
	if err != nil {
		_ = os.Remove(tmp.Name())
		s.log.Warn("scrape artwork replace failed", zap.String("dst", dst), zap.Error(err))
		return ""
	}
	s.log.Debug("scrape artwork written", zap.String("dst", dst))
	return dst
}

// imageExtForContentType maps a detected image MIME type to a file extension.
// Unknown image types fall back to a generic ".img" so we never write an empty
// extension that could confuse media players.
func imageExtForContentType(ctype string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(ctype, ";")[0])) {
	case "image/jpeg", "image/jpg", "image/pjpeg":
		return ".jpg"
	case "image/png", "image/x-png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/avif":
		return ".avif"
	default:
		return ".img"
	}
}
