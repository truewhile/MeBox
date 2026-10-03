package reader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

// tocHitCounter 统计目录地址被上游请求的次数（目录抓取去重的回归用）。
type tocHitCounter struct {
	mu   sync.Mutex
	hits map[string]int
}

func (c *tocHitCounter) add(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hits == nil {
		c.hits = map[string]int{}
	}
	c.hits[path]++
}

func (c *tocHitCounter) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[path]
}

// TestGetTocConcurrentCallsHitUpstreamOnce 同一本书的并发抓目录只打一次上游。
//
// 回归：换源、加入书架之后，服务端会预热目录（WarmUpBookChaptersAsync），阅读页在
// 章节缓存为空时又会自己抓一次 /api/reader/toc，两次几乎同时到达——实测同一份目录
// 被上游抓了两遍（3.7s 与 11.7s），章节也被写了两遍。
func TestGetTocConcurrentCallsHitUpstreamOnce(t *testing.T) {
	counter := &tocHitCounter{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case strings.HasPrefix(r.URL.Path, "/book/1/toc"):
			counter.add(r.URL.Path)
			// 抓取期间让后到的调用方一定落在同一次在飞请求上。
			time.Sleep(150 * time.Millisecond)
			_, _ = w.Write([]byte(e2eTocHTML))
		case strings.HasPrefix(r.URL.Path, "/book/"):
			_, _ = w.Write([]byte(e2eBookInfoHTML))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	sourceID := importTestSource(t, svc, e2eSourceJSON(srv.URL), srv.URL)
	book := newTocFallbackBook(t, svc, srv.URL) // BookURL = srv.URL + "/book/1"
	tocURL := srv.URL + "/book/1/toc.html"

	// 第 0 个调用方按 source_id 找源（阅读页/详情页的形态），其余按 source_url 找
	// （服务端预热传的形态）：两条入口都要落在同一次在飞请求上。
	const callers = 3
	var wg sync.WaitGroup
	chapters := make([][]TocChapter, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			id := ""
			if i == 0 {
				id = sourceID
			}
			chapters[i], errs[i] = svc.GetToc(context.Background(), "u1", id, srv.URL, book.BookURL, tocURL)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("第 %d 个调用方抓目录失败: %v", i, errs[i])
		}
		if len(chapters[i]) != 2 {
			t.Fatalf("第 %d 个调用方拿到 %d 章，期望 2", i, len(chapters[i]))
		}
	}
	if n := counter.count("/book/1/toc.html"); n != 1 {
		t.Fatalf("上游目录被请求 %d 次，期望合并成 1 次", n)
	}
}

// TestGetTocDedupeTreatsEmptyAndBookURLAsSameTarget 预热传空 toc_url、阅读页传
// book_url（前端 `toc_url || book_url` 的兜底）指的是同一份目录，必须落在同一次抓取上。
func TestGetTocDedupeTreatsEmptyAndBookURLAsSameTarget(t *testing.T) {
	counter := &tocHitCounter{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case strings.HasPrefix(r.URL.Path, "/book/1/toc"):
			counter.add(r.URL.Path)
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte(e2eTocHTML))
		case strings.HasPrefix(r.URL.Path, "/book/"):
			_, _ = w.Write([]byte(e2eBookInfoHTML))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	importTestSource(t, svc, e2eSourceJSON(srv.URL), srv.URL)
	book := newTocFallbackBook(t, svc, srv.URL) // TocURL 留空，模拟还没写回目录地址
	if book.TocURL != "" {
		t.Fatalf("前置条件不成立：测试书的 toc_url 应为空，实际 %q", book.TocURL)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, tocURL := range []string{"", book.BookURL} { // 预热形态 / 阅读页兜底形态
		wg.Add(1)
		go func(i int, tocURL string) {
			defer wg.Done()
			<-start
			_, errs[i] = svc.GetToc(context.Background(), "u1", "", srv.URL, book.BookURL, tocURL)
		}(i, tocURL)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个调用方抓目录失败: %v", i, err)
		}
	}
	if n := counter.count("/book/1/toc.html"); n != 1 {
		t.Fatalf("上游目录被请求 %d 次，期望合并成 1 次", n)
	}
}

// TestGetTocDedupeKeepsDifferentBooksSeparate 不同的书不能被合并成一次抓取：
// 单飞的 key 里必须带上书本地址。
func TestGetTocDedupeKeepsDifferentBooksSeparate(t *testing.T) {
	counter := &tocHitCounter{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case strings.HasSuffix(r.URL.Path, "/toc.html"):
			counter.add(r.URL.Path)
			time.Sleep(50 * time.Millisecond)
			_, _ = w.Write([]byte(e2eTocHTML))
		case strings.HasPrefix(r.URL.Path, "/book/"):
			_, _ = w.Write([]byte(e2eBookInfoHTML))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	sourceID := importTestSource(t, svc, e2eSourceJSON(srv.URL), srv.URL)
	book1 := newTocFallbackBook(t, svc, srv.URL) // BookURL = srv.URL + "/book/1"
	book2 := &model.ReaderBook{
		UserID:     "u1",
		Origin:     srv.URL,
		OriginName: "测试源",
		BookURL:    srv.URL + "/book/2",
		Name:       "第二本",
	}
	if err := svc.repo.CreateBook(t.Context(), book2); err != nil {
		t.Fatalf("创建书籍失败: %v", err)
	}

	targets := []struct{ bookURL, tocURL string }{
		{book1.BookURL, srv.URL + "/book/1/toc.html"},
		{book2.BookURL, srv.URL + "/book/2/toc.html"},
	}
	var wg sync.WaitGroup
	errs := make([]error, len(targets))
	start := make(chan struct{})
	for i, tg := range targets {
		wg.Add(1)
		go func(i int, tg struct{ bookURL, tocURL string }) {
			defer wg.Done()
			<-start
			_, errs[i] = svc.GetToc(context.Background(), "u1", sourceID, srv.URL, tg.bookURL, tg.tocURL)
		}(i, tg)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 本书抓目录失败: %v", i+1, err)
		}
	}
	for _, path := range []string{"/book/1/toc.html", "/book/2/toc.html"} {
		if n := counter.count(path); n != 1 {
			t.Fatalf("%s 被请求 %d 次，期望 1 次", path, n)
		}
	}
}
