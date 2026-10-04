package reader

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 本文件：GET 请求的 query 必须被保留并重编码。
//
// 回归：executeWithState 曾拿 req.URLNoQuery 当请求目标。对 GET 来说那是**去掉
// query** 的裸路径，于是所有「参数写在 query 里」的请求（搜索关键词、分页、
// 平台筛选等）到了站点只剩路径。光遇聚合的 /search 因此收到空参数，返回
// `{"code":-1,"msg":"参数不能为空"}`，表现为「搜索无结果」。
// legado 的 GET 走 `get(urlNoQuery, encodedQuery)`，两者拼起来才是完整地址。

// queryEchoSource 构造一个把查询参数回显成 JSON 的书源。
func queryEchoSource(serverURL string) string {
	src := map[string]any{
		"bookSourceUrl":  serverURL,
		"bookSourceName": "query 回显源",
		"searchUrl":      serverURL + "/search?q={{key}}&page={{page}}",
		"ruleSearch": map[string]any{
			"bookList": "$.data",
			"name":     "$.book_name",
			"author":   "$.author",
			"bookUrl":  "$.book_url",
		},
	}
	out, _ := json.Marshal(src)
	return string(out)
}

func TestSearchSendsQueryString(t *testing.T) {
	var gotURI, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.URL.RequestURI()
		gotQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"data":[{"book_name":"宠魅","author":"安橙花","book_url":"/book/1"}]}`))
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	_ = prepareLoginSource(t, svc, queryEchoSource(srv.URL))

	books, skipped, err := svc.Search(t.Context(), "宠魅", nil, 1)
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if len(skipped) > 0 {
		t.Fatalf("书源被跳过: %+v", skipped)
	}
	if len(books) != 1 {
		t.Fatalf("应搜到 1 条，实际 %d 条", len(books))
	}
	if books[0].Name != "宠魅" || books[0].Author != "安橙花" {
		t.Fatalf("书目解析异常: %+v", books[0])
	}
	// 服务端必须真的收到 query —— 这正是之前丢掉的部分
	if gotQuery != "宠魅" {
		t.Fatalf("服务端收到的 q = %q，请求 URI = %q", gotQuery, gotURI)
	}
	if !strings.Contains(gotURI, "page=1") {
		t.Fatalf("分页参数丢失: %q", gotURI)
	}
	// 中文必须被百分号编码后才上线（原始 UTF-8 字节进请求行是非法的）
	if strings.Contains(gotURI, "宠") {
		t.Fatalf("query 未做百分号编码: %q", gotURI)
	}
}

// TestSearchResponseUsesEmptyArrays 搜索返回的列表字段必须是空切片而不是 nil。
//
// 回归：skipped 为 nil 切片时会被编码成 JSON null，前端 `skipped.length`
// 直接抛 TypeError，整页被错误边界接管（表现为「页面加载失败」）。
// handler 把这两个值原样交给 c.JSON，所以按同样的方式编码即可验证契约。
func TestSearchResponseUsesEmptyArrays(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	_ = prepareLoginSource(t, svc, queryEchoSource(srv.URL))

	books, skipped, err := svc.Search(t.Context(), "宠魅", nil, 1)
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"books": books, "skipped": skipped})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"skipped":null`) {
		t.Fatalf("skipped 被编码成 null（前端会按数组用）: %s", payload)
	}
	if strings.Contains(string(payload), `"books":null`) {
		t.Fatalf("books 被编码成 null: %s", payload)
	}
	if !strings.Contains(string(payload), `"skipped":[]`) {
		t.Fatalf("skipped 应为空数组: %s", payload)
	}
}

// TestSearchPreservesExistingPercentEncoding 已经编码好的 query 不应被二次编码。
func TestSearchPreservesExistingPercentEncoding(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	src := map[string]any{
		"bookSourceUrl":  srv.URL,
		"bookSourceName": "已编码 query 源",
		"searchUrl":      srv.URL + "/search?q=%E5%AE%A0%E9%AD%85&page={{page}}",
		"ruleSearch": map[string]any{
			"bookList": "$.data",
			"name":     "$.book_name",
			"bookUrl":  "$.book_url",
		},
	}
	out, _ := json.Marshal(src)
	_ = prepareLoginSource(t, svc, string(out))

	if _, _, err := svc.Search(t.Context(), "宠魅", nil, 1); err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if gotQuery != "宠魅" {
		t.Fatalf("已编码的 query 被破坏: %q", gotQuery)
	}
}
