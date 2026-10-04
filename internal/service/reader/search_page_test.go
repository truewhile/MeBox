package reader

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 本文件：搜索分页。
//
// 搜索页只发第一页时，书源 searchUrl 里的 {{page}} 永远是 1，用户滚到底也拿不到
// 后面的结果（表现为「搜索无法翻页，只有第一页」）。所以页码必须由调用方传入并
// 原样交给 AnalyzeUrl；非法页码（0、负数）按第 1 页处理，避免拼出 page=0 的地址。

func pagedSearchSourceJSON(serverURL string) string {
	src := map[string]any{
		"bookSourceUrl":  serverURL,
		"bookSourceName": "分页源",
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

func TestSearchUsesRequestedPage(t *testing.T) {
	var gotPage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPage = r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		payload := map[string]any{"data": []map[string]string{{
			"book_name": fmt.Sprintf("第%s页的书", gotPage),
			"author":    "作者",
			"book_url":  "/book/" + gotPage,
		}}}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	_ = prepareLoginSource(t, svc, pagedSearchSourceJSON(srv.URL))

	for _, tc := range []struct {
		in   int
		want string
	}{
		{1, "1"},
		{2, "2"},
		{5, "5"},
		{0, "1"},  // 非法页码退回第 1 页
		{-3, "1"}, // 负数同理
	} {
		books, skipped, err := svc.Search(t.Context(), "关键词", nil, tc.in)
		if err != nil {
			t.Fatalf("page=%d 搜索失败: %v", tc.in, err)
		}
		if len(skipped) > 0 {
			t.Fatalf("page=%d 书源被跳过: %+v", tc.in, skipped)
		}
		if len(books) != 1 {
			t.Fatalf("page=%d 应搜到 1 条，实际 %d 条", tc.in, len(books))
		}
		if gotPage != tc.want {
			t.Fatalf("page=%d 时服务端收到的 page = %q，期望 %q", tc.in, gotPage, tc.want)
		}
		if books[0].Name != fmt.Sprintf("第%s页的书", tc.want) {
			t.Fatalf("page=%d 拿到了别的页的结果: %+v", tc.in, books[0])
		}
	}
}

// TestSearchWithoutPagePlaceholderStillWorks 书源 searchUrl 不带 {{page}} 时
// 仍应正常返回首页结果（分页能力是源自己的事，不是搜索的硬要求）。
func TestSearchWithoutPagePlaceholderStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"data":[{"book_name":"单页书","author":"作者","book_url":"/book/1"}]}`))
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	_ = prepareLoginSource(t, svc, scopeEchoSourceJSON(t, "单页源", srv.URL))

	books, _, err := svc.Search(t.Context(), "书", nil, 3)
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if len(books) != 1 || books[0].Name != "单页书" {
		t.Fatalf("不带 {{page}} 的源在第 3 页也应返回结果: %+v", books)
	}
}
