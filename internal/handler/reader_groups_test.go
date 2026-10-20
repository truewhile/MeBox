package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
	"github.com/truewhile/MeBox/internal/service/reader"
)

// 书架分组接口（/reader/book-groups）的 HTTP 契约测试。
//
// 分组仿影视模块的媒体库标签：整份替换、一本书只归一个组、按用户隔离。
// 另外两条是书籍特有的：只保留书架上的书（书移出后不留死 ID）、空分组保留。

func addBookForGroups(t *testing.T, svc *service.Container, userID, name, bookURL string) *model.ReaderBook {
	t.Helper()
	book, err := svc.Reader.AddBook(t.Context(), userID, reader.SearchOrigin{
		Origin:  "https://example.com",
		BookURL: bookURL,
	}, name, "作者", "")
	if err != nil {
		t.Fatalf("加入书架失败: %v", err)
	}
	return book
}

func setBookGroups(t *testing.T, router *gin.Engine, user string, groups []model.BookGroupSet) ([]model.BookGroupSet, int) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"groups": groups})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/reader/book-groups", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", user)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		return nil, w.Code
	}
	var res struct {
		Groups []model.BookGroupSet `json:"groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, w.Body.String())
	}
	return res.Groups, w.Code
}

func getBookGroups(t *testing.T, router *gin.Engine, user string) ([]model.BookGroupSet, int) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/reader/book-groups", nil)
	req.Header.Set("X-Test-User", user)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		return nil, w.Code
	}
	var res struct {
		Groups []model.BookGroupSet `json:"groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, w.Body.String())
	}
	return res.Groups, w.Code
}

func namesOf(groups []model.BookGroupSet) string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.Name)
	}
	return strings.Join(out, ",")
}

func idsOf(groups []model.BookGroupSet, name string) string {
	for _, g := range groups {
		if g.Name == name {
			return strings.Join(g.BookIDs, ",")
		}
	}
	return "<missing>"
}

