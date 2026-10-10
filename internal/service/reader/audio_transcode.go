// 本地/远端有声书的按需转码：源文件是浏览器解不了的格式（典型是 WMA/ASF）时，
// 用 ffmpeg 转成 mp3 落到缓存目录，再按 Range 下发。
//
// 为什么落盘而不是实时流式输出：有声书必须能拖动进度，而 http.ServeContent 的
// Range 支持要求可随机读取的文件；实时管道没有长度信息，一旦 seek 就废掉。
// 一章通常几 MB，转一次几秒，之后同一章秒开。
package reader

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/truewhile/MeBox/internal/helper"
)

// audioTranscodeFlight 同一章节并发命中时只跑一次 ffmpeg（按缓存路径去重）。
var audioTranscodeFlight singleflight.Group

// 转码互斥表：singleflight 只保证同一 key 不重复执行，prune 与写文件仍需串行。
var audioTranscodeMu sync.Mutex

const (
	// audioTranscodeDirName 缓存目录名，位于 cache.cache_dir 之下。
	audioTranscodeDirName = "reader-audio"
	// maxAudioTranscodeCacheBytes 转码缓存上限，超出按修改时间淘汰最旧的。
	maxAudioTranscodeCacheBytes = 4 << 30
	// audioTranscodeKeepRatio 触发淘汰后回落到上限的比例，避免每次写入都淘汰。
	audioTranscodeKeepRatio = 0.9
	// maxAudioTranscodeDuration 单章转码超时。
	maxAudioTranscodeDuration = 15 * time.Minute
	// audioTranscodeBitrate 语音内容 96k 足够，体积约为原 WMA 的两倍以内。
	audioTranscodeBitrate = "96k"
)

// needsTranscodeAudioExt 明确需要转码的容器/编码（浏览器都无法直接解码）。
// 只列已知有问题的：未列出的格式维持原样直出，避免把本来能播的流也拖去转码。
var needsTranscodeAudioExt = map[string]bool{
	".wma": true, ".asf": true, ".wmv": true, ".ape": true, ".wv": true,
	".ac3": true, ".dts": true, ".amr": true, ".tta": true, ".dsf": true, ".dff": true,
}

// audioSourceExt 取音频地址的扩展名（小写带点）。
//
// 既要认本地路径（D:\x\a.wma），也要认带 query 的远端地址
// （…/video.wma?acct=…）——后者直接 filepath.Ext 会把 query 一起算进去。
func audioSourceExt(source string) string {
	raw := strings.TrimSpace(source)
	if raw == "" {
		return ""
	}
	// scheme 长度 >1 才算真 URL：Windows 盘符（D:\…）会被 url.Parse 当成单字符 scheme
	if u, err := url.Parse(raw); err == nil && len(u.Scheme) > 1 {
		return strings.ToLower(filepath.Ext(u.Path))
	}
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	return strings.ToLower(filepath.Ext(raw))
}

// needsAudioTranscode 判断该音频是否必须转码后才能给浏览器播放。
func needsAudioTranscode(source string) bool {
	return needsTranscodeAudioExt[audioSourceExt(source)]
}

// ffmpegBinary 解析可用的 ffmpeg 可执行文件；找不到返回空串。
func (s *ReaderService) ffmpegBinary() string {
	path := strings.TrimSpace(s.cfg.App.FFmpegPath)
	if path == "" {
		path = "ffmpeg"
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return ""
	}
	return resolved
}

// ErrAudioTranscodeUnavailable 服务器没有 ffmpeg，无法转码该格式。
var ErrAudioTranscodeUnavailable = errors.New("服务器未安装 ffmpeg")

// transcodeMissingFFmpegError 给前端一条能直接照做的提示。
func transcodeMissingFFmpegError(source string) error {
	ext := strings.TrimPrefix(audioSourceExt(source), ".")
	if ext == "" {
		ext = "该"
	}
	return fmt.Errorf("这个音频是 %s 格式，浏览器无法直接播放，需要服务器转码；%w，请在设置里配置 app.ffmpeg_path 或安装 ffmpeg",
		strings.ToUpper(ext), ErrAudioTranscodeUnavailable)
}

