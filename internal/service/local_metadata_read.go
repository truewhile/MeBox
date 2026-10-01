package service

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ReadLocalMetadata reads sidecar NFO files for a media path. For TV/anime it
// merges show-level tvshow.nfo with episode-level sidecar metadata.
func ReadLocalMetadata(mediaPath, libraryRoot string, seriesLike bool) (*LocalMetadata, error) {
	if seriesLike {
		return readSeriesMetadata(mediaPath, libraryRoot)
	}
	doc, path, err := findMovieNFO(mediaPath, libraryRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return metadataFromArtwork(mediaPath, ""), nil
		}
		return nil, err
	}
	meta := metadataFromDoc(doc, filepath.Dir(path), false)
	mergeArtworkMetadata(meta, mediaPath, filepath.Dir(path))
	// A sidecar written while resolution tokens were misread as SxxExx must not
	// reintroduce the bogus season/episode on rescan.
	dropResolutionArtifactEpisodeIdentity(meta, mediaPath)
	return meta, nil
}

func findMovieNFO(mediaPath, libraryRoot string) (*nfoDocument, string, error) {
	mediaDir := filepath.Dir(mediaPath)
	adultCode := AdultCodeFromMediaPath(mediaPath)
	names := make([]string, 0, 8)
	for _, base := range mediaSidecarBaseVariants(mediaPath) {
		names = append(names, base+".nfo")
	}
	names = append(names, "movie.nfo", filepath.Base(mediaDir)+".nfo")
	if adultCode != "" {
		names = append([]string{adultCode + ".nfo", strings.ReplaceAll(adultCode, "-", "") + ".nfo"}, names...)
	}
	seen := map[string]struct{}{}
	for _, name := range names {
		if name == ".nfo" || name == "" {
			continue
		}
		path := filepath.Join(mediaDir, name)
		key := strings.ToLower(filepath.Clean(path))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if doc, _, err := decodeNFOFile(path); err == nil && doc != nil {
			return doc, path, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
	}
	if libraryRoot == "" || !samePath(mediaDir, filepath.Clean(libraryRoot)) {
		matches, _ := filepath.Glob(filepath.Join(mediaDir, "*.nfo"))
		if adultCode != "" {
			codeKey := strings.ToLower(strings.ReplaceAll(adultCode, "-", ""))
			for _, match := range matches {
				baseKey := strings.ToLower(strings.ReplaceAll(strings.TrimSuffix(filepath.Base(match), filepath.Ext(match)), "-", ""))
				if strings.Contains(baseKey, codeKey) || strings.Contains(codeKey, baseKey) {
					if doc, _, err := decodeNFOFile(match); err == nil && doc != nil {
						return doc, match, nil
					} else if err != nil && !errors.Is(err, os.ErrNotExist) {
						return nil, "", err
					}
				}
			}
		}
		if len(matches) == 1 {
			if doc, _, err := decodeNFOFile(matches[0]); err == nil && doc != nil {
				return doc, matches[0], nil
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, "", err
			}
		}
	}
	return nil, "", os.ErrNotExist
}

func readSeriesMetadata(mediaPath, libraryRoot string) (*LocalMetadata, error) {
	var meta *LocalMetadata
	showBaseDir := ""
	if showDoc, showPath, partial, err := findShowNFO(mediaPath, libraryRoot); err == nil && showDoc != nil {
		showBaseDir = filepath.Dir(showPath)
		meta = metadataFromDoc(showDoc, showBaseDir, true)
		// A truncated show NFO still yields a usable title/poster (decodePartialNFO
		// only surfaces docs with recoverable fields). Keep HasNFO so the recovered
		// title participates in series grouping; a fully partial episode match
		// below can still demote it.
		meta.HasNFO = !partial || meta.HasNFO
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		// A damaged show NFO must not discard the whole series: readLocalScanMetadata
		// treats this as a hard failure and leaves every episode without local
		// metadata. When the show level fails we still merge the episode NFO and
		// local artwork below instead of returning early.
		meta = nil
	}

	if episodeDoc, episodePath, episodePartial, err := readEpisodeNFO(nfoPath(mediaPath)); err == nil && episodeDoc != nil {
		episodeMeta := metadataFromDoc(episodeDoc, filepath.Dir(episodePath), true)
		if meta == nil {
			meta = &LocalMetadata{}
		}
		mergeEpisodeMetadata(meta, episodeMeta, episodeDoc)
		// Only a fully valid episode NFO can confirm the match; a truncated one
		// keeps whatever the show NFO supplied but must not force scrape_status.
		if episodePartial {
			meta.HasNFO = false
		}
	}
	if meta == nil {
		meta = metadataFromArtwork(mediaPath, showBaseDir)
	} else {
		mergeArtworkMetadata(meta, mediaPath, showBaseDir)
	}
	// A sidecar written while resolution tokens were misread as SxxExx must not
	// reintroduce the bogus season/episode on rescan.
	dropResolutionArtifactEpisodeIdentity(meta, mediaPath)
	return meta, nil
}

// readEpisodeNFO reads the sidecar NFO next to an episode file and reports both
// the recovered document and whether it came from a truncated/malformed file.
func readEpisodeNFO(path string) (*nfoDocument, string, bool, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- path is a discovered NFO sidecar under the configured library root.
	if err != nil {
		return nil, "", false, err
	}
	doc, partial, err := decodePartialNFO(body)
	if err != nil {
		return nil, "", false, err
	}
	return doc, path, partial, nil
}

func decodePartialNFO(body []byte) (*nfoDocument, bool, error) {
	var doc nfoDocument
	err := xml.Unmarshal(body, &doc)
	if err == nil {
		return &doc, false, nil
	}
	if !isLikelyTruncatedXMLError(err) {
		// A real parse error unrelated to truncation: don't trust partial fields.
		return nil, false, err
	}
	// Unmarshal still fills the elements it closed before hitting the cut; treat
	// those as partial show metadata instead of dropping everything.
	if doc.Title == "" && doc.OriginalTitle == "" && len(doc.Thumbs) == 0 &&
		doc.Premiered == "" && doc.Plot == "" {
		return nil, false, err
	}
	return &doc, true, nil
}

func isLikelyTruncatedXMLError(err error) bool {
	for _, msg := range []string{"unexpected EOF", "EOF"} {
		if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(msg)) {
			return true
		}
	}
	return false
}

func findShowNFO(mediaPath, libraryRoot string) (*nfoDocument, string, bool, error) {
	dir := filepath.Dir(mediaPath)
	root := filepath.Clean(libraryRoot)
	for {
		names := []string{"tvshow.nfo", "series.nfo"}
		base := filepath.Base(dir)
		if _, ok := seasonFromDir(base); ok {
			parentBase := filepath.Base(filepath.Dir(dir))
			names = append(names, parentBase+".nfo")
		}
		names = append(names, base+".nfo")
		for _, name := range names {
			path := filepath.Join(dir, name)
			doc, partial, err := decodeNFOFile(path)
			if doc != nil {
				return doc, path, partial, nil
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, "", false, err
			}
		}
		if samePath(dir, root) {
			return nil, "", false, os.ErrNotExist
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, "", false, os.ErrNotExist
		}
		dir = parent
	}
}

func decodeNFOFile(path string) (*nfoDocument, bool, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- path is a discovered NFO sidecar under the configured library root.
	if err != nil {
		return nil, false, err
	}
	doc, partial, err := decodePartialNFO(body)
	if err != nil {
		return nil, false, err
	}
	return doc, partial, nil
}
