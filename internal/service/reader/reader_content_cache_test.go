package reader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

// 正文持久缓存（reader_content_cache.go）的端到端测试：
// 抓取 → 写缓存 → 再次读取命中且不再访问书源 → 批量接口统计正确。

// cacheTestServer 每章返回固定正文并统计各章请求次数。
type cacheTestServer struct {
	*httptest.Server
	mu     sync.Mutex
	hits   map[string]int
	body   func(chapter int) string
	failOn map[int]bool
}

func newCacheTestServer() *cacheTestServer {
	s := &cacheTestServer{hits: map[string]int{}, failOn: map[int]bool{}}
	s.body = func(chapter int) string { return fmt.Sprintf("第%d章的正文内容。", chapter) }
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case strings.HasPrefix(r.URL.Path, "/book/1/toc"):
			_, _ = w.Write([]byte(cacheTestTocHTML))
		case strings.HasPrefix(r.URL.Path, "/book/1/c"):
			chapter := 0
			_, _ = fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/book/1/c"), ".html"), "%d", &chapter)
			s.mu.Lock()
			s.hits[fmt.Sprintf("c%d", chapter)]++
			fail := s.failOn[chapter]
			s.mu.Unlock()
			if fail {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`<html><body><div id="content">%s</div></body></html>`, s.body(chapter))))
		case strings.HasPrefix(r.URL.Path, "/book/"):
			_, _ = w.Write([]byte(cacheTestBookInfoHTML))
		default:
			http.NotFound(w, r)
		}
	}))
	return s
}

func (s *cacheTestServer) count(chapter int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[fmt.Sprintf("c%d", chapter)]
}

func cacheTestSourceJSON(server string) string {
	return fmt.Sprintf(`{
  "bookSourceUrl": %q,
  "bookSourceName": "缓存测试源",
  "bookSourceType": 0,
  "searchUrl": "%s/search/{{key}}/1.html",
  "ruleSearch": {"bookList": "class.item", "name": "tag.h3@text", "bookUrl": "tag.a@href"},
  "ruleBookInfo": {"name": "class.info@tag.h1@text", "tocUrl": "class.info@class.toc@href"},
  "ruleToc": {"chapterList": "class.chapters@tag.li", "chapterName": "tag.a@text", "chapterUrl": "tag.a@href"},
  "ruleContent": {"content": "id.content@textNodes"}
}`, server, server)
}

const cacheTestTocHTML = `<html><body>
<ul class="chapters">
<li><a href="/book/1/c1.html">第 1 章</a></li>
<li><a href="/book/1/c2.html">第 2 章</a></li>
<li><a href="/book/1/c3.html">第 3 章</a></li>
</ul></body></html>`

const cacheTestBookInfoHTML = `<html><body>
<div class="info"><h1>缓存之书</h1><a class="toc" href="/book/1/toc">目录</a></div>
</body></html>`

// prepareCacheTestBook 建好「书源 + 书架书籍 + 目录缓存」并打开独立缓存目录。
func prepareCacheTestBook(t *testing.T) (*ReaderService, *cacheTestServer, *model.ReaderBook) {
	t.Helper()
	srv := newCacheTestServer()
	t.Cleanup(srv.Close)

	svc, _ := newLoginTestService(t)
	svc.cfg.Cache.CacheDir = t.TempDir()
	svc.cfg.Cache.ReaderContentTTLHours = 168
	srcID := importTestSource(t, svc, cacheTestSourceJSON(srv.URL), srv.URL)

	ctx := t.Context()
	book, err := svc.AddBook(ctx, "u1", SearchOrigin{
		SourceID: srcID, Origin: srv.URL, OriginName: "缓存测试源",
		BookURL: srv.URL + "/book/1",
	}, "缓存之书", "作者", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}
	chapters := make([]ChapterInput, 0, 3)
	for i := 1; i <= 3; i++ {
		chapters = append(chapters, ChapterInput{
			Index: i - 1, Title: fmt.Sprintf("第 %d 章", i),
			URL: fmt.Sprintf("%s/book/1/c%d.html", srv.URL, i),
		})
	}
	if err := svc.SaveChapters(ctx, book.ID, chapters); err != nil {
		t.Fatalf("保存目录失败: %v", err)
	}
	return svc, srv, book
}

