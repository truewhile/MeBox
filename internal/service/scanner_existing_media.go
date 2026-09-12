package service

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/truewhile/MeBox/internal/model"
)

// existingLocalMediaSnapshot 保留整库快照入口，主要供兼容性调用和测试使用。
// 实际扫库路径使用 existingLocalMediaSnapshotForRoot，避免多根目录库重复持有
// 与当前 root 无关的媒体记录。
func (s *ScannerService) existingLocalMediaSnapshot(ctx context.Context, libraryID string) (map[string]existingLocalMedia, error) {
	return s.existingLocalMediaSnapshotForRoot(ctx, libraryID, "", "")
}

// existingLocalMediaSnapshotForRoot 以流式方式读取当前 root 的已有媒体索引，
// 不再先构造 []model.Media 再复制成 map。map 中的字符串只在最终索引中保留一份，
// 可以显著降低大库扫描时的峰值内存。
func (s *ScannerService) existingLocalMediaSnapshotForRoot(ctx context.Context, libraryID, rootID, rootPath string) (map[string]existingLocalMedia, error) {
	if s == nil || s.repo == nil || s.repo.DB == nil {
		return map[string]existingLocalMedia{}, nil
	}
	libraryID = strings.TrimSpace(libraryID)
	rootID = strings.TrimSpace(rootID)
	rootPath = strings.TrimSpace(rootPath)
	query := s.repo.DB.WithContext(ctx).
		Model(&model.Media{}).
		Where("library_id = ? AND path NOT LIKE ?", libraryID, "cloud://%")
	if rootID != "" {
		// 兼容尚未回填 library_root_id 的旧数据；没有 root id 的行再由路径归属过滤。
		query = query.Where("(library_root_id = ? OR library_root_id = '' OR library_root_id IS NULL)", rootID)
	}
	rows, err := query.
		Select("path", "library_root_id", "relative_path", "title", "original_name", "episode_title", "size_bytes", "duration_sec", "width", "height", "video_codec", "audio_codec", "container", "strm_url", "file_id", "poster_url", "backdrop_url", "overview", "year", "release_date", "rating", "tm_db_id", "bangumi_id", "douban_id", "thetvdb_id", "season_num", "episode_num", "genres", "countries", "languages", "nsfw", "scrape_status").
		Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	snapshot := make(map[string]existingLocalMedia)
	for rows.Next() {
		var (
			path string
			row  existingLocalMedia
		)
		if err := rows.Scan(
			&path,
			&row.LibraryRootID,
			&row.RelativePath,
			&row.Title,
			&row.OriginalName,
			&row.EpisodeTitle,
			&row.SizeBytes,
			&row.DurationSec,
			&row.Width,
			&row.Height,
			&row.VideoCodec,
			&row.AudioCodec,
			&row.Container,
			&row.STRMURL,
			&row.FileID,
			&row.PosterURL,
			&row.BackdropURL,
			&row.Overview,
			&row.Year,
			&row.ReleaseDate,
			&row.Rating,
			&row.TMDbID,
			&row.BangumiID,
			&row.DoubanID,
			&row.TheTVDBID,
			&row.SeasonNum,
			&row.EpisodeNum,
			&row.Genres,
			&row.Countries,
			&row.Languages,
			&row.NSFW,
			&row.ScrapeStatus,
		); err != nil {
			return nil, err
		}
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if rootPath != "" {
			if rootID == "" || strings.TrimSpace(row.LibraryRootID) == "" {
				if !pathBelongsToRoot(path, rootPath) {
					continue
				}
			}
		}
		snapshot[filepath.Clean(path)] = row
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return snapshot, nil
}
