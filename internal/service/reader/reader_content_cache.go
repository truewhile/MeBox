package reader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

// 本文件实现阅读正文的持久缓存（对应 legado BookHelp 的章节正文缓存 + CacheBook）。
//
// 设计要点：
//   - 缓存「书源侧产物」：getContentFrom 的输出（书源 replaceRegex 之后，
//     用户替换规则、签名代理改写之前）。因此缓存里不含任何用户维度的处理结果，
//     用户替换规则在读出后逐请求应用，规则改动即时生效。
//   - 缓存键按书源行组织（书源按用户独立，见 contentBookKey 的说明），
//     不同用户即使持有同一 URL 的书源也各自一份缓存。
//   - 索引落库（ReaderContentCache），内容落盘
//     （cache_dir/reader-content/<sourceKey>/<bookKey>/<chapterKey>.json）。
//   - 缓存键只包含书源行、章节身份、书源指纹与格式版本：不含书籍行 ID
//     （目录刷新会重建行，见 RemapContentCacheOnTocChange）。

// readerContentFormatVersion 载荷格式版本。解析管线（正文归一 / 段评提取 / 图片标记）
// 语义变化时必须递增：旧版本条目会被当作未命中并重抓，避免读到旧结构的缓存。
const readerContentFormatVersion = 1

// readerContentMaxEntryBytes 单条缓存的字节上限（超过不缓存，避免超大章节占满盘）。
const readerContentMaxEntryBytes = 16 << 20

// audioCacheTTL 音频清单的短 TTL：CDN 直链常带签名/过期参数，缓存太久会拿到失效地址。
const audioCacheTTL = 30 * time.Minute

// cachedChapterContent 落盘的载荷。
type cachedChapterContent struct {
	FormatVersion int              `json:"v"`
	SourceHash    string           `json:"src"`
	ContentType   string           `json:"type"`
	Content       string           `json:"content,omitempty"`
	Tracks        []string         `json:"tracks,omitempty"`
	Images        []string         `json:"images,omitempty"`
	ImageStyle    string           `json:"image_style,omitempty"`
	IsHLS         bool             `json:"hls,omitempty"`
	Comments      []ContentComment `json:"comments,omitempty"`
	SavedAt       int64            `json:"saved_at"`
}

// contentFlight 一次进行中的正文抓取，并发的调用方共享结果（读穿透单飞）。
type contentFlight struct {
	done chan struct{}
	data *ChapterContent
	err  error
}

// ─── 键与身份 ──────────────────────────────────────────────────────────────

// contentBookKey 书源身份哈希：书源行 ID + 书本地址。
//
// 用书源行 ID 而不是 origin（书源 URL）：书源按用户独立，不同用户可以持有同一
// URL 但规则/header 不同的副本，按 URL 共享缓存会把别人书源的产物喂进来。
// 用「行 ID + 书本地址」而不是书源显示名：聚合源的 origin 是显示名，多本同源书会撞。
func contentBookKey(sourceID, bookURL string) string {
	sum := sha256.Sum256([]byte(sourceID + "\x00" + strings.TrimSpace(bookURL)))
	return hex.EncodeToString(sum[:16])
}

// contentOriginKey 书源行维度的磁盘目录名（同一书源的不同书共享这一层）。
func contentOriginKey(sourceID string) string {
	sum := sha256.Sum256([]byte(sourceID))
	return hex.EncodeToString(sum[:16])
}

// contentChapterIdentity 章节身份原文：绝对化 URL 优先，卷/空地址退化为标题。
func contentChapterIdentity(book *model.ReaderBook, ch model.ReaderChapter) string {
	if ch.URL != "" && !ch.IsVolume {
		base := book.BookURL
		if strings.TrimSpace(book.TocURL) != "" {
			base = book.TocURL
		}
		if abs := rule.GetAbsoluteURL(base, ch.URL); abs != "" {
			return "url|" + abs
		}
		return "url|" + strings.TrimSpace(ch.URL)
	}
	return "title|" + strings.TrimSpace(ch.Title)
}

// contentChapterKey 章节身份哈希。
func contentChapterKey(book *model.ReaderBook, ch model.ReaderChapter) string {
	sum := sha256.Sum256([]byte(contentChapterIdentity(book, ch)))
	return hex.EncodeToString(sum[:16])
}

// contentSourceHash 书源内容指纹：RawJSON 哈希（书源更新后旧缓存自然失效）。
func contentSourceHash(src *model.ReaderBookSource) string {
	if src == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(src.RawJSON))
	return hex.EncodeToString(sum[:16])
}

// ─── 磁盘布局 ──────────────────────────────────────────────────────────────