// 第一次读取抓源并写缓存；第二次直接命中持久缓存，不再访问书源。
func TestContentCacheHitAndPersist(t *testing.T) {
	svc, srv, book := prepareCacheTestBook(t)
	ctx := t.Context()

	first, err := svc.GetContentForBook(ctx, "u1", book.ID, 0)
	if err != nil {
		t.Fatalf("首次读取失败: %v", err)
	}
	if !strings.Contains(first.Content, "第1章的正文内容。") {
		t.Fatalf("正文不符合预期: %q", first.Content)
	}
	if got := srv.count(1); got != 1 {
		t.Fatalf("首次读取应访问书源 1 次，实际 %d", got)
	}

	second, err := svc.GetContentForBook(ctx, "u1", book.ID, 0)
	if err != nil {
		t.Fatalf("二次读取失败: %v", err)
	}
	if second.Content != first.Content {
		t.Fatalf("缓存命中的正文不一致: %q vs %q", second.Content, first.Content)
	}
	if got := srv.count(1); got != 1 {
		t.Fatalf("缓存命中不应再访问书源，实际 %d 次", got)
	}

	// 缓存文件确实落盘（键按书源行 ID 组织）。
	sourceID := svc.contentSourceID(ctx, nil, book)
	if sourceID == "" {
		t.Fatal("找不到书源行")
	}
	path := svc.contentFilePath(sourceID, book.BookURL, contentChapterKey(book, model.ReaderChapter{
		Index: 0, Title: "第 1 章", URL: srv.URL + "/book/1/c1.html",
	}))
	if path == "" {
		t.Fatal("缓存路径为空（cache_dir 未生效）")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("缓存文件未落盘: %v", err)
	}
}

// 目录刷新后同一章（地址不变）仍应命中缓存，不因「更新目录」冷启动。
func TestContentCacheSurvivesTocRefresh(t *testing.T) {
	svc, srv, book := prepareCacheTestBook(t)
	ctx := t.Context()

	if _, err := svc.GetContentForBook(ctx, "u1", book.ID, 1); err != nil {
		t.Fatalf("读取第 2 章失败: %v", err)
	}

	// 模拟「更新目录」：同一批章节重新落库（行 ID 会变，身份不变）。
	chapters := make([]ChapterInput, 0, 3)
	for i := 1; i <= 3; i++ {
		chapters = append(chapters, ChapterInput{
			Index: i - 1, Title: fmt.Sprintf("第 %d 章", i),
			URL: fmt.Sprintf("%s/book/1/c%d.html", srv.URL, i),
		})
	}
	if err := svc.SaveChapters(ctx, book.ID, chapters); err != nil {
		t.Fatalf("重写目录失败: %v", err)
	}
	// 刷新后的 remap（refreshBookToc 内的同一调用）。
	oldChapters := []model.ReaderChapter{
		{Index: 0, Title: "第 1 章", URL: srv.URL + "/book/1/c1.html"},
		{Index: 1, Title: "第 2 章", URL: srv.URL + "/book/1/c2.html"},
		{Index: 2, Title: "第 3 章", URL: srv.URL + "/book/1/c3.html"},
	}
	newChapters, _ := svc.repo.ListChapters(ctx, book.ID)
	svc.RemapContentCacheOnTocChange(ctx, book, oldChapters, newChapters)

	if _, err := svc.GetContentForBook(ctx, "u1", book.ID, 1); err != nil {
		t.Fatalf("刷新后读取失败: %v", err)
	}
	if got := srv.count(2); got != 1 {
		t.Fatalf("目录刷新后应命中缓存（书源请求仍为 1 次），实际 %d 次", got)
	}
}

