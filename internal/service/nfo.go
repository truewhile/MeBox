// Package service — NFO writer (Kodi / Jellyfin compatibility).
//
// Kodi and Jellyfin index media using sidecar XML files alongside the
// source video. We export a minimal subset that those scrapers consume
// happily:
//
//	movie.mkv    -> movie.nfo            (<movie>...</movie>)
//	tvshow/      -> tvshow.nfo           (<tvshow>...</tvshow>)  [future]
//
// Movie and episode sidecars are generated today; a library-level tvshow.nfo
// exporter can be added later when the UI exposes a series-level export action.
package service

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// NFOService is the entry point used by the admin "导出 NFO" action.
type NFOService struct {
	log  *zap.Logger
	repo *repository.Container
}

// NewNFOService is the constructor.
func NewNFOService(log *zap.Logger, repo *repository.Container) *NFOService {
	return &NFOService{log: log, repo: repo}
}

// movieNFO is the on-disk schema. Tag names match Kodi's expectations.
type movieNFO struct {
	XMLName  xml.Name `xml:"movie"`
	Title    string   `xml:"title"`
	Original string   `xml:"originaltitle,omitempty"`
	Year     int      `xml:"year,omitempty"`
	Plot     string   `xml:"plot,omitempty"`
	Rating   float32  `xml:"rating,omitempty"`
	Poster   string   `xml:"thumb,omitempty"`
	Fanart   string   `xml:"fanart,omitempty"`
	TMDb     int      `xml:"tmdbid,omitempty"`
	// UniqueIDs 让 sidecar 能把 MeBox 自己用的外部 ID 原样带回来。缺了它，
	// 「在线刮削 → 写 NFO → 重扫读 NFO」会丢掉 douban/thetvdb（成人条目借用
	// 这两个字段存番号/provider），同一部片的两条来源就会算出不同的版本键。
	UniqueIDs []nfoUniqueID `xml:"uniqueid,omitempty"`
	Genre     []string      `xml:"genre,omitempty"`
	Country   []string      `xml:"country,omitempty"`
	Language  []string      `xml:"language,omitempty"`
}

type episodeNFO struct {
	XMLName   xml.Name `xml:"episodedetails"`
	Title     string   `xml:"title"`
	ShowTitle string   `xml:"showtitle,omitempty"`
	Season    int      `xml:"season"`
	Episode   int      `xml:"episode"`
	Year      int      `xml:"year,omitempty"`
	Plot      string   `xml:"plot,omitempty"`
	Rating    float32  `xml:"rating,omitempty"`
	Poster    string   `xml:"thumb,omitempty"`
	Fanart    string   `xml:"fanart,omitempty"`
	TMDb      int      `xml:"tmdbid,omitempty"`
	Genre     []string `xml:"genre,omitempty"`
	Country   []string `xml:"country,omitempty"`
	Language  []string `xml:"language,omitempty"`
}

// ExportOne writes a movie.nfo file next to the media file. Existing files
// are overwritten so a re-scrape always reflects the latest metadata. When the
// folder already holds a sidecar the reader would pick (movie.nfo /
// <dirname>.nfo), that file is updated in place instead of adding a second,
// divergent NFO.
func (s *NFOService) ExportOne(ctx context.Context, mediaID string) (string, error) {
	m, err := s.repo.Media.FindByID(ctx, mediaID)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", errors.New("media not found")
	}
	if m.Path == "" {
		return "", errors.New("media has empty path")
	}

	dst, err := WriteMediaNFO(m)
	if err != nil {
		return "", err
	}
	s.log.Info("nfo exported", zap.String("media_id", m.ID), zap.String("path", dst))
	return dst, nil
}

// ExportLibrary loops through every matched movie in a library and writes
// an .nfo for each. Returns (written, error).
func (s *NFOService) ExportLibrary(ctx context.Context, libraryID string) (int, error) {
	type row struct{ ID string }
	var ids []row
	q := s.repo.DB.Table("media").Select("id").Where("scrape_status = ?", "matched")
	if libraryID != "" {
		q = q.Where("library_id = ?", libraryID)
	}
	if err := q.Scan(&ids).Error; err != nil {
		return 0, err
	}
	written := 0
	for _, r := range ids {
		if _, err := s.ExportOne(ctx, r.ID); err == nil {
			written++
		}
	}
	return written, nil
}

func nfoPath(media string) string {
	dir := filepath.Dir(media)
	base := mediaSidecarBase(media)
	if base == "" {
		base = strings.TrimSuffix(filepath.Base(media), filepath.Ext(media))
	}
	return filepath.Join(dir, fmt.Sprintf("%s.nfo", base))
}

