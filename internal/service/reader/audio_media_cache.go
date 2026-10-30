package reader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 远端音轨的本地落盘缓存（媒体代理 /api/reader/media 使用）。
//
// 背景：听书源的音频直链签在腾讯音乐 CDN 上，跨境链路会间歇性返回 401
// 或直接 RST 连接。旧行为是每次请求（首播、拖动进度、播放器重试）都重新
// 回源，一次抖动就把错误交给播放器。这里按「书 + 媒体对象」把整段音频
// 落盘（丢掉 ?vkey=... 这类会轮换的签名参数），命中后由 http.ServeContent
// 按 Range 下发：首播成功之后不再依赖 CDN，拖动进度也是本地读取。

const (
	audioMediaDirName = "reader-audio"
	audioMediaTTL     = 7 * 24 * time.Hour
	// audioMediaMaxBytes 音频缓存的总容量上限。超限时按 mtime 淘汰最旧的。
	//
	// 这个池不参与 cache_cleanup 的全局总量统计（hls/images/library-covers
	// 三池），而是在每次下载成功后自行淘汰，见 pruneAudioMediaCache。
	audioMediaMaxBytes     = 1 << 30 // 1GiB
	audioMediaPrunePeriod  = 10 * time.Minute
	audioMediaPruneKeepPct = 90

	// audioPartialSuffix 未下完的音轨分片后缀。
	//
	// 分片跨请求保留：首播失败后用户再点一次、或播放器重连，都能用 Range 接着下，
	// 不必从第 0 字节重来。分片名带 .part 后缀，不会被 OpenCachedRemoteAudio 当成
	// 完整音轨下发。
	audioPartialSuffix = ".part"
	// audioMediaObjectMaxBytes 单个音频对象的落盘上限。有声书一章约 3–5MB，
	// 给到 64MB 足够容纳长章节，也能挡住误当成音频的超大对象。
	audioMediaObjectMaxBytes = 64 << 20
	// audioDownloadRetryDelay 断点续传重试的退避基数，实际等待为 (attempt-1) 倍。
	audioDownloadRetryDelay = 500 * time.Millisecond
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

// audioMediaVolatileParams 是换票时会变、但与「这是哪段音频」无关的 query 参数。
// 缓存键必须忽略它们，否则每次换票都打不中缓存。
//
// 不在这张表里的参数一律保留：有些书源的直链形如
// https://host/audio?id=12345，路径固定、靠 query 区分章节，把 query 整个丢掉
// 会让第二集命中第一集的缓存，直接播错集。
var audioMediaVolatileParams = map[string]bool{
	"vkey": true, "uid": true, "ct": true, "t": true, "ts": true,
	"token": true, "sign": true, "signature": true, "sig": true,
	"expires": true, "expire": true, "expiry": true, "auth": true,
	"nonce": true, "rand": true, "rnd": true, "r": true, "guid": true,
	"ftime": true,
}

// audioMediaCacheKey 缓存键：书 + 媒体对象身份。
//
// 身份 = scheme://host/path + 非签名类 query。签名/时效参数（vkey、token、
// 时间戳等）被剔除，保证换票后仍命中同一条缓存；其余参数保留，避免
// 「路径相同、只靠 query 区分」的书源串集。
func audioMediaCacheKey(bookID, mediaURL string) string {
	u, err := url.Parse(strings.TrimSpace(mediaURL))
	if err != nil || u.Host == "" {
		return ""
	}
	ident := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + u.EscapedPath()
	if u.RawQuery != "" {
		if q, parseErr := url.ParseQuery(u.RawQuery); parseErr == nil {
			var kept []string
			for k, vs := range q {
				if audioMediaVolatileParams[strings.ToLower(k)] {
					continue
				}
				for _, v := range vs {
					kept = append(kept, k+"="+v)
				}
			}
			if len(kept) > 0 {
				sort.Strings(kept)
				ident += "?" + strings.Join(kept, "&")
			}
		}
	}
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

// HasPartialRemoteAudio 是否有可续传的未完成分片（.part）。
//
// 调用方据此决定首播策略：有分片说明上一次已经下到一半（用户点过重试、
// 或播放器自己重连过），此时「下载完再下发」能透明续上剩余字节；
// 没有分片才是真正的冷启动，边下边发换更低的 TTFB。
func (s *ReaderService) HasPartialRemoteAudio(bookID, mediaURL string) bool {
	path := s.RemoteAudioCachePath(bookID, mediaURL)
	if path == "" {
		return false
	}
	info, err := os.Stat(path + audioPartialSuffix)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return false
	}
	return info.Size() <= audioMediaObjectMaxBytes
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

// errAudioObjectTooLarge 上游对象超过单章缓存上限（不是可重试的错误）。
var errAudioObjectTooLarge = errors.New("音源超过缓存上限")

// NoRetryError 标记「换着花样再试也不会有进展」的失败（例如上游正成批拒绝
// 签名票、书源根本给不出新票）。带断点续传的下载器收到它会立刻停止，并把
// Err 原样交给调用方——继续重试只会让播放器多等十几秒，还会让对方更坚定地
// 拒绝我们。
type NoRetryError struct{ Err error }

func (e *NoRetryError) Error() string { return e.Err.Error() }
func (e *NoRetryError) Unwrap() error { return e.Err }

// AudioRangeFetcher 取一次上游音轨响应。
//
// rangeHeader 为空表示整段请求；否则形如 "bytes=N-"，用于从断点续传。
// head 是调用方已经嗅探读过、必须补写回文件的前缀（只有整段请求才有）。
// 调用方负责在内部完成换票；这里只看状态码和字节。
type AudioRangeFetcher func(rangeHeader string) (resp *http.Response, head []byte, err error)

// AudioStreamStart 首播直通开始的回调：调用方在这里写响应头与状态码。
// total 是上游声明的总长度（分块传输时为 -1），contentType 是上游给的类型。
type AudioStreamStart func(total int64, contentType string)

// errAudioStreamCannotRestart 直通模式下遇到必须「从第 0 字节重来」的情况。
//
// 直通时客户端已经收到了前缀字节，不可能让它把已收到的丢掉重听，所以这类
// 分支只能中止这次响应（让客户端判成不完整），并把已下载的分片留给下次续传。
var errAudioStreamCannotRestart = errors.New("直通中无法从零重新下载")

// audioSinkError 标记「写客户端失败」（通常是播放器关掉了连接）。
// 与写盘失败区分开：写盘失败说明本地有问题，写客户端失败只需停止下载。
type audioSinkError struct{ err error }

func (e *audioSinkError) Error() string { return "写入客户端失败: " + e.err.Error() }
func (e *audioSinkError) Unwrap() error { return e.err }

// DownloadRemoteAudio 带断点续传地把整段音轨写进缓存，返回最终文件路径。
//
// 为什么需要续传：腾讯 CDN 的跨境边缘常在传输中途 RST，旧实现每次重试都从
// 第 0 字节重下，一章 3MB 白下几次就把首播拖到几十秒。这里把已写入的字节留在
// <cache>.part 里，下一次用 Range: bytes=N- 续上；分片跨请求保留，用户点重试
// 或播放器重连都能接着下。
//
// 上游忽略 Range 直接回 200、或分片区间对不上时，丢弃分片从头重来。
// 下载过程中的失败不会留下「看起来完整」的缓存文件：只有字节数与上游声明的
// 总长一致才会原子改名到正式路径。
func (s *ReaderService) DownloadRemoteAudio(
	ctx context.Context,
	bookID, mediaURL string,
	attempts int,
	fetch AudioRangeFetcher,
) (string, error) {
	return s.downloadRemoteAudio(ctx, bookID, mediaURL, attempts, fetch, nil, nil)
}

// DownloadRemoteAudioStreaming 首播直通：一边落盘一边把字节写给 sink。
//
// 与 DownloadRemoteAudio 的区别只有一处——不再等整段下完才返回，第一个字节
// 到达就通过 onStart 写好响应头并开始外发，慢链路上的首播不必先等 3–5MB。
//
// 代价是失败语义变严：客户端已经收到前缀字节，任何「必须从 0 重来」的情况
// （上游忽略 Range 回 200、分片区间对不上、416）都只能中止这次响应，
// 不能像普通模式那样静默重下。已写入的分片仍然保留，下次可续传。
func (s *ReaderService) DownloadRemoteAudioStreaming(
	ctx context.Context,
	bookID, mediaURL string,
	attempts int,
	fetch AudioRangeFetcher,
	onStart AudioStreamStart,
	sink io.Writer,
) (string, error) {
	if sink == nil {
		return "", errors.New("直通下载未提供输出目标")
	}
	return s.downloadRemoteAudio(ctx, bookID, mediaURL, attempts, fetch, onStart, sink)
}

// downloadRemoteAudio 是两种下载模式的共同实现。sink 非空即首播直通模式。
func (s *ReaderService) downloadRemoteAudio(
	ctx context.Context,
	bookID, mediaURL string,
	attempts int,
	fetch AudioRangeFetcher,
	onStart AudioStreamStart,
	sink io.Writer,
) (string, error) {
	path := s.RemoteAudioCachePath(bookID, mediaURL)
	if path == "" {
		return "", errors.New("音频缓存目录未配置")
	}
	if fetch == nil {
		return "", errors.New("音频下载器未配置")
	}
	if attempts <= 0 {
		attempts = 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("创建音频缓存目录失败: %w", err)
	}
	partPath := path + audioPartialSuffix
	var offset int64
	if info, statErr := os.Stat(partPath); statErr == nil && info.Mode().IsRegular() {
		offset = info.Size()
		// 分片太旧或超过上限：续传没有意义，直接丢掉重下。
		if offset > audioMediaObjectMaxBytes || info.ModTime().Add(audioMediaTTL).Before(time.Now()) {
			_ = os.Remove(partPath)
			offset = 0
		}
	}
	file, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return "", fmt.Errorf("创建音频缓存文件失败: %w", err)
	}
	// onStart 只应触发一次：直通模式下响应头只能写一次。
	started := false

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				_ = file.Close()
				return "", ctx.Err()
			case <-time.After(audioDownloadRetryDelay * time.Duration(attempt-1)):
			}
		}

		rangeHeader := ""
		if offset > 0 {
			rangeHeader = fmt.Sprintf("bytes=%d-", offset)
		}
		resp, head, fetchErr := fetch(rangeHeader)
		if fetchErr != nil {
			var noRetry *NoRetryError
			if errors.As(fetchErr, &noRetry) {
				_ = file.Close()
				// 已经写下的分片留着：上游恢复后用户再点一次能接着续。
				if offset == 0 {
					_ = os.Remove(partPath)
				}
				return "", noRetry.Err
			}
			lastErr = fetchErr
			continue
		}
		if resp == nil {
			lastErr = errors.New("音源响应为空")
			continue
		}

		expected := int64(-1)
		switch resp.StatusCode {
		case http.StatusPartialContent:
			start, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
			if !ok || offset == 0 || start != offset {
				drainAndClose(resp.Body)
				if sink != nil && offset > 0 {
					// 已经发给客户端 offset 字节：重来会让客户端收到重复内容。
					// 分片保持原样，下次请求从这里续。
					_ = file.Close()
					return "", errAudioStreamCannotRestart
				}
				offset = 0
				_ = file.Truncate(0)
				lastErr = fmt.Errorf("音源返回的分片区间不可用（%q）", resp.Header.Get("Content-Range"))
				continue
			}
			expected = total
		case http.StatusOK:
			// 上游忽略了 Range（或者本来就是整段请求）：只能从第 0 字节写。
			if offset > 0 {
				drainAndClose(resp.Body)
				if sink != nil {
					// 同上：已发出去的字节不能撤回，分片留着供下次续传。
					_ = file.Close()
					return "", errAudioStreamCannotRestart
				}
				offset = 0
				if truncErr := file.Truncate(0); truncErr != nil {
					lastErr = fmt.Errorf("重置音频缓存失败: %w", truncErr)
					continue
				}
			}
			expected = resp.ContentLength
		case http.StatusRequestedRangeNotSatisfiable:
			// 分片比上游对象还长（对象换过）：丢掉分片重来。
			drainAndClose(resp.Body)
			if sink != nil && offset > 0 {
				_ = file.Close()
				return "", errAudioStreamCannotRestart
			}
			offset = 0
			_ = file.Truncate(0)
			lastErr = fmt.Errorf("音源分片已失效（%d）", resp.StatusCode)
			continue
		default:
			drainAndClose(resp.Body)
			lastErr = fmt.Errorf("音源返回 %d", resp.StatusCode)
			continue
		}

		if sink != nil && !started {
			// 直通模式下要在这里就把状态码与 Content-Length 写出去，客户端才能
			// 边收边播。写头的失败（客户端已断开）当作停止下载处理。
			if onStart != nil {
				onStart(expected, resp.Header.Get("Content-Type"))
			}
			started = true
		}

		if _, seekErr := file.Seek(offset, io.SeekStart); seekErr != nil {
			drainAndClose(resp.Body)
			lastErr = fmt.Errorf("音频缓存定位失败: %w", seekErr)
			continue
		}
		var written int64
		var writeErr error
		if sink != nil {
			written, writeErr = writeAudioChunkStream(file, sink, head, resp.Body, audioMediaObjectMaxBytes-offset)
		} else {
			written, writeErr = writeAudioChunk(file, head, resp.Body, audioMediaObjectMaxBytes-offset)
		}
		_ = resp.Body.Close()
		offset += written
		if errors.Is(writeErr, errAudioObjectTooLarge) {
			_ = file.Close()
			_ = os.Remove(partPath)
			return "", fmt.Errorf("已重试 %d 次: %w", attempts, writeErr)
		}
		var sinkErr *audioSinkError
		if errors.As(writeErr, &sinkErr) {
			// 客户端不要了：停止下载，已写入的分片留着供下次续传。
			_ = file.Close()
			return "", sinkErr
		}
		if writeErr != nil {
			// 已写入的字节保留在分片里，下一轮从新的断点续传。
			lastErr = writeErr
			continue
		}
		if expected >= 0 && offset != expected {
			lastErr = fmt.Errorf("音频写入不完整（%d/%d 字节）", offset, expected)
			continue
		}
		if offset == 0 {
			lastErr = errors.New("音源内容为空")
			continue
		}
		if truncErr := file.Truncate(offset); truncErr != nil {
			_ = file.Close()
			return "", fmt.Errorf("写入音频缓存失败: %w", truncErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return "", fmt.Errorf("写入音频缓存失败: %w", closeErr)
		}
		if renameErr := os.Rename(partPath, path); renameErr != nil {
			return "", fmt.Errorf("写入音频缓存失败: %w", renameErr)
		}
		s.pruneAudioMediaCache()
		return path, nil
	}

	_ = file.Close()
	if lastErr == nil {
		lastErr = errors.New("音源拉取失败")
	}
	if offset > 0 {
		return "", fmt.Errorf("已重试 %d 次: %w（已缓存 %d 字节，重试可续传）", attempts, lastErr, offset)
	}
	return "", fmt.Errorf("已重试 %d 次: %w", attempts, lastErr)
}

