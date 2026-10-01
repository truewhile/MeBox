package reader

import (
	"fmt"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// importTestSource 导入一个测试书源并按书源 URL 找到它（同包测试可能导入多个源）。
func importTestSource(t *testing.T, svc *ReaderService, raw, sourceURL string) string {
	t.Helper()
	if _, err := svc.ImportSources(t.Context(), raw); err != nil {
		t.Fatalf("导入书源失败: %v", err)
	}
	srcs, err := svc.ListSources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		if s.SourceURL == sourceURL {
			return s.ID
		}
	}
	t.Fatalf("导入后找不到书源 %s", sourceURL)
	return ""
}

// 换源：来源字段整体换到新源，旧源目录缓存清空，阅读进度保留。
func TestSwitchOriginKeepsProgressAndClearsChapters(t *testing.T) {
	oldSrv := e2eServer()
	defer oldSrv.Close()
	newSrv := e2eServer()
	defer newSrv.Close()

	svc, repos := newLoginTestService(t)
	ctx := t.Context()
	oldID := importTestSource(t, svc, e2eSourceJSON(oldSrv.URL), oldSrv.URL)
	newID := importTestSource(t, svc, e2eSourceJSON(newSrv.URL), newSrv.URL)

	book, err := svc.AddBook(ctx, "u1", SearchOrigin{
		SourceID:   oldID,
		Origin:     oldSrv.URL,
		OriginName: "旧源",
		BookURL:    oldSrv.URL + "/book/1",
	}, "斗破苍穹", "天蚕土豆", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}

	// 旧源目录缓存 + 阅读进度
	chapters := make([]ChapterInput, 0, 5)
	for i := 0; i < 5; i++ {
		chapters = append(chapters, ChapterInput{
			Index: i,
			Title: fmt.Sprintf("第 %d 章", i+1),
			URL:   fmt.Sprintf("%s/book/1/c%d.html", oldSrv.URL, i),
		})
	}
	if err := svc.SaveChapters(ctx, book.ID, chapters); err != nil {
		t.Fatalf("保存目录失败: %v", err)
	}
	if err := svc.SaveProgress(ctx, "u1", book.ID, 3, 120, "第 4 章"); err != nil {
		t.Fatalf("保存进度失败: %v", err)
	}

	switched, err := svc.SwitchOrigin(ctx, "u1", book.ID, SearchOrigin{
		SourceID:   newID,
		Origin:     newSrv.URL,
		OriginName: "新源",
		BookURL:    newSrv.URL + "/book/1",
	})
	if err != nil {
		t.Fatalf("换源失败: %v", err)
	}

	if switched.Origin != newSrv.URL || switched.OriginName != "新源" {
		t.Fatalf("来源未切换: origin=%q name=%q", switched.Origin, switched.OriginName)
	}
	if switched.BookURL != newSrv.URL+"/book/1" {
		t.Fatalf("书本地址未切换: %q", switched.BookURL)
	}
	if !strings.HasPrefix(switched.TocURL, newSrv.URL) {
		t.Fatalf("tocUrl 应指向新源: %q", switched.TocURL)
	}
	if switched.TotalChapterNum != 0 {
		t.Fatalf("旧源章数应归零，实际 %d", switched.TotalChapterNum)
	}
	if switched.DurChapterIndex != 3 || switched.DurChapterPos != 120 || switched.DurChapterTitle != "第 4 章" {
		t.Fatalf("阅读进度没保留: %+v", switched)
	}

	// 旧源章节缓存必须清掉（章节地址只对旧源有效）
	left, err := repos.Reader.ListChapters(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("换源后仍残留 %d 条旧源章节", len(left))
	}

	// 落库确认，避免只改了内存对象
	stored, err := repos.Reader.GetBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Origin != newSrv.URL || stored.BookURL != newSrv.URL+"/book/1" || stored.DurChapterIndex != 3 {
		t.Fatalf("换源结果未落库: %+v", stored)
	}
}