// ─── 签名（与本地音频同一套：<audio src> 带不上 JWT） ─────────────────────

func (s *ReaderService) signAudioTranscode(bookID, source string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.Secrets.JWTSecret))
	mac.Write([]byte(bookID + "|transcode|" + source))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// AudioTranscodeURL 需要转码的音频地址（签名代理）。
func (s *ReaderService) AudioTranscodeURL(bookID, source string) string {
	return "/api/reader/audio/transcode?b=" + url.QueryEscape(bookID) +
		"&u=" + base64.RawURLEncoding.EncodeToString([]byte(source)) +
		"&s=" + s.signAudioTranscode(bookID, source)
}

// VerifyAudioTranscodeURL 校验签名并还原原始音频地址（本地路径或远端 URL）。
func (s *ReaderService) VerifyAudioTranscodeURL(bookID, encoded, sig string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("转码地址解码失败")
	}
	expect := s.signAudioTranscode(bookID, string(raw))
	if !hmac.Equal([]byte(expect), []byte(sig)) {
		return "", fmt.Errorf("转码签名校验失败")
	}
	return string(raw), nil
}

// ─── 缓存与转码 ──────────────────────────────────────────────────────────

func (s *ReaderService) audioTranscodeDir() (string, error) {
	base := strings.TrimSpace(s.cfg.Cache.CacheDir)
	if base == "" {
		base = filepath.Join(s.cfg.App.DataDir, "cache")
	}
	dir := filepath.Join(base, audioTranscodeDirName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("创建转码缓存目录失败: %w", err)
	}
	return dir, nil
}

// audioTranscodeCachePath 缓存文件名由（书 + 源地址）哈希决定：同一章重复播放直接命中。
func audioTranscodeCachePath(dir, bookID, source string) string {
	sum := sha256.Sum256([]byte(bookID + "|" + source))
	return filepath.Join(dir, hex.EncodeToString(sum[:16])+".mp3")
}

// AudioTranscodeCachePath 返回该音轨的转码缓存文件路径（不触发转码），
// 供测试与运维排查使用。
func (s *ReaderService) AudioTranscodeCachePath(bookID, source string) (string, error) {
	dir, err := s.audioTranscodeDir()
	if err != nil {
		return "", err
	}
	return audioTranscodeCachePath(dir, bookID, source), nil
}

// EnsureTranscodedAudio 确保该音频已有转码结果，返回可 Range 下发的 mp3 路径。
func (s *ReaderService) EnsureTranscodedAudio(ctx context.Context, bookID, source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", fmt.Errorf("缺少音频地址")
	}
	dir, err := s.audioTranscodeDir()
	if err != nil {
		return "", err
	}
	dst := audioTranscodeCachePath(dir, bookID, source)
	if ok := touchCachedAudio(dst); ok {
		return dst, nil
	}
	if s.ffmpegBinary() == "" {
		return "", transcodeMissingFFmpegError(source)
	}

	v, err, _ := audioTranscodeFlight.Do(dst, func() (any, error) {
		// 排队期间别的请求可能已经转好了
		if ok := touchCachedAudio(dst); ok {
			return dst, nil
		}
		if err := s.transcodeAudioFile(ctx, bookID, source, dst, dir); err != nil {
			return nil, err
		}
		return dst, nil
	})
	if err != nil {
		return "", err
	}
	path, _ := v.(string)
	if path == "" {
		return "", fmt.Errorf("转码结果不可用")
	}
	return path, nil
}

// touchCachedAudio 命中缓存时刷新访问时间，作为 LRU 依据。
func touchCachedAudio(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return false
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return true
}