// writeAudioChunk 先把已读前缀补写回文件，再把响应体写到 limit 字节为止。
// 返回本次实际写入的字节数（出错时也包含已写成功的部分）。
func writeAudioChunk(w io.Writer, head []byte, body io.Reader, limit int64) (int64, error) {
	var total int64
	if len(head) > 0 {
		n, err := w.Write(head)
		total += int64(n)
		if err != nil {
			return total, err
		}
	}
	if limit <= 0 {
		return total, errAudioObjectTooLarge
	}
	n, err := io.Copy(w, io.LimitReader(body, limit-total+1))
	total += n
	if err != nil {
		return total, err
	}
	if total > limit {
		return total, errAudioObjectTooLarge
	}
	return total, nil
}

// writeAudioChunkStream 同 writeAudioChunk，但同时把字节写给客户端（首播直通）。
//
// 写盘失败要让整个下载停下来（本地状态不可信，继续发字节等于给客户端一份
// 服务端存不下的内容）；写客户端失败只说明接收方不要了，用 audioSinkError
// 标出来让上层保留分片、安静收尾。两者必须区分，否则播放器一关就把分片删了。
func writeAudioChunkStream(w io.Writer, sink io.Writer, head []byte, body io.Reader, limit int64) (int64, error) {
	var total int64
	writeBoth := func(p []byte) error {
		n, err := w.Write(p)
		total += int64(n)
		if err != nil {
			return err
		}
		if n > 0 {
			if _, sinkErr := sink.Write(p[:n]); sinkErr != nil {
				return &audioSinkError{err: sinkErr}
			}
		}
		return nil
	}
	if len(head) > 0 {
		if err := writeBoth(head); err != nil {
			return total, err
		}
	}
	if limit <= 0 {
		return total, errAudioObjectTooLarge
	}
	buf := make([]byte, 32<<10)
	remaining := limit - total + 1
	for {
		if remaining <= 0 {
			return total, errAudioObjectTooLarge
		}
		chunk := buf
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		n, readErr := body.Read(chunk)
		if n > 0 {
			if err := writeBoth(chunk[:n]); err != nil {
				return total, err
			}
			remaining -= int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return total, readErr
		}
	}
	if total > limit {
		return total, errAudioObjectTooLarge
	}
	return total, nil
}

// parseContentRange 解析 "bytes 100-199/1234" 的起点与总长度。
// 总长度未知（"*"）时 total 为 -1。
func parseContentRange(v string) (start, total int64, ok bool) {
	v = strings.TrimSpace(v)
	const prefix = "bytes "
	if len(v) <= len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
		return 0, -1, false
	}
	v = strings.TrimSpace(v[len(prefix):])
	slash := strings.IndexByte(v, '/')
	if slash < 0 {
		return 0, -1, false
	}
	span, size := v[:slash], strings.TrimSpace(v[slash+1:])
	dash := strings.IndexByte(span, '-')
	if dash <= 0 {
		return 0, -1, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(span[:dash]), 10, 64)
	if err != nil || start < 0 {
		return 0, -1, false
	}
	if size == "" || size == "*" {
		return start, -1, true
	}
	total, err = strconv.ParseInt(size, 10, 64)
	if err != nil || total <= 0 {
		return start, -1, true
	}
	return start, total, true
}

// drainAndClose 丢弃剩余响应体并关闭连接（尽量让上游把连接收回去）。
func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	_ = body.Close()
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
