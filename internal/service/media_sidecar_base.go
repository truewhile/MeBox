package service

import (
	"path/filepath"
	"strings"
)

// mediaSidecarBase 返回媒体文件用于配对 NFO/海报/字幕等元数据的共享基名。
// 同片多版本（含 keep_ext 的 name.mkv.strm / name.mp4.strm）应落到同一词干，
// 从而忽略中间的视频扩展，按「同一影片」匹配边车文件。
//
//	movie.mkv            → movie
//	movie.strm           → movie
//	movie.mkv.strm       → movie
//	Show.S01E01.mp4.strm → Show.S01E01
func mediaSidecarBase(mediaPath string) string {
	clean := strings.ReplaceAll(strings.TrimSpace(mediaPath), "\\", "/")
	if clean == "" {
		return ""
	}
	return mediaFileStem(filepath.Base(clean))
}

// mediaFileStem 去掉最终扩展名；若为 .strm 且前一层是视频扩展，再剥一层。
func mediaFileStem(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." {
		return ""
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if strings.EqualFold(ext, ".strm") {
		if second := strings.ToLower(filepath.Ext(stem)); second != "" {
			if _, ok := videoExtensions[second]; ok && second != ".strm" {
				stem = strings.TrimSuffix(stem, filepath.Ext(stem))
			}
		}
	}
	return strings.TrimSpace(stem)
}

// mediaSidecarBaseVariants 返回匹配用的基名候选：共享词干优先，其次保留单层剥扩展
// （兼容历史上写成 name.mkv.nfo / name.mkv-poster.jpg 的边车）。
func mediaSidecarBaseVariants(mediaPath string) []string {
	stem := mediaSidecarBase(mediaPath)
	single := strings.TrimSuffix(filepath.Base(strings.TrimSpace(mediaPath)), filepath.Ext(mediaPath))
	single = strings.TrimSpace(single)
	out := make([]string, 0, 2)
	seen := map[string]struct{}{}
	for _, item := range []string{stem, single} {
		if item == "" || item == "." {
			continue
		}
		key := strings.ToLower(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}