// transcodeAudioFile 跑一次 ffmpeg 并原子落盘（先写 .part 再 rename）。
func (s *ReaderService) transcodeAudioFile(ctx context.Context, bookID, source, dst, dir string) error {
	ffmpeg := s.ffmpegBinary()
	if ffmpeg == "" {
		return transcodeMissingFFmpegError(source)
	}
	remote := isRemoteMediaURL(source)
	if !remote {
		if info, err := os.Stat(source); err != nil || info.IsDir() {
			return fmt.Errorf("音频文件已丢失")
		}
	}

	// 转码与淘汰串行，避免边写边删
	audioTranscodeMu.Lock()
	pruneAudioTranscodeCache(dir, maxAudioTranscodeCacheBytes)
	audioTranscodeMu.Unlock()

	tmp := dst + ".part"
	defer os.Remove(tmp)

	headers := s.transcodeInputHeaders(ctx, bookID, source)
	// 客户端断开不该杀掉已开始的转码：产物对下次播放仍然有用
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), maxAudioTranscodeDuration)
	defer cancel()

	cmd := exec.CommandContext(runCtx, ffmpeg, buildFFmpegAudioArgs(source, tmp, headers)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("转码超时（超过 %s）", maxAudioTranscodeDuration)
		}
		return fmt.Errorf("转码失败: %v: %s", err, truncateForError(string(out), 300))
	}
	info, err := os.Stat(tmp)
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("转码没有产生有效输出")
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("写入转码缓存失败: %w", err)
	}
	return nil
}

// transcodeInputHeaders 远端音源要带的请求头：浏览器 UA 预设 + 书源请求头 + 登录态。
// 本机 /api/strm 这类内网地址不需要，但书源直链类的有声书要靠它才能取到。
func (s *ReaderService) transcodeInputHeaders(ctx context.Context, bookID, source string) map[string]string {
	if !isRemoteMediaURL(source) || s.repo == nil {
		return nil
	}
	headers := map[string]string{}
	for k, v := range helper.HTTPHeaderPresets() {
		headers[k] = v
	}
	book, err := s.repo.GetBook(ctx, bookID)
	if err != nil || book == nil || strings.TrimSpace(book.Origin) == "" {
		return headers
	}
	if src, err := s.repo.GetSourceAnyByURL(ctx, book.Origin); err == nil && src != nil && src.Header != "" {
		var extra map[string]any
		if json.Unmarshal([]byte(src.Header), &extra) == nil {
			for k, v := range extra {
				headers[k] = fmt.Sprintf("%v", v)
			}
		}
	}
	state := s.newSourceState(ctx, book.Origin)
	for k, v := range state.LoginHeaderMap() {
		if !strings.EqualFold(k, "cookie") {
			headers[k] = v
		}
	}
	if ck := state.CookieForRequest(source); ck != "" {
		headers["Cookie"] = ck
	}
	// 默认 Referer 同上：聚合类书源的 origin 是显示名，拼出来的 Referer 非法，
	// 会被音源/图床判盗链。只在 origin 是真正的 http(s) 地址时才补。
	if headers["Referer"] == "" {
		if referer := sourceReferer(book.Origin); referer != "" {
			headers["Referer"] = referer
		}
	}
	return headers
}

// buildFFmpegAudioArgs 组装音频转码参数（纯函数，便于测试）。
// 只取第一条音频流：WMA 常把专辑封面挂在视频流上，-vn 一并丢掉。
func buildFFmpegAudioArgs(source, output string, headers map[string]string) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}
	if len(headers) > 0 {
		keys := make([]string, 0, len(headers))
		for k := range headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(headers[k])
			b.WriteString("\r\n")
		}
		args = append(args, "-headers", b.String())
	}
	return append(args,
		"-i", source,
		"-vn", "-map", "0:a:0",
		"-c:a", "libmp3lame", "-b:a", audioTranscodeBitrate,
		"-f", "mp3", output,
	)
}

// pruneAudioTranscodeCache 缓存超过上限时按访问时间淘汰，回落到上限的 90%。
func pruneAudioTranscodeCache(dir string, maxBytes int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type cacheItem struct {
		path string
		size int64
		mod  time.Time
	}
	items := make([]cacheItem, 0, len(entries))
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".mp3") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, cacheItem{filepath.Join(dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= maxBytes {
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	target := int64(float64(maxBytes) * audioTranscodeKeepRatio)
	for _, it := range items {
		if total <= target {
			break
		}
		if os.Remove(it.path) == nil {
			total -= it.size
		}
	}
}

// truncateForError 截断 ffmpeg 输出，避免把整段 stderr 塞进响应。
func truncateForError(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