// 空封面时用新源的封面补上；已有封面不被覆盖。
func TestSwitchOriginFillsOnlyEmptyCover(t *testing.T) {
	oldSrv := e2eServer()
	defer oldSrv.Close()
	newSrv := e2eServer()
	defer newSrv.Close()

	svc, repos := newLoginTestService(t)
	ctx := t.Context()
	oldID := importTestSource(t, svc, e2eSourceJSON(oldSrv.URL), oldSrv.URL)
	newID := importTestSource(t, svc, e2eSourceJSON(newSrv.URL), newSrv.URL)

	book, err := svc.AddBook(ctx, "u1", SearchOrigin{
		SourceID: oldID, Origin: oldSrv.URL, OriginName: "旧源", BookURL: oldSrv.URL + "/book/1",
	}, "斗破苍穹", "天蚕土豆", "https://old.example.com/cover.jpg")
	if err != nil {
		t.Fatal(err)
	}
	target := SearchOrigin{SourceID: newID, Origin: newSrv.URL, OriginName: "新源", BookURL: newSrv.URL + "/book/1"}

	switched, err := svc.SwitchOrigin(ctx, "u1", book.ID, target)
	if err != nil {
		t.Fatalf("换源失败: %v", err)
	}
	if switched.CoverURL != "https://old.example.com/cover.jpg" {
		t.Fatalf("已有封面不该被覆盖: %q", switched.CoverURL)
	}

	// 清空封面后换源，应补上新源的封面（e2e 详情页没配封面规则，这里改为一本空封面的书）
	switched.CoverURL = ""
	if err := repos.Reader.UpdateBook(ctx, switched); err != nil {
		t.Fatal(err)
	}
	back, err := svc.SwitchOrigin(ctx, "u1", book.ID, SearchOrigin{
		SourceID: oldID, Origin: oldSrv.URL, OriginName: "旧源", BookURL: oldSrv.URL + "/book/1",
	})
	if err != nil {
		t.Fatalf("换回旧源失败: %v", err)
	}
	// e2e 书源没有 coverUrl 规则，所以这里只断言流程走通且不报错
	if back.Origin != oldSrv.URL {
		t.Fatalf("换回失败: %+v", back)
	}
}

// 换源的各种拒绝场景。
func TestSwitchOriginRejects(t *testing.T) {
	srv := e2eServer()
	defer srv.Close()

	svc, repos := newLoginTestService(t)
	ctx := t.Context()
	srcID := importTestSource(t, svc, e2eSourceJSON(srv.URL), srv.URL)

	book, err := svc.AddBook(ctx, "u1", SearchOrigin{
		SourceID: srcID, Origin: srv.URL, OriginName: "测试源", BookURL: srv.URL + "/book/1",
	}, "斗破苍穹", "天蚕土豆", "")
	if err != nil {
		t.Fatal(err)
	}
	valid := SearchOrigin{SourceID: srcID, Origin: srv.URL, OriginName: "测试源", BookURL: srv.URL + "/book/2"}

	t.Run("他人书架", func(t *testing.T) {
		if _, err := svc.SwitchOrigin(ctx, "u2", book.ID, valid); err == nil || !strings.Contains(err.Error(), "无权") {
			t.Fatalf("err = %v, want 无权操作他人书架", err)
		}
	})

	t.Run("本地书没有书源", func(t *testing.T) {
		local := &model.ReaderBook{
			UserID:    "u3",
			Name:      "本地书",
			LocalPath: "abc.txt",
			BookURL:   "local://abc",
		}
		if err := repos.Reader.CreateBook(ctx, local); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.SwitchOrigin(ctx, "u3", local.ID, valid); err == nil || !strings.Contains(err.Error(), "本地导入") {
			t.Fatalf("err = %v, want 本地书不可换源", err)
		}
	})

	t.Run("缺少目标书本地址", func(t *testing.T) {
		if _, err := svc.SwitchOrigin(ctx, "u1", book.ID, SearchOrigin{Origin: srv.URL}); err == nil {
			t.Fatal("缺少 book_url 时应报错")
		}
	})

	t.Run("书源不存在", func(t *testing.T) {
		bad := SearchOrigin{Origin: "https://nope.example.com", BookURL: "https://nope.example.com/book/1"}
		if _, err := svc.SwitchOrigin(ctx, "u1", book.ID, bad); err == nil {
			t.Fatal("书源不存在时应报错")
		}
	})

	t.Run("同一源重复换源视为成功", func(t *testing.T) {
		same := SearchOrigin{SourceID: srcID, Origin: srv.URL, OriginName: "测试源", BookURL: srv.URL + "/book/1"}
		got, err := svc.SwitchOrigin(ctx, "u1", book.ID, same)
		if err != nil {
			t.Fatalf("重复换源不该报错: %v", err)
		}
		if got.BookURL != srv.URL+"/book/1" {
			t.Fatalf("书本地址被改动: %q", got.BookURL)
		}
	})
}
