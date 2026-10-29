package reader

import (
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// 上游限流时详情解析不到封面，规则会把页面地址当封面回退。这类记录被存进书架后
// 会一直显示破图；打开一次详情页应当用真实封面把它修好。
func TestRepairShelfCoverReplacesFallbackURL(t *testing.T) {
	svc, _ := newLoginTestService(t)
	ctx := t.Context()

	const origin = "https://api.example.com/book"
	const bookURL = "https://api.example.com/book?bookId=1"
	const cover = "https://img.example.com/c.jpg"

	book := &model.ReaderBook{
		UserID:   "u1",
		Origin:   origin,
		BookURL:  bookURL,
		Name:     "测试书",
		CoverURL: bookURL, // 规则取空后回退的产物
	}
	if err := svc.repo.CreateBook(ctx, book); err != nil {
		t.Fatal(err)
	}

	svc.RepairShelfCover(ctx, "u1", origin, bookURL, cover)

	stored, err := svc.repo.GetBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CoverURL != cover {
		t.Fatalf("封面应修为 %q，实际 %q", cover, stored.CoverURL)
	}
}

// 已经有有效封面的书不能被动覆盖（可能是换源得到的、或用户自己挑的）。
func TestRepairShelfCoverKeepsValidCover(t *testing.T) {
	svc, _ := newLoginTestService(t)
	ctx := t.Context()

	const origin = "https://api.example.com/book"
	const bookURL = "https://api.example.com/book?bookId=1"

	book := &model.ReaderBook{
		UserID:   "u1",
		Origin:   origin,
		BookURL:  bookURL,
		Name:     "测试书",
		CoverURL: "https://img.example.com/old.jpg",
	}
	if err := svc.repo.CreateBook(ctx, book); err != nil {
		t.Fatal(err)
	}

	svc.RepairShelfCover(ctx, "u1", origin, bookURL, "https://img.example.com/new.jpg")

	stored, err := svc.repo.GetBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CoverURL != "https://img.example.com/old.jpg" {
		t.Fatalf("有效封面不应被覆盖，实际 %q", stored.CoverURL)
	}
}
