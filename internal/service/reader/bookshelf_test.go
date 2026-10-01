package reader

import (
	"fmt"
	"testing"
)

// 本文件：书架未读章数所需的「网络书籍总章数从目录缓存补齐」逻辑回归测试。
// 前端的未读章数 = total_chapter_num -（已读到第几章），所以这里保证 total_chapter_num 可靠。

func TestListBooksFillsChapterCountFromCache(t *testing.T) {
	svc, _ := newLoginTestService(t)
	ctx := t.Context()

	book, err := svc.AddBook(ctx, "u1", SearchOrigin{
		Origin:     "https://example.com",
		OriginName: "测试书源",
		BookURL:    "https://example.com/book/1",
	}, "测试书", "作者", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}
	if book.TotalChapterNum != 0 {
		t.Fatalf("刚加入书架的网文总章数 = %d，期望 0", book.TotalChapterNum)
	}

	// 目录还没缓存：书架读不到总章数，前端此时不显示未读徽标
	books, err := svc.ListBooks(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].TotalChapterNum != 0 {
		t.Fatalf("未缓存目录时书架 = %+v，期望 1 本且总章数 0", books)
	}

	// 缓存 5 章后，书架应能算出总章数
	chapters := make([]ChapterInput, 0, 5)
	for i := 0; i < 5; i++ {
		chapters = append(chapters, ChapterInput{
			Index: i,
			Title: fmt.Sprintf("第 %d 章", i+1),
			URL:   fmt.Sprintf("https://example.com/c/%d", i),
		})
	}
	if err := svc.SaveChapters(ctx, book.ID, chapters); err != nil {
		t.Fatalf("保存目录失败: %v", err)
	}

	books, err = svc.ListBooks(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if books[0].TotalChapterNum != 5 {
		t.Fatalf("缓存 5 章后总章数 = %d，期望 5", books[0].TotalChapterNum)
	}

	// 读到第 2 章后，前端用 dur_chapter_index + 1 算已读；这里确认进度按原样回读
	if err := svc.SaveProgress(ctx, "u1", book.ID, 1, 0, "第 2 章"); err != nil {
		t.Fatalf("保存进度失败: %v", err)
	}
	books, err = svc.ListBooks(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if books[0].DurChapterIndex != 1 || books[0].DurChapterTime == 0 {
		t.Fatalf("进度 = index %d / time %d，期望 index 1 且有时间戳", books[0].DurChapterIndex, books[0].DurChapterTime)
	}
}

func TestListBooksChapterCountIsPerUser(t *testing.T) {
	svc, _ := newLoginTestService(t)
	ctx := t.Context()

	book, err := svc.AddBook(ctx, "u1", SearchOrigin{
		Origin:  "https://example.com",
		BookURL: "https://example.com/book/1",
	}, "测试书", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveChapters(ctx, book.ID, []ChapterInput{{Index: 0, Title: "第 1 章", URL: "u"}}); err != nil {
		t.Fatal(err)
	}

	// 别人的书架不应该受影响，也不该拿到别人的书
	other, err := svc.ListBooks(ctx, "u2")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("u2 的书架应为空，得到 %+v", other)
	}
}

func TestWarmUpBookChaptersSkipsWhenCacheExists(t *testing.T) {
	svc, _ := newLoginTestService(t)
	ctx := t.Context()

	book, err := svc.AddBook(ctx, "u1", SearchOrigin{
		Origin:  "https://example.com",
		BookURL: "https://example.com/book/1",
	}, "测试书", "", "")
	if err != nil {
		t.Fatal(err)
	}
	chapters := []ChapterInput{
		{Index: 0, Title: "第 1 章", URL: "https://example.com/c/0"},
		{Index: 1, Title: "第 2 章", URL: "https://example.com/c/1"},
	}
	if err := svc.SaveChapters(ctx, book.ID, chapters); err != nil {
		t.Fatal(err)
	}

	// 已有目录缓存时直接返回，不联网、不覆盖缓存
	svc.WarmUpBookChapters(ctx, "u1", book)

	got, err := svc.ListChapters(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Title != "第 1 章" {
		t.Fatalf("缓存被改动: %+v", got)
	}
}

func TestWarmUpBookChaptersSkipsLocalBook(t *testing.T) {
	svc := newLocalBookService(t)
	ctx := t.Context()

	book, err := svc.ImportLocalBook(ctx, "u1", "测试小说.txt", []byte(sampleTXT))
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	// 本地书籍不应触发联网抓目录（书源为空的地址必然失败），章节保持导入时的结果
	svc.WarmUpBookChapters(ctx, "u1", book)

	got, err := svc.ListChapters(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("本地书籍章节数 = %d，期望 3", len(got))
	}
	if book.TotalChapterNum != 3 {
		t.Fatalf("本地书籍总章数 = %d，期望 3", book.TotalChapterNum)
	}
}
