package reader

import (
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// newTocFallbackBook 建一本书架记录：只存 book_url，toc_url 留空，
// 模拟聚合类书源加入书架时的形态。
func newTocFallbackBook(t *testing.T, svc *ReaderService, serverURL string) *model.ReaderBook {
	t.Helper()
	book := &model.ReaderBook{
		UserID:     "u1",
		Origin:     serverURL,
		OriginName: "测试源",
		BookURL:    serverURL + "/book/1",
		Name:       "斗破苍穹",
	}
	if err := svc.repo.CreateBook(t.Context(), book); err != nil {
		t.Fatalf("创建书籍失败: %v", err)
	}
	return book
}

// 聚合类书源（光遇聚合一类）把目录地址放在详情结果里，加书架时只存了 book_url。
// 调用方又习惯把 book_url 当目录地址兜底（前端 toc_url || book_url），于是目录规则
// 拿到的是书籍详情页 → 0 章 → 阅读页显示「目录为空」，漫画根本进不去。
//
// GetToc 必须自己补一次详情、用详情里的 tocUrl 重抓，并把地址写回书架。
func TestGetTocFallsBackToBookInfoTocURL(t *testing.T) {
	srv := e2eServer()
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	sourceID := importTestSource(t, svc, e2eSourceJSON(srv.URL), srv.URL)
	book := newTocFallbackBook(t, svc, srv.URL)

	chapters, err := svc.GetToc(ctx, "u1", sourceID, srv.URL, book.BookURL, "")
	if err != nil {
		t.Fatalf("抓目录失败: %v", err)
	}
	if len(chapters) != 2 {
		t.Fatalf("应回退到详情里的 tocUrl 抓到 2 章，实际 %d 章", len(chapters))
	}

	// 目录地址要写回书架：之后书架/阅读页/更新目录都不用再走一次详情
	stored, err := svc.repo.GetBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.TocURL, "/book/1/toc") {
		t.Fatalf("目录地址未写回: %q", stored.TocURL)
	}
}

// 前端会把空 toc_url 兜底成 book_url 传进来，这条路径同样要能拿到目录。
func TestGetTocTreatsBookURLAsMissingTocURL(t *testing.T) {
	srv := e2eServer()
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	sourceID := importTestSource(t, svc, e2eSourceJSON(srv.URL), srv.URL)
	book := newTocFallbackBook(t, svc, srv.URL)

	chapters, err := svc.GetToc(ctx, "u1", sourceID, srv.URL, book.BookURL, book.BookURL)
	if err != nil {
		t.Fatalf("抓目录失败: %v", err)
	}
	if len(chapters) != 2 {
		t.Fatalf("book_url 兜底的目录地址也应回退到详情，实际 %d 章", len(chapters))
	}
}

// 正常源（详情给了独立目录页、调用方也传了）不能被这次改动影响。
func TestGetTocKeepsGivenTocURL(t *testing.T) {
	srv := e2eServer()
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	sourceID := importTestSource(t, svc, e2eSourceJSON(srv.URL), srv.URL)
	book := newTocFallbackBook(t, svc, srv.URL)
	book.TocURL = srv.URL + "/book/1/toc.html"
	if err := svc.repo.UpdateBook(ctx, book); err != nil {
		t.Fatal(err)
	}

	chapters, err := svc.GetToc(ctx, "u1", sourceID, srv.URL, book.BookURL, book.TocURL)
	if err != nil {
		t.Fatalf("抓目录失败: %v", err)
	}
	if len(chapters) != 2 {
		t.Fatalf("应正常抓到 2 章，实际 %d 章", len(chapters))
	}
}
