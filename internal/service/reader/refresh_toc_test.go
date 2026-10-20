package reader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// 本文件：「更新目录」的结果分档回归测试。
//
// 背景：书架提示曾经把「抓到了目录但没有新章节」也算成失败。完结书每次检查都没有新章节，
// 于是书架天天报「N 本失败」，看起来像书源坏了。现在必须区分三档：
// 抓到新章节 / 抓到但没变（已是最新）/ 抓取或写入失败。

// refreshTocForTest 跑一次「更新目录」并返回汇总。
func refreshTocForTest(t *testing.T, svc *ReaderService, ctx context.Context) *TocRefreshResult {
	t.Helper()
	res, err := svc.RefreshBooksToc(ctx, "u1")
	if err != nil {
		t.Fatalf("更新目录失败: %v", err)
	}
	return res
}

// newRefreshTocServer 建一个目录页内容可切换的测试源站点。
func newRefreshTocServer(tocHandler *http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case strings.HasPrefix(r.URL.Path, "/book/1/toc"):
			(*tocHandler)(w, r)
		case strings.HasPrefix(r.URL.Path, "/book/"):
			_, _ = w.Write([]byte(e2eBookInfoHTML))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestRefreshBooksTocSeparatesUnchangedFromFailed(t *testing.T) {
	tocHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(e2eTocHTML))
	})
	srv := newRefreshTocServer(&tocHandler)
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	importTestSource(t, svc, e2eSourceJSON(srv.URL), srv.URL)

	book := &model.ReaderBook{
		UserID:     "u1",
		Origin:     srv.URL,
		OriginName: "测试源",
		BookURL:    srv.URL + "/book/1",
		TocURL:     srv.URL + "/book/1/toc.html",
		Name:       "斗破苍穹",
	}
	if err := svc.repo.CreateBook(ctx, book); err != nil {
		t.Fatalf("创建书籍失败: %v", err)
	}

	// 1) 首次刷新：抓到 2 章并落库。书架原先没有目录缓存，没有可比基准，算「已是最新」。
	res := refreshTocForTest(t, svc, ctx)
	if res.Total != 1 || res.Updated != 0 || res.Unchanged != 1 || res.Failed != 0 {
		t.Fatalf("首次刷新 = %+v，期望 total 1 / updated 0 / unchanged 1 / failed 0", res)
	}
	chapters, err := svc.ListChapters(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 2 {
		t.Fatalf("目录缓存 = %d 章，期望 2", len(chapters))
	}

	// 2) 站点多出一章：这才算「有新章节」。
	tocHandler = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Replace(
			e2eTocHTML, "</ul>", `<li><a href="/book/1/c3.html">第三章 药老</a></li></ul>`, 1,
		)))
	}
	res = refreshTocForTest(t, svc, ctx)
	if res.Updated != 1 || res.Unchanged != 0 || res.Failed != 0 {
		t.Fatalf("出现新章节时 = %+v，期望 updated 1 / unchanged 0 / failed 0", res)
	}

	// 3) 站点返回风控响应体（HTTP 200 但不是目录）：规则解析成空列表且不报错，
	//    这种静默失败必须记失败，不能报成「已是最新」。
	tocHandler = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":210,"message":"請到官網更新最新APP，請下載安裝正版之後等待1小時。"}`))
	}
	res = refreshTocForTest(t, svc, ctx)
	if res.Failed != 1 || res.Unchanged != 0 || res.Updated != 0 {
		t.Fatalf("风控响应体 = %+v，期望 updated 0 / unchanged 0 / failed 1", res)
	}
	// 抓失败不能动已有缓存，否则阅读器会连本来能看的章节都读不到。
	chapters, err = svc.ListChapters(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 3 {
		t.Fatalf("抓失败后目录缓存 = %d 章，期望保持 3 章", len(chapters))
	}

	// 4) 站点 404：抓取失败。
	tocHandler = func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
	res = refreshTocForTest(t, svc, ctx)
	if res.Failed != 1 || res.Unchanged != 0 {
		t.Fatalf("站点 404 = %+v，期望 unchanged 0 / failed 1", res)
	}
}

// 本地书籍与没有书源信息的书籍不参与刷新，汇总里也不该出现。
func TestRefreshBooksTocSkipsLocalAndSourcelessBooks(t *testing.T) {
	svc, _ := newLoginTestService(t)
	ctx := t.Context()

	local := &model.ReaderBook{UserID: "u1", Name: "本地书", LocalPath: "/tmp/a.txt", BookURL: "x", Origin: "y"}
	sourceless := &model.ReaderBook{UserID: "u1", Name: "无书源", BookURL: "", Origin: ""}
	for _, b := range []*model.ReaderBook{local, sourceless} {
		if err := svc.repo.CreateBook(ctx, b); err != nil {
			t.Fatalf("创建书籍失败: %v", err)
		}
	}

	res := refreshTocForTest(t, svc, ctx)
	if res.Total != 0 || res.Updated != 0 || res.Unchanged != 0 || res.Failed != 0 {
		t.Fatalf("跳过本地/无源书籍时 = %+v，期望全 0", res)
	}
}
