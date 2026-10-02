package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/service"
	"github.com/truewhile/MeBox/internal/service/reader"
)

// 阅读进度接口的 HTTP 契约测试。
//
// 听书（音频）的进度是 audio.currentTime，天然带小数；漫画是图片序号、文本是页码，
// 都是整数。同一个接口必须同时接住这两种形态，否则音频进度会被 400 丢掉
// （前端是 fire-and-forget，失败不报错），表现为「听书退出后从头开始」。

func progressRouterForTest(cfg *service.Container, userID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, userID)
		c.Next()
	})
	registerReaderRoutes(r.Group("/api"), cfg)
	return r
}

func putProgress(t *testing.T, router *gin.Engine, bookID, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/reader/books/"+bookID+"/progress", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

func TestSaveProgressAcceptsFractionalPos(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	ctx := t.Context()

	book, err := svc.Reader.AddBook(ctx, "u1", reader.SearchOrigin{
		Origin:  "https://example.com",
		BookURL: "https://example.com/book/1",
	}, "有声书", "作者", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}

	router := progressRouterForTest(svc, "u1")
	// 37.5 秒：音频进度上报的真实形态
	w := putProgress(t, router, book.ID, `{"chapter_index":2,"pos":37.5,"chapter_title":"第 3 章"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("音频进度应被接受，实际 status=%d body=%s", w.Code, w.Body.String())
	}

	books, err := svc.Reader.ListBooks(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("书架应有 1 本，实际 %d", len(books))
	}
	// 小数秒截断成整数秒落库（dur_chapter_pos 是 int）
	if books[0].DurChapterPos != 37 {
		t.Fatalf("进度 = %d，期望 37（37.5 截断）", books[0].DurChapterPos)
	}
	if books[0].DurChapterIndex != 2 || books[0].DurChapterTitle != "第 3 章" {
		t.Fatalf("章节/标题未保存: index=%d title=%q", books[0].DurChapterIndex, books[0].DurChapterTitle)
	}
}

func TestSaveProgressAcceptsIntegerPos(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	ctx := t.Context()

	book, err := svc.Reader.AddBook(ctx, "u1", reader.SearchOrigin{
		Origin:  "https://example.com",
		BookURL: "https://example.com/book/2",
	}, "文本", "", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}

	router := progressRouterForTest(svc, "u1")
	// 文本/漫画仍是整数页码，不能被破坏
	w := putProgress(t, router, book.ID, `{"chapter_index":7,"pos":12,"chapter_title":"第 8 章"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("页码进度应被接受，实际 status=%d body=%s", w.Code, w.Body.String())
	}

	books, err := svc.Reader.ListBooks(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if books[0].DurChapterPos != 12 || books[0].DurChapterIndex != 7 {
		t.Fatalf("进度 = pos %d / index %d，期望 12 / 7", books[0].DurChapterPos, books[0].DurChapterIndex)
	}
}

func TestSaveProgressRejectsNegativePos(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	ctx := t.Context()

	book, err := svc.Reader.AddBook(ctx, "u1", reader.SearchOrigin{
		Origin:  "https://example.com",
		BookURL: "https://example.com/book/3",
	}, "文本", "", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}

	router := progressRouterForTest(svc, "u1")
	// 负数不是合法进度，落库前应被夹到 0，而不是写入负值
	w := putProgress(t, router, book.ID, `{"chapter_index":0,"pos":-5,"chapter_title":"第 1 章"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("负数进度不应 500，实际 status=%d body=%s", w.Code, w.Body.String())
	}

	books, err := svc.Reader.ListBooks(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if books[0].DurChapterPos != 0 {
		t.Fatalf("进度 = %d，期望夹到 0", books[0].DurChapterPos)
	}
}

// TestSaveProgressRoundTripsThroughJSON 防回归：进度写进去要能原样读回来。
func TestSaveProgressRoundTripsThroughJSON(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	ctx := t.Context()

	book, err := svc.Reader.AddBook(ctx, "u1", reader.SearchOrigin{
		Origin:  "https://example.com",
		BookURL: "https://example.com/book/4",
	}, "有声书", "", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}

	router := progressRouterForTest(svc, "u1")
	if w := putProgress(t, router, book.ID, `{"chapter_index":0,"pos":123.75,"chapter_title":"第 1 章"}`); w.Code != http.StatusOK {
		t.Fatalf("保存进度失败 status=%d body=%s", w.Code, w.Body.String())
	}

	// 走列表接口（前端书架/阅读器恢复进度读的就是它）
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/reader/books", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("书架列表 status=%d body=%s", w.Code, w.Body.String())
	}
	var res struct {
		Books []struct {
			DurChapterPos int `json:"dur_chapter_pos"`
		} `json:"books"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析书架响应失败: %v", err)
	}
	if len(res.Books) != 1 || res.Books[0].DurChapterPos != 123 {
		t.Fatalf("读回的进度 = %+v，期望 123", res.Books)
	}
}
