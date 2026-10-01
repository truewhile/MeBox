package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OrganizeSourceCandidate is a selectable organize source directory surfaced to
// the UI so operators can organize an arbitrary directory (such as the download
// directory) and not only registered libraries.
type OrganizeSourceCandidate struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Kind  string `json:"kind"` // "download" | "media"
}

// OrganizeSourceCandidates returns the configured directories that are valid
// organize sources (download dir + media dir). It uses the container-visible
// paths; in NAS direct-read mode those equal the host paths the operator sees.
func (o *OrganizerService) OrganizeSourceCandidates(ctx context.Context) []OrganizeSourceCandidate {
	out := []OrganizeSourceCandidate{}
	seen := map[string]struct{}{}
	add := func(label, path, kind string) {
		path = strings.TrimSpace(path)
		if path == "" || path == "." || strings.HasPrefix(path, ".") {
			return
		}
		clean := filepath.Clean(path)
		if !isAccessibleDir(clean) {
			return
		}
		if _, ok := seen[clean]; ok {
			return
		}
		seen[clean] = struct{}{}
		out = append(out, OrganizeSourceCandidate{Label: label, Path: clean, Kind: kind})
	}
	add("默认整理源", o.settingValue(ctx, "organize.source_dir"), "source")
	add("下载器保存目录", o.downloaderSavepath(ctx), "download")
	add("下载目录", envOrDefault("MEBOX_DOWNLOAD_CONTAINER_DIR", "/downloads"), "download")
	add("媒体目录", envOrDefault("MEBOX_MEDIA_CONTAINER_DIR", "/media"), "media")
	return out
}

func (o *OrganizerService) settingValue(ctx context.Context, key string) string {
	if o.repo == nil || o.repo.Setting == nil {
		return ""
	}
	if v, err := o.repo.Setting.Get(ctx, key); err == nil {
		return strings.TrimSpace(v)
	}
	return ""
}

// downloaderSavepath 返回下载器保存目录，优先新键 downloader.savepath，
// 兼容历史键名 qbittorrent.savepath（旧版本部署已写入的配置不丢失）。
func (o *OrganizerService) downloaderSavepath(ctx context.Context) string {
	if v := o.settingValue(ctx, "downloader.savepath"); v != "" {
		return v
	}
	return o.settingValue(ctx, "qbittorrent.savepath")
}

// defaultSourceRoot resolves the source root for a directory organize:
// explicit override → organize.source_dir setting → downloader save path →
// download container dir.
func (o *OrganizerService) defaultSourceRoot(ctx context.Context, override string) string {
	if r := strings.TrimSpace(override); r != "" {
		return r
	}
	if v := o.settingValue(ctx, "organize.source_dir"); v != "" {
		return v
	}
	if v := o.downloaderSavepath(ctx); v != "" {
		return v
	}
	return envOrDefault("MEBOX_DOWNLOAD_CONTAINER_DIR", "/downloads")
}

// defaultDestRoot resolves the destination root for a directory organize:
// explicit override → organize.target_dir setting → media container dir.
func (o *OrganizerService) defaultDestRoot(ctx context.Context, override string) string {
	if r := strings.TrimSpace(override); r != "" {
		return r
	}
	if o.repo != nil && o.repo.Setting != nil {
		if v, err := o.repo.Setting.Get(ctx, "organize.target_dir"); err == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return envOrDefault("MEBOX_MEDIA_CONTAINER_DIR", "/media")
}

func ensureOrganizeDestinationWritable(dest string) error {
	dest = strings.TrimSpace(dest)
	if dest == "" || dest == "." {
		return errors.New("destination path required")
	}
	if _, ok := ParseCloudLibraryMount(dest); ok {
		return errors.New("organize destination must be a local writable media directory; enable cloud transfer in external storage when writing to cloud")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil { // #nosec G301 -- organized media directories must remain readable by NAS/player users.
		return fmt.Errorf("destination path is not a writable directory: %s: %w", dest, err)
	}
	probe, err := os.CreateTemp(dest, ".mebox-write-test-*") // #nosec G304 -- dest is operator-configured organize root.
	if err != nil {
		return fmt.Errorf("destination path is not writable: %s: %w", dest, err)
	}
	name := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("destination path write probe failed: %s: %w", dest, closeErr)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("destination path cleanup probe failed: %s: %w", dest, err)
	}
	return nil
}