func WriteMediaNFO(m *model.Media) (string, error) {
	if m == nil {
		return "", errors.New("media not found")
	}
	if m.Path == "" {
		return "", errors.New("media has empty path")
	}

	var doc any
	if m.SeasonNum > 0 || m.EpisodeNum > 0 {
		title := strings.TrimSpace(m.EpisodeTitle)
		if title == "" && m.EpisodeNum > 0 {
			title = fmt.Sprintf("第 %d 集", m.EpisodeNum)
		}
		if title == "" {
			title = strings.TrimSpace(m.Title)
		}
		doc = episodeNFO{
			Title:     title,
			ShowTitle: m.Title,
			Season:    m.SeasonNum,
			Episode:   m.EpisodeNum,
			Year:      m.Year,
			Plot:      m.Overview,
			Rating:    m.Rating,
			Poster:    m.PosterURL,
			Fanart:    m.BackdropURL,
			TMDb:      m.TMDbID,
			Genre:     splitNFOList(m.Genres),
			Country:   splitNFOList(m.Countries),
			Language:  splitNFOList(m.Languages),
		}
	} else {
		title := m.Title
		original := m.OriginalName
		adultCode := normalizeAdultCode(firstText(m.OriginalName, AdultCodeFromMediaPath(m.Path), m.Title))
		if adultCode != "" || m.NSFW {
			if adultCode != "" {
				title = FormatAdultTitle(adultCode, m.Title)
				if original == "" || strings.EqualFold(original, m.Title) {
					original = adultCode
				}
			}
		}
		doc = movieNFO{
			Title:     title,
			Original:  original,
			Year:      m.Year,
			Plot:      m.Overview,
			Rating:    m.Rating,
			Poster:    m.PosterURL,
			Fanart:    m.BackdropURL,
			TMDb:      m.TMDbID,
			UniqueIDs: nfoExternalUniqueIDs(m),
			Genre:     splitNFOList(m.Genres),
			Country:   splitNFOList(m.Countries),
			Language:  splitNFOList(m.Languages),
		}
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	dst := nfoPath(resolveMappedDestinationPath(m.Path))
	if m.SeasonNum <= 0 && m.EpisodeNum <= 0 {
		dst = nfoExportTarget(dst)
	}
	if err := os.WriteFile(dst, []byte(xml.Header+string(out)+"\n"), 0o644); err != nil { // #nosec G306 -- NFO sidecars must remain readable by media players.
		return "", err
	}
	return dst, nil
}

// nfoExportTarget picks the file an export should write for a movie.
//
// Besides the canonical "<base>.nfo", the reader (findMovieNFO) also accepts
// movie.nfo and "<dirname>.nfo". Writing the canonical name into a folder that
// already carries one of those left two divergent NFOs for the same movie, and
// because the reader prefers the canonical one the pre-existing file quietly went
// stale. Reuse whichever file the reader would already have picked, in the same
// precedence order, so an export updates the existing sidecar in place.
//
// Episodes are deliberately excluded by the caller: for an episode the
// alternatives are series-level files, and writing episode metadata there would
// corrupt the series identity.
func nfoExportTarget(canonical string) string {
	dir := filepath.Dir(canonical)
	candidates := []string{
		filepath.Base(canonical),
		"movie.nfo",
		filepath.Base(dir) + ".nfo",
	}
	for _, name := range candidates {
		if name == "" || name == "." {
			continue
		}
		candidate := filepath.Join(dir, name)
		if candidate == canonical {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return canonical
}

func splitNFOList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// nfoExternalUniqueIDs 把 MeBox 使用的外部 ID 写进 sidecar，供自己重扫时读回
// （metadataFromDoc 会按 type 还原成 bangumi/douban/thetvdb 字段）。
// TMDb 走 <tmdbid>，单集 NFO 不写唯一 ID：单集 ID 属于单集，写进整剧身份会把
// 同一部剧拆成多张卡（见 mergeEpisodeMetadata 的约束说明）。
func nfoExternalUniqueIDs(m *model.Media) []nfoUniqueID {
	if m == nil {
		return nil
	}
	out := make([]nfoUniqueID, 0, 3)
	if m.BangumiID > 0 {
		out = append(out, nfoUniqueID{Type: "bangumi", Value: strconv.Itoa(m.BangumiID)})
	}
	if id := strings.TrimSpace(m.DoubanID); id != "" {
		out = append(out, nfoUniqueID{Type: "douban", Value: id})
	}
	if id := strings.TrimSpace(m.TheTVDBID); id != "" {
		out = append(out, nfoUniqueID{Type: "thetvdb", Value: id})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