// TestBookGroupsEmptyStateIsArray 没有分组时应返回空数组，前端可直接遍历。
func TestBookGroupsEmptyStateIsArray(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	router := profileRouterForTest(t, svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/reader/book-groups", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// 必须是 [] 而不是 null
	if !strings.Contains(w.Body.String(), `"groups":[]`) {
		t.Fatalf("空态应返回 []，实际 %s", w.Body.String())
	}
}

func TestBookGroupsNormalizeFilterAndDedupe(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	router := profileRouterForTest(t, svc)

	b1 := addBookForGroups(t, svc, "u1", "书一", "https://example.com/1")
	b2 := addBookForGroups(t, svc, "u1", "书二", "https://example.com/2")
	b3 := addBookForGroups(t, svc, "u1", "书三", "https://example.com/3")
	foreign := addBookForGroups(t, svc, "u2", "别人的书", "https://example.com/9")

	got, code := setBookGroups(t, router, "u1", []model.BookGroupSet{
		{Name: "科幻", BookIDs: []string{b1.ID, b2.ID}},
		{Name: "在读", BookIDs: []string{b1.ID}}, // b1 已被「科幻」认领 → 应变空
		{Name: "玄幻", BookIDs: []string{b3.ID}},
		{Name: "空组", BookIDs: []string{}},
		{Name: "   ", BookIDs: []string{b2.ID}},     // 空名 → 丢弃整个分组
		{Name: "外来", BookIDs: []string{foreign.ID}}, // 不属于 u1 的书架 → 过滤掉
	})
	if code != http.StatusOK {
		t.Fatalf("保存失败 status=%d", code)
	}
	if names := namesOf(got); names != "科幻,在读,玄幻,空组,外来" {
		t.Fatalf("分组顺序/数量 = %q，期望 科幻,在读,玄幻,空组,外来", names)
	}
	if ids := idsOf(got, "科幻"); ids != b1.ID+","+b2.ID {
		t.Fatalf("科幻 = %q，期望 %s,%s", ids, b1.ID, b2.ID)
	}
	if ids := idsOf(got, "在读"); ids != "" {
		t.Fatalf("在读 应为空（b1 归了科幻），实际 %q", ids)
	}
	if ids := idsOf(got, "玄幻"); ids != b3.ID {
		t.Fatalf("玄幻 = %q，期望 %s", ids, b3.ID)
	}
	if ids := idsOf(got, "外来"); ids != "" {
		t.Fatalf("外来 应被过滤为空，实际 %q", ids)
	}

	// 读回来应当一致
	loaded, code := getBookGroups(t, router, "u1")
	if code != http.StatusOK {
		t.Fatalf("读回 status=%d", code)
	}
	if names := namesOf(loaded); names != namesOf(got) {
		t.Fatalf("读回分组 = %q，期望 %q", names, namesOf(got))
	}
	if ids := idsOf(loaded, "玄幻"); ids != b3.ID {
		t.Fatalf("读回 玄幻 = %q，期望 %s", ids, b3.ID)
	}
}

// TestBookGroupsDropsBooksRemovedFromShelf 书被移出书架后，分组里不应留下死 ID。
func TestBookGroupsDropsBooksRemovedFromShelf(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	router := profileRouterForTest(t, svc)

	b1 := addBookForGroups(t, svc, "u1", "书一", "https://example.com/1")
	b2 := addBookForGroups(t, svc, "u1", "书二", "https://example.com/2")

	if _, code := setBookGroups(t, router, "u1", []model.BookGroupSet{
		{Name: "科幻", BookIDs: []string{b1.ID, b2.ID}},
	}); code != http.StatusOK {
		t.Fatalf("保存失败 status=%d", code)
	}

	if err := svc.Reader.RemoveBook(t.Context(), "u1", b2.ID); err != nil {
		t.Fatalf("移出书架失败: %v", err)
	}

	loaded, _ := getBookGroups(t, router, "u1")
	if ids := idsOf(loaded, "科幻"); ids != b1.ID {
		t.Fatalf("科幻 = %q，期望只剩 %s", ids, b1.ID)
	}
}

func TestBookGroupsArePerUser(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	router := profileRouterForTest(t, svc)

	u1Book := addBookForGroups(t, svc, "u1", "书一", "https://example.com/1")
	u2Book := addBookForGroups(t, svc, "u2", "书二", "https://example.com/2")

	if _, code := setBookGroups(t, router, "u1", []model.BookGroupSet{
		{Name: "u1 的分组", BookIDs: []string{u1Book.ID}},
	}); code != http.StatusOK {
		t.Fatalf("u1 保存失败 status=%d", code)
	}

	// u2 看不到 u1 的分组
	if groups, code := getBookGroups(t, router, "u2"); code != http.StatusOK || len(groups) != 0 {
		t.Fatalf("u2 分组 = %+v (status=%d)，期望空", groups, code)
	}

	// u2 把 u1 的书塞进自己的分组也不会生效（不属于 u2 的书架）
	if groups, code := setBookGroups(t, router, "u2", []model.BookGroupSet{
		{Name: "抢过来", BookIDs: []string{u1Book.ID, u2Book.ID}},
	}); code != http.StatusOK || idsOf(groups, "抢过来") != u2Book.ID {
		t.Fatalf("u2 分组 = %+v (status=%d)，期望只含自己的书", groups, code)
	}

	// u1 的分组不受影响
	back, _ := getBookGroups(t, router, "u1")
	if names := namesOf(back); names != "u1 的分组" || idsOf(back, "u1 的分组") != u1Book.ID {
		t.Fatalf("u1 分组被 u2 影响: %+v", back)
	}
}

// TestBookGroupsSaveTwiceReplacesWholeSet 整份替换语义：第二次保存覆盖第一次。
func TestBookGroupsSaveTwiceReplacesWholeSet(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	router := profileRouterForTest(t, svc)

	b1 := addBookForGroups(t, svc, "u1", "书一", "https://example.com/1")

	if _, code := setBookGroups(t, router, "u1", []model.BookGroupSet{
		{Name: "旧组", BookIDs: []string{b1.ID}},
	}); code != http.StatusOK {
		t.Fatalf("首次保存 status=%d", code)
	}
	got, code := setBookGroups(t, router, "u1", []model.BookGroupSet{
		{Name: "新组", BookIDs: []string{}},
	})
	if code != http.StatusOK {
		t.Fatalf("二次保存 status=%d", code)
	}
	if names := namesOf(got); names != "新组" {
		t.Fatalf("二次保存后 = %q，期望只有 新组", names)
	}

	// 清空分组
	if groups, code := setBookGroups(t, router, "u1", nil); code != http.StatusOK || len(groups) != 0 {
		t.Fatalf("清空后 = %+v (status=%d)，期望空", groups, code)
	}
	if groups, _ := getBookGroups(t, router, "u1"); len(groups) != 0 {
		t.Fatalf("清空后读回 = %+v，期望空", groups)
	}
}
