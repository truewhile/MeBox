package reader

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 远端音轨的本地落盘缓存（媒体代理 /api/reader/media 使用）。
//
// 背景：听书源的音频直链签在腾讯音乐 CDN 上，跨境链路会间歇性返回 401
// 或直接 RST 连接。旧行为是每次请求（首播、拖动进度、播放器重试）都重新
// 回源，一次抖动就把错误交给播放器。这里按「书 + 媒体对象路径」把整段音频
// 落盘（忽略 ?vkey=... 这类会轮换的签名参数），命中后由 http.ServeContent
// 按 Range 下发：首播成功之后不再依赖 CDN，拖动进度也是本地读取。

const (
	audioMediaDirName      = "reader-audio"
	audioMediaTTL          = 7 * 24 * time.Hour
	audioMediaMaxBytes     = 2 << 30 // 2GiB：超出后按 mtime 淘汰最旧的
	audioMediaPrunePeriod  = 10 * time.Minute
	audioMediaPruneKeepPct = 90
)

var (
	audioMediaPruneMu  sync.Mutex
	audioMediaPrunedAt time.Time
)

// audioMediaDir 音频缓存根目录（与转码缓存同一套目录约定）。
func (s *ReaderService) audioMediaDir() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	base := strings.TrimSpace(s.cfg.Cache.CacheDir)
	if base == "" {
		if dataDir := strings.TrimSpace(s.cfg.App.DataDir); dataDir != "" {
			base = filepath.Join(dataDir, "cache")
		}
	}
	if base == "" {
		return ""
	}
	return filepath.Join(base, audioMediaDirName)
}

// audioMediaCacheKey 缓存键：书 + scheme://host/path。
//
// 必须忽略 query：同一段音频每次换票都会得到新的 vkey/uid/ct，只按完整 URL
// 取哈希的话缓存永远打不中。
func audioMediaCacheKey(bookID, mediaURL string) string {
	u, err := url.Parse(strings.TrimSpace(mediaURL))
	if err != nil || u.Host == "" {
		return ""
	}
	ident := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + u.EscapedPath()
	sum := sha256.Sum256([]byte(bookID + "|" + ident))
	return hex.EncodeToString(sum[:16])
}

// RemoteAudioCachePath 返回该音轨的缓存文件路径；无法缓存时返回空串。
func (s *ReaderService) RemoteAudioCachePath(bookID, mediaURL string) string {
	key := audioMediaCacheKey(bookID, mediaURL)
	if key == "" {
		return ""
	}
	dir := s.audioMediaDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, key[:2], key+".audio")
}

// IsAudioMediaURL 判断媒体代理这次要拉的是不是音轨。
//
// 后缀优先：音频书籍里也会出现封面/插图（.jpg）或 HLS 播放列表（.m3u8），
// 这些不能当成音轨整段落盘。只有后缀为空（签名直链常见形态）时才退回按
// 书籍类型判断。
func IsAudioMediaURL(rawURL string, bookType int) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return bookType == 1
	}
	ext := strings.ToLower(filepath.Ext(u.Path))
	if ext == "" {
		return bookType == 1
	}
	return audioExtensions[ext]
}

// HasCachedRemoteAudio 判断该音轨是否已有可用缓存（会顺带刷新 mtime）。
func (s *ReaderService) HasCachedRemoteAudio(bookID, mediaURL string) bool {
	f, _, ok := s.OpenCachedRemoteAudio(bookID, mediaURL)
	if ok {
		_ = f.Close()
	}
	return ok
}

// OpenCachedRemoteAudio 打开已缓存的音轨（命中返回打开的文件，调用方负责关闭）。
// 过期的缓存顺手删掉，未过期则刷新 mtime 供 LRU 淘汰。
func (s *ReaderService) OpenCachedRemoteAudio(bookID, mediaURL string) (*os.File, os.FileInfo, bool) {
	path := s.RemoteAudioCachePath(bookID, mediaURL)
	if path == "" {
		return nil, nil, false
	}
	f, err := os.Open(path) // #nosec G304 -- 路径由服务端哈希生成
	if err != nil {
		return nil, nil, false
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() || info.Size() == 0 {
		_ = f.Close()
		return nil, nil, false
	}
	if info.ModTime().Add(audioMediaTTL).Before(time.Now()) {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, nil, false
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return f, info, true
}

// StoreCachedRemoteAudio 把一段音源原子写入缓存，fill 负责把内容写进 writer。
// 返回最终文件路径；内容为空或写入失败时不留下半成品。
func (s *ReaderService) StoreCachedRemoteAudio(bookID, mediaURL string, fill func(io.Writer) error) (string, error) {
	path := s.RemoteAudioCachePath(bookID, mediaURL)
	if path == "" {
		return "", errors.New("音频缓存目录未配置")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("创建音频缓存目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".audio-*")
	if err != nil {
		return "", fmt.Errorf("创建音频缓存文件失败: %w", err)
	}
	tmpName := tmp.Name()
	fillErr := fill(tmp)
	closeErr := tmp.Close()
	if fillErr != nil || closeErr != nil {
		_ = os.Remove(tmpName)
		if fillErr != nil {
			return "", fillErr
		}
		return "", closeErr
	}
	info, statErr := os.Stat(tmpName)
	if statErr != nil || info.Size() == 0 {
		_ = os.Remove(tmpName)
		return "", errors.New("音源内容为空")
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("写入音频缓存失败: %w", err)
	}
	s.pruneAudioMediaCache()
	return path, nil
}

// pruneAudioMediaCache 低频清理：删过期文件，超容量时按 mtime 淘汰最旧的。
func (s *ReaderService) pruneAudioMediaCache() {
	dir := s.audioMediaDir()
	if dir == "" {
		return
	}
	audioMediaPruneMu.Lock()
	if time.Since(audioMediaPrunedAt) < audioMediaPrunePeriod {
		audioMediaPruneMu.Unlock()
		return
	}
	audioMediaPrunedAt = time.Now()
	audioMediaPruneMu.Unlock()

	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	var entries []entry
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		if info.ModTime().Add(audioMediaTTL).Before(time.Now()) {
			_ = os.Remove(path)
			return nil
		}
		entries = append(entries, entry{path: path, size: info.Size(), mod: info.ModTime()})
		total += info.Size()
		return nil
	})
	if total <= audioMediaMaxBytes {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod.Before(entries[j].mod) })
	target := int64(audioMediaMaxBytes) * audioMediaPruneKeepPct / 100
	for _, e := range entries {
		if total <= target {
			break
		}
		if err := os.Remove(e.path); err == nil {
			total -= e.size
		}
	}
}
