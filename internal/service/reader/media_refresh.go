package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

// 本文件处理「音频直链的签名票过期」这一类上游故障。
//
// 背景：听书源（聆阅·懒人听书这类）的播放地址是第三方中转现签的 CDN 直链，
// 形如 https://vb.stream.tencentmusic.com/long/xxx.m4a?...&vkey=...&ttl=86400000。
// 票的有效性不只取决于时间：中转背后是多个账号 token 轮换，某个 uid/vkey 组合
// 会被 CDN 判掉，此时上游返回**空的 401**（无 Content-Type、无 body），而同一章
// 隔几百毫秒重新签一张票就又能取到。
//
// 旧行为：媒体代理把这个 401 原样透传给播放器，播放器重试的又是同一个 URL，
// 于是 App 日志里出现连续 4~8 个 401；同时音频清单还有 30 分钟缓存，这 30 分钟
// 内 /content 一直下发同一张死票，表现为「这本书怎么都播不了，换本书才行」。
//
// 现在的行为：代理遇到 401/403/410 时先把该章的缓存作废、重新走一次书源取正文，
// 拿到新票再拉一次；新票同时写回缓存，后续请求自然就是有效的。

// mediaRefreshTTL 新票的短缓存：播放器一次失败会立刻用同一 URL 重试多次，
// 这里让并发的重试共用一次回源，避免把书源打成限流。
const mediaRefreshTTL = 2 * time.Minute

// mediaRefreshEntry 一次成功换票的结果。
type mediaRefreshEntry struct {
	url string
	exp time.Time
}

// mediaRefreshState 保护「旧直链 → 新直链」的短缓存。
type mediaRefreshState struct {
	mu      sync.Mutex
	entries map[string]mediaRefreshEntry
}

// lookupMediaRefresh 查换票短缓存（过期条目顺手清掉）。
func (s *ReaderService) lookupMediaRefresh(staleURL string) (string, bool) {
	s.mediaRefresh.mu.Lock()
	defer s.mediaRefresh.mu.Unlock()
	entry, ok := s.mediaRefresh.entries[staleURL]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.exp) {
		delete(s.mediaRefresh.entries, staleURL)
		return "", false
	}
	return entry.url, true
}

// rememberMediaRefresh 记住换票结果，并顺手清理过期条目（表很小，遍历即可）。
func (s *ReaderService) rememberMediaRefresh(staleURL, freshURL string) {
	s.mediaRefresh.mu.Lock()
	defer s.mediaRefresh.mu.Unlock()
	if s.mediaRefresh.entries == nil {
		s.mediaRefresh.entries = map[string]mediaRefreshEntry{}
	}
	now := time.Now()
	for k, v := range s.mediaRefresh.entries {
		if now.After(v.exp) {
			delete(s.mediaRefresh.entries, k)
		}
	}
	s.mediaRefresh.entries[staleURL] = mediaRefreshEntry{url: freshURL, exp: now.Add(mediaRefreshTTL)}
}

// RefreshMediaURL 在直链签名失效后重新换一张票。
//
// staleURL 是书源侧产出的原始媒体地址（媒体代理已还原、未加签名）。返回三个值：
//
//	refreshable=false  这个地址不是「可换票的音轨」（漫画图片、封面、或并非来自
//	                   章节缓存的地址），调用方应原样透传上游状态，保持旧行为；
//	refreshable=true 且 err!=nil  确实是音轨但换票失败，调用方应给出明确错误，
//	                   别让播放器对着必然失败的地址反复重试；
//	refreshable=true 且 err==nil  拿到了新票。
//
// 换票流程：在书架书籍的章节缓存里找到它所属的那一章 → 作废该章缓存（否则
// /content 会继续下发同一张死票）→ 重新走书源取正文 → 按同一个音轨序号取新地址。
func (s *ReaderService) RefreshMediaURL(ctx context.Context, book *model.ReaderBook, staleURL string) (string, bool, error) {
	if s == nil || s.repo == nil || book == nil {
		return "", false, nil
	}
	staleURL = strings.TrimSpace(staleURL)
	if staleURL == "" {
		return "", false, nil
	}
	if fresh, ok := s.lookupMediaRefresh(staleURL); ok {
		return fresh, true, nil
	}
	sourceID := s.contentSourceID(ctx, nil, book)
	if sourceID == "" {
		return "", false, nil
	}
	bookKey := contentBookKey(sourceID, book.BookURL)
	rows, err := s.repo.ListContentCacheByBook(ctx, bookKey)
	if err != nil || len(rows) == 0 {
		return "", false, nil
	}
	for i := range rows {
		row := rows[i]
		payload, ok := s.readContentPayload(&row)
		if !ok || payload.ContentType != "audio" {
			continue
		}
		trackIndex := indexOfString(payload.Tracks, staleURL)
		if trackIndex < 0 {
			continue
		}
		ch, ok := s.chapterForCacheRow(ctx, book, row)
		if !ok {
			return "", true, fmt.Errorf("章节已不在目录中")
		}
		// 先作废：即使后面重抓失败，也不会继续把死票发给播放器。
		s.dropContentCacheRow(ctx, &row)
		out, fetchErr := s.fetchChapterContentShared(ctx, book, ch)
		if fetchErr != nil {
			return "", true, fmt.Errorf("重新取章节失败: %w", fetchErr)
		}
		if out == nil || trackIndex >= len(out.Tracks) {
			return "", true, fmt.Errorf("新内容没有对应的音轨")
		}
		fresh := strings.TrimSpace(out.Tracks[trackIndex])
		if strings.HasPrefix(fresh, "/api/") {
			// fetchChapterContentShared 返回缓存原样地址；兜底解析一次代理形态。
			return "", true, fmt.Errorf("书源返回了代理地址")
		}
		if fresh == "" || fresh == staleURL {
			return "", true, fmt.Errorf("书源返回了相同地址")
		}
		s.rememberMediaRefresh(staleURL, fresh)
		return fresh, true, nil
	}
	return "", false, nil
}

// readContentPayload 读取一条章节缓存的落盘载荷。
func (s *ReaderService) readContentPayload(row *model.ReaderContentCache) (cachedChapterContent, bool) {
	var payload cachedChapterContent
	path := s.contentPathFromRow(row)
	if path == "" {
		return payload, false
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- 路径由服务端生成
	if err != nil {
		return payload, false
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, false
	}
	return payload, true
}

// chapterForCacheRow 按缓存行的章节键找回目录里的章节（remap 后键仍一致）。
func (s *ReaderService) chapterForCacheRow(ctx context.Context, book *model.ReaderBook, row model.ReaderContentCache) (model.ReaderChapter, bool) {
	chapters, err := s.repo.ListChapters(ctx, book.ID)
	if err != nil {
		return model.ReaderChapter{}, false
	}
	for _, ch := range chapters {
		if contentChapterKey(book, ch) == row.ChapterKey {
			return ch, true
		}
	}
	// 退一步按序号取：目录里同一章可能因为标题变化换了身份键。
	if row.ChapterIndex >= 0 && row.ChapterIndex < len(chapters) && !chapters[row.ChapterIndex].IsVolume {
		return chapters[row.ChapterIndex], true
	}
	return model.ReaderChapter{}, false
}

// indexOfString 返回目标字符串在列表中的下标，找不到返回 -1。
func indexOfString(list []string, target string) int {
	target = strings.TrimSpace(target)
	for i, item := range list {
		if strings.TrimSpace(item) == target {
			return i
		}
	}
	return -1
}