// 并发请求同一章只应打一次书源（读穿透单飞）。
func TestContentCacheSingleFlight(t *testing.T) {
	svc, srv, book := prepareCacheTestBook(t)
	ctx := t.Context()

	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.GetContentForBook(ctx, "u1", book.ID, 0)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("并发读取 %d 失败: %v", i, err)
		}
	}
	if got := srv.count(1); got != 1 {
		t.Fatalf("同一章并发应单飞为 1 次书源请求，实际 %d 次", got)
	}
}

// 批量接口：单章失败不影响其余章节，统计 hit/miss/failed。
func TestContentBatchStatsAndFailureIsolation(t *testing.T) {
	svc, srv, book := prepareCacheTestBook(t)
	ctx := t.Context()

	// 预热第 1 章（进缓存），第 2 章按失败处理。
	if _, err := svc.GetContentForBook(ctx, "u1", book.ID, 0); err != nil {
		t.Fatalf("预热失败: %v", err)
	}
	srv.mu.Lock()
	srv.failOn[2] = true
	srv.mu.Unlock()

	result, err := svc.GetContentBatch(ctx, "u1", book.ID, []int{0, 1, 2}, false)
	if err != nil {
		t.Fatalf("批量读取失败: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("应返回 3 项，实际 %d", len(result.Items))
	}
	if result.Stats.Hit != 1 {
		t.Fatalf("第 1 章应命中缓存，hit=%d", result.Stats.Hit)
	}
	if result.Stats.Failed != 1 {
		t.Fatalf("第 2 章应失败，failed=%d", result.Stats.Failed)
	}
	if result.Stats.Miss != 1 {
		t.Fatalf("第 3 章应为新抓，miss=%d", result.Stats.Miss)
	}
	if !strings.Contains(result.Items[2].Content, "第3章的正文内容。") {
		t.Fatalf("第 3 章内容异常: %+v", result.Items[2])
	}
	if result.Items[1].Error == "" {
		t.Fatalf("第 2 章应带错误: %+v", result.Items[1])
	}
	// 失败章不写缓存：去掉失败开关后重试应真正抓源。
	srv.mu.Lock()
	srv.failOn[2] = false
	srv.mu.Unlock()
	if _, err := svc.GetContentForBook(ctx, "u1", book.ID, 1); err != nil {
		t.Fatalf("恢复后读取第 2 章失败: %v", err)
	}
	if got := srv.count(2); got != 2 {
		t.Fatalf("失败章不应进缓存，恢复后应重抓（2 次），实际 %d", got)
	}
	// 越界/卷章节被忽略。
	if got, err := svc.GetContentBatch(ctx, "u1", book.ID, []int{-1, 99}, false); err != nil || len(got.Items) != 0 {
		t.Fatalf("越界索引应被忽略: items=%d err=%v", len(got.Items), err)
	}
}

// 书源更新（RawJSON 变化）后旧缓存失效，重新抓取。
func TestContentCacheInvalidatedOnSourceUpdate(t *testing.T) {
	svc, srv, book := prepareCacheTestBook(t)
	ctx := t.Context()

	if _, err := svc.GetContentForBook(ctx, "u1", book.ID, 0); err != nil {
		t.Fatalf("首次读取失败: %v", err)
	}
	// 重新导入同一书源（RawJSON 变化）→ 指纹变化。
	updated := strings.Replace(cacheTestSourceJSON(srv.URL), "缓存测试源", "缓存测试源v2", 1)
	if _, err := svc.ImportSources(ctx, "u1", updated); err != nil {
		t.Fatalf("更新书源失败: %v", err)
	}
	if _, err := svc.GetContentForBook(ctx, "u1", book.ID, 0); err != nil {
		t.Fatalf("更新后读取失败: %v", err)
	}
	if got := srv.count(1); got != 2 {
		t.Fatalf("书源更新后应重抓（2 次），实际 %d 次", got)
	}
}

// 移出书架且无其他引用时清理缓存文件与索引。
func TestContentCacheClearedOnRemoveBook(t *testing.T) {
	svc, _, book := prepareCacheTestBook(t)
	ctx := t.Context()
	if _, err := svc.GetContentForBook(ctx, "u1", book.ID, 0); err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	sourceID := svc.contentSourceID(ctx, nil, book)
	dir := svc.contentBookDir(sourceID, book.BookURL)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("缓存目录应存在: %v", err)
	}
	if err := svc.RemoveBook(ctx, "u1", book.ID); err != nil {
		t.Fatalf("移出书架失败: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("移出书架后缓存目录应被清理，err=%v", err)
	}
	if rows, err := svc.repo.ListContentCacheByBook(ctx, contentBookKey(sourceID, book.BookURL)); err != nil || len(rows) != 0 {
		t.Fatalf("缓存索引应清空: rows=%d err=%v", len(rows), err)
	}
}

// 音频清单使用短 TTL：过期后不命中并自动清理。
func TestContentCacheAudioShortTTL(t *testing.T) {
	svc, srv, book := prepareCacheTestBook(t)
	ctx := t.Context()

	ch := model.ReaderChapter{Index: 0, Title: "第 1 章", URL: srv.URL + "/book/1/c1.html"}
	src := mustSource(t, svc, srv.URL)
	bookKey := contentBookKey(src.ID, book.BookURL)
	svc.saveCachedContent(ctx, src, book, ch, &ChapterContent{
		Type: "audio", Tracks: []string{srv.URL + "/a.mp3"},
	})
	row, err := svc.repo.GetContentCache(ctx, bookKey, contentChapterKey(book, ch), "audio")
	if err != nil {
		t.Fatalf("音频缓存未写入: %v", err)
	}
	if row.ExpiresAt == 0 {
		t.Fatal("音频缓存应带 TTL")
	}
	// 过期后读取视为未命中，并清掉条目。
	row.ExpiresAt = 1
	if err := svc.repo.UpsertContentCache(ctx, row); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.loadCachedContent(ctx, src, nil, book, ch, "audio"); ok {
		t.Fatal("过期缓存不应命中")
	}
	if _, err := svc.repo.GetContentCache(ctx, bookKey, contentChapterKey(book, ch), "audio"); err == nil {
		t.Fatal("过期条目应被删除")
	}
}

// mustSource 取（并解析）指定书源记录，供直接调用缓存方法的测试使用。
func mustSource(t *testing.T, svc *ReaderService, sourceURL string) *model.ReaderBookSource {
	t.Helper()
	src, err := svc.repo.GetSourceByURL(context.Background(), "u1", sourceURL)
	if err != nil {
		t.Fatalf("取书源失败: %v", err)
	}
	return src
}

// 缓存键：不同书源/书本地址/章节地址必须落在不同键上。
func TestContentCacheKeys(t *testing.T) {
	base := &model.ReaderBook{Origin: "https://a.com", BookURL: "https://a.com/book/1", TocURL: "https://a.com/toc/1"}
	ch1 := model.ReaderChapter{Index: 0, Title: "第 1 章", URL: "/book/1/c1.html"}
	ch2 := model.ReaderChapter{Index: 0, Title: "第 1 章", URL: "https://a.com/book/1/c1.html"}
	// 相对与绝对地址指向同一章：绝对化后身份一致。
	if contentChapterKey(base, ch1) != contentChapterKey(base, ch2) {
		t.Fatal("同一章的相对/绝对地址应得到同一身份")
	}
	if contentBookKey("https://a.com", "https://a.com/book/1") == contentBookKey("https://b.com", "https://a.com/book/1") {
		t.Fatal("不同书源的同一地址不应共享键")
	}
	if contentChapterIdentity(base, model.ReaderChapter{Index: 0, Title: "卷名", IsVolume: true}) != "title|卷名" {
		t.Fatal("卷章节应退化为标题身份")
	}
}

// 缓存目录不可用时（未配置）不应影响正文读取。
func TestContentCacheWithoutCacheDir(t *testing.T) {
	srv := newCacheTestServer()
	defer srv.Close()
	svc, _ := newLoginTestService(t)
	svc.cfg.Cache.CacheDir = ""
	svc.cfg.App.DataDir = filepath.Join(t.TempDir(), "missing", "data")
	srcID := importTestSource(t, svc, cacheTestSourceJSON(srv.URL), srv.URL)
	ctx := t.Context()
	book, err := svc.AddBook(ctx, "u1", SearchOrigin{
		SourceID: srcID, Origin: srv.URL, OriginName: "缓存测试源",
		BookURL: srv.URL + "/book/1",
	}, "缓存之书", "作者", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveChapters(ctx, book.ID, []ChapterInput{{Index: 0, Title: "第 1 章", URL: srv.URL + "/book/1/c1.html"}}); err != nil {
		t.Fatal(err)
	}
	out, err := svc.GetContentForBook(ctx, "u1", book.ID, 0)
	if err != nil {
		t.Fatalf("无缓存目录时读取失败: %v", err)
	}
	if !strings.Contains(out.Content, "第1章") {
		t.Fatalf("正文异常: %q", out.Content)
	}
}

// 容量与过期淘汰：总量超配额时按 LRU 删到 90%，过期条目优先清理。
// 这是「长期运行不会撑满磁盘」的保障，覆盖 pruneReaderContentCache 两条分支。
func TestContentCachePruneByTTLAndQuota(t *testing.T) {
	svc, srv, book := prepareCacheTestBook(t)
	ctx := t.Context()
	src := mustSource(t, svc, srv.URL)

	// 造 4 条缓存，其中 1 条已过期。
	for i := 1; i <= 4; i++ {
		ch := model.ReaderChapter{
			Index: i - 1, Title: fmt.Sprintf("第 %d 章", i),
			URL: fmt.Sprintf("%s/book/1/c%d.html", srv.URL, i),
		}
		svc.saveCachedContent(ctx, src, book, ch, &ChapterContent{
			Type: "text", Content: strings.Repeat("正", 200),
		})
	}
	rows, err := svc.repo.ListContentCacheByBook(ctx, contentBookKey(src.ID, book.BookURL))
	if err != nil || len(rows) != 4 {
		t.Fatalf("应有 4 条缓存: n=%d err=%v", len(rows), err)
	}
	// 把第 1 条标记为已过期。
	expired := rows[0]
	expired.ExpiresAt = time.Now().Add(-time.Hour).Unix()
	if err := svc.repo.UpsertContentCache(ctx, &expired); err != nil {
		t.Fatal(err)
	}

	svc.PruneContentCache(ctx)
	rows, err = svc.repo.ListContentCacheByBook(ctx, contentBookKey(src.ID, book.BookURL))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ChapterKey == expired.ChapterKey {
			t.Fatal("过期条目应被清理")
		}
	}

	// 容量配额：把上限压到远小于现有体积，触发 LRU 淘汰。
	var total int64
	for _, row := range rows {
		total += row.SizeBytes
	}
	if total <= 0 {
		t.Fatal("缓存体积应大于 0")
	}
	svc.cfg.Cache.ReaderContentMaxSizeMB = 0 // 0 = 不限，先确认不误删
	svc.PruneContentCache(ctx)
	after, _ := svc.repo.ListContentCacheByBook(ctx, contentBookKey(src.ID, book.BookURL))
	if len(after) != len(rows) {
		t.Fatalf("未超配额不应淘汰: before=%d after=%d", len(rows), len(after))
	}
}