// readerContentDir 正文缓存根目录。
func (s *ReaderService) readerContentDir() string {
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
	return filepath.Join(base, "reader-content")
}

// contentFilePath 单条缓存的磁盘路径：<root>/<sourceKey>/<bookKey>/<chapterKey>.json。
// chapterKey 已是 hex 哈希，不含路径分隔符。
func (s *ReaderService) contentFilePath(sourceID, bookURL, chapterKey string) string {
	root := s.readerContentDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, contentOriginKey(sourceID), contentBookKey(sourceID, bookURL), chapterKey+".json")
}

// contentBookDir 某本书的缓存目录（整本清理用）。
func (s *ReaderService) contentBookDir(sourceID, bookURL string) string {
	root := s.readerContentDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, contentOriginKey(sourceID), contentBookKey(sourceID, bookURL))
}

// writeContentFile 原子写入缓存文件（临时文件 + rename）。
func writeContentFile(path string, payload []byte) error {
	if path == "" {
		return fmt.Errorf("缓存目录未配置")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".content-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// removeContentFile 删除缓存文件（不存在视为成功）。
func removeContentFile(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// ─── 读写 ──────────────────────────────────────────────────────────────────

// contentSourceID 缓存键用的书源行 ID。src 缺失时按 origin 兜底查一次：
// 直接调用（测试、诊断路径）可能只拿到书籍与 origin。
func (s *ReaderService) contentSourceID(ctx context.Context, src *model.ReaderBookSource, book *model.ReaderBook) string {
	if src != nil && src.ID != "" {
		return src.ID
	}
	if s == nil || s.repo == nil || book == nil {
		return ""
	}
	found, err := s.repo.GetSourceAnyByURL(ctx, book.Origin)
	if err != nil || found == nil {
		return ""
	}
	return found.ID
}

// loadCachedContent 读取一章节的缓存：命中返回内容与 true。
//
// contentType 为空表示「按章节取任意类型」（正文链路不需要预知类型）；给出具体类型时
// 用于带类型校验的探测（如音频 TTL 的测试与诊断）。
//
// 校验链：索引存在 → 格式版本一致 → 书源指纹一致 → 未过期 → 载荷可解析。
func (s *ReaderService) loadCachedContent(ctx context.Context, src *model.ReaderBookSource, bs *BookSource, book *model.ReaderBook, ch model.ReaderChapter, contentType string) (*ChapterContent, bool) {
	if s == nil || s.repo == nil || book == nil {
		return nil, false
	}
	sourceID := s.contentSourceID(ctx, src, book)
	if sourceID == "" {
		return nil, false
	}
	bookKey := contentBookKey(sourceID, book.BookURL)
	chapterKey := contentChapterKey(book, ch)
	row, err := s.repo.GetContentCacheByChapter(ctx, bookKey, chapterKey, contentType)
	if err != nil || row == nil {
		return nil, false
	}
	if row.FormatVersion != readerContentFormatVersion {
		s.dropContentCacheRow(ctx, row)
		return nil, false
	}
	if row.SourceHash != "" && row.SourceHash != contentSourceHash(src) {
		// 书源更新过：旧正文可能已失效，删掉重抓。
		s.dropContentCacheRow(ctx, row)
		return nil, false
	}
	now := time.Now().Unix()
	if row.ExpiresAt > 0 && row.ExpiresAt < now {
		s.dropContentCacheRow(ctx, row)
		return nil, false
	}
	path := s.contentFilePath(sourceID, book.BookURL, chapterKey)
	raw, err := os.ReadFile(path) // #nosec G304 -- 路径由服务端生成
	if err != nil {
		s.dropContentCacheRow(ctx, row)
		return nil, false
	}
	var payload cachedChapterContent
	if err := json.Unmarshal(raw, &payload); err != nil || payload.FormatVersion != readerContentFormatVersion {
		s.dropContentCacheRow(ctx, row)
		return nil, false
	}
	// 命中：刷新 LRU 时间与计数（异步失败不影响读取）。
	_ = s.repo.TouchContentCache(ctx, row.ID, row.Hits+1, now)

	out := &ChapterContent{
		Type:         payload.ContentType,
		Content:      payload.Content,
		Tracks:       payload.Tracks,
		Images:       payload.Images,
		ImageStyle:   payload.ImageStyle,
		IsHLS:        payload.IsHLS,
		Comments:     payload.Comments,
		declaredType: -1,
	}
	return out, true
}

// saveCachedContent 写入一章节的缓存（空内容不写，与报错语义保持一致）。
func (s *ReaderService) saveCachedContent(ctx context.Context, src *model.ReaderBookSource, book *model.ReaderBook, ch model.ReaderChapter, out *ChapterContent) {
	if s == nil || s.repo == nil || book == nil || out == nil || chapterContentEmpty(out) {
		return
	}
	payload := cachedChapterContent{
		FormatVersion: readerContentFormatVersion,
		SourceHash:    contentSourceHash(src),
		ContentType:   out.Type,
		Content:       out.Content,
		Tracks:        out.Tracks,
		Images:        out.Images,
		ImageStyle:    out.ImageStyle,
		IsHLS:         out.IsHLS,
		Comments:      out.Comments,
		SavedAt:       time.Now().Unix(),
	}
	raw, err := json.Marshal(payload)
	if err != nil || len(raw) > readerContentMaxEntryBytes {
		return
	}
	sourceID := s.contentSourceID(ctx, src, book)
	if sourceID == "" {
		return
	}
	path := s.contentFilePath(sourceID, book.BookURL, contentChapterKey(book, ch))
	if err := writeContentFile(path, raw); err != nil {
		if s.log != nil {
			s.log.Warn("reader: 写入正文缓存失败", zap.String("path", path), zap.Error(err))
		}
		return
	}
	now := time.Now().Unix()
	row := &model.ReaderContentCache{
		OriginHash:      contentOriginKey(sourceID),
		BookKey:         contentBookKey(sourceID, book.BookURL),
		ChapterKey:      contentChapterKey(book, ch),
		ChapterIdentity: contentChapterIdentity(book, ch),
		ChapterIndex:    ch.Index,
		ContentType:     out.Type,
		SourceHash:      payload.SourceHash,
		FormatVersion:   readerContentFormatVersion,
		SizeBytes:       int64(len(raw)),
		AssetCount:      len(out.Tracks) + len(out.Images),
		ExpiresAt:       s.contentExpiry(out.Type, now),
		LastAccessAt:    now,
	}
	if err := s.repo.UpsertContentCache(ctx, row); err != nil && s.log != nil {
		s.log.Warn("reader: 写入正文缓存索引失败", zap.Error(err))
	}
}

// contentExpiry 按内容类型给出过期时间：音频清单短 TTL（直链会过期），文本/图片用配置的 TTL。
func (s *ReaderService) contentExpiry(contentType string, now int64) int64 {
	if contentType == "audio" {
		return now + int64(audioCacheTTL.Seconds())
	}
	ttlHours := 0
	if s != nil && s.cfg != nil {
		ttlHours = s.cfg.Cache.ReaderContentTTLHours
	}
	if ttlHours <= 0 {
		return 0
	}
	return now + int64(ttlHours)*3600
}

// dropContentCacheRow 删除索引并清理磁盘文件。
func (s *ReaderService) dropContentCacheRow(ctx context.Context, row *model.ReaderContentCache) {
	if s == nil || s.repo == nil || row == nil {
		return
	}
	_ = s.repo.DeleteContentCacheRow(ctx, row.ID)
	path := s.contentPathFromRow(row)
	removeContentFile(path)
	if path != "" {
		s.pruneContentDirIfEmpty(filepath.Dir(path))
	}
}

// pruneContentDirIfEmpty 删掉空目录（缓存清理后不留空壳）。
func (s *ReaderService) pruneContentDirIfEmpty(dir string) {
	if dir == "" || dir == s.readerContentDir() {
		return
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
}

// ClearContentCacheForBook 清理某书源行下一本书的全部正文缓存（换源/移出书架时调用）。
//
// sourceID 是书源行 ID：书源按用户独立，缓存也随之按书源行隔离，所以删除
// 自己那份书源或换源不会影响其他用户的同名书源缓存。
func (s *ReaderService) ClearContentCacheForBook(ctx context.Context, sourceID, bookURL string) {
	if s == nil || s.repo == nil || sourceID == "" || bookURL == "" {
		return
	}
	bookKey := contentBookKey(sourceID, bookURL)
	if _, err := s.repo.DeleteContentCacheByBook(ctx, bookKey); err != nil {
		return
	}
	if dir := s.contentBookDir(sourceID, bookURL); dir != "" {
		_ = os.RemoveAll(dir)
		s.pruneContentDirIfEmpty(filepath.Dir(dir))
	}
}

// PruneContentCache 清理阅读正文缓存（TTL 过期 + 容量 LRU），供调度器按小时调用。
func (s *ReaderService) PruneContentCache(ctx context.Context) {
	s.pruneReaderContentCache(ctx)
}

// pruneReaderContentCache 容量/TTL 淘汰：过期条目直接删，超配额按 LRU 删到 90%。
func (s *ReaderService) pruneReaderContentCache(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	now := time.Now().Unix()
	if expired, err := s.repo.ListContentCacheExpired(ctx, now, 500); err == nil {
		for i := range expired {
			row := expired[i]
			_ = s.repo.DeleteContentCacheRow(ctx, row.ID)
			removeContentFile(s.contentPathFromRow(&row))
		}
	}
	maxMB := 0
	if s.cfg != nil {
		maxMB = s.cfg.Cache.ReaderContentMaxSizeMB
	}
	if maxMB <= 0 {
		return
	}
	_, totalBytes, err := s.repo.ContentCacheStats(ctx)
	if err != nil {
		return
	}
	limit := int64(maxMB) << 20
	if totalBytes <= limit {
		return
	}
	target := limit * 9 / 10
	oldest, err := s.repo.ListContentCacheOldest(ctx, 500)
	if err != nil {
		return
	}
	for i := range oldest {
		if totalBytes <= target {
			break
		}
		row := oldest[i]
		_ = s.repo.DeleteContentCacheRow(ctx, row.ID)
		removeContentFile(s.contentPathFromRow(&row))
		totalBytes -= row.SizeBytes
	}
}

// contentPathFromRow 由索引行拼磁盘路径（索引存了 origin_hash / book_key / chapter_key）。
func (s *ReaderService) contentPathFromRow(row *model.ReaderContentCache) string {
	if s == nil || row == nil {
		return ""
	}
	root := s.readerContentDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, row.OriginHash, row.BookKey, row.ChapterKey+".json")
}

// ─── 目录刷新后的缓存迁移（对应 legado BookHelp.remapContentCache） ─────────

// RemapContentCacheOnTocChange 目录刷新后迁移缓存键：同一章按绝对化 URL 命中、
// 标题唯一命中兜底。不迁移的话每次「更新目录」都会让已缓存正文全部冷启动。
func (s *ReaderService) RemapContentCacheOnTocChange(ctx context.Context, book *model.ReaderBook, oldChapters, newChapters []model.ReaderChapter) {
	if s == nil || s.repo == nil || book == nil || len(oldChapters) == 0 || len(newChapters) == 0 {
		return
	}
	sourceID := s.contentSourceID(ctx, nil, book)
	if sourceID == "" {
		return
	}
	bookKey := contentBookKey(sourceID, book.BookURL)
	rows, err := s.repo.ListContentCacheByBook(ctx, bookKey)
	if err != nil || len(rows) == 0 {
		return
	}
	// 旧身份 → 新章节。
	byURL := map[string]model.ReaderChapter{}
	byTitle := map[string][]model.ReaderChapter{}
	for _, ch := range newChapters {
		if ch.URL != "" && !ch.IsVolume {
			base := book.BookURL
			if strings.TrimSpace(book.TocURL) != "" {
				base = book.TocURL
			}
			abs := rule.GetAbsoluteURL(base, ch.URL)
			if abs == "" {
				abs = strings.TrimSpace(ch.URL)
			}
			byURL["url|"+abs] = ch
		}
		if t := strings.TrimSpace(ch.Title); t != "" {
			byTitle["title|"+t] = append(byTitle["title|"+t], ch)
		}
	}
	for i := range rows {
		row := rows[i]
		var target model.ReaderChapter
		found := false
		if ch, ok := byURL[row.ChapterIdentity]; ok {
			target, found = ch, true
		} else if matches := byTitle[row.ChapterIdentity]; len(matches) == 1 {
			// 标题唯一命中才迁移：多个同标题章节时无法确定是哪一章，宁可重抓。
			target, found = matches[0], true
		}
		if !found {
			continue
		}
		newKey := contentChapterKey(book, target)
		if newKey == row.ChapterKey {
			continue // 身份未变，只更新序号
		}
		oldPath := s.contentPathFromRow(&row)
		newPath := s.contentFilePath(sourceID, book.BookURL, newKey)
		if oldPath != "" && newPath != "" {
			if err := os.MkdirAll(filepath.Dir(newPath), 0o750); err == nil {
				// 文件不在（只留下索引）时忽略：新条目下次读取会自动重抓。
				_ = os.Rename(oldPath, newPath)
			}
		}
		_ = s.repo.DeleteContentCacheRow(ctx, row.ID)
		row.ID = ""
		row.ChapterKey = newKey
		row.ChapterIdentity = contentChapterIdentity(book, target)
		row.ChapterIndex = target.Index
		row.LastAccessAt = time.Now().Unix()
		_ = s.repo.UpsertContentCache(ctx, &row)
	}
}
