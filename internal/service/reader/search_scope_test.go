package reader

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 本文件：搜索范围的过滤逻辑（对应 legado SearchScope.getBookSourceParts）。
// 覆盖「只搜指定源」「空范围=全部」「范围失效退回全部」三种情形。

func scopeEchoSourceJSON(t *testing.T, name, serverURL string) string {
	t.Helper()
	src := map[string]any{
		"bookSourceUrl":  serverURL,
		"bookSourceName": name,
		"searchUrl":      serverURL + "/search?q={{key}}",
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

func scopeEchoServer(t *testing.T, bookName string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		payload := map[string]any{
			"data": []map[string]string{{
				"book_name": bookName,
				"author":    "作者",
				"book_url":  "/book/1",
			}},
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
}

func TestSearchRespectsSourceScope(t *testing.T) {
	srvA := scopeEchoServer(t, "甲书")
	defer srvA.Close()
	srvB := scopeEchoServer(t, "乙书")
	defer srvB.Close()

	svc, _ := newLoginTestService(t)
	for _, raw := range []string{
		scopeEchoSourceJSON(t, "源A", srvA.URL),
		scopeEchoSourceJSON(t, "源B", srvB.URL),
	} {
		if _, err := svc.ImportSources(t.Context(), "u1", raw); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := svc.ListSources(t.Context(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	idByName := map[string]string{}
	for _, s := range sources {
		idByName[s.Name] = s.ID
	}

	names := func(books []SearchBook) map[string]bool {
		out := map[string]bool{}
		for _, b := range books {
			out[b.Name] = true
		}
		return out
	}

	// 指定单个源：只应搜到该源的结果
	books, skipped, err := svc.Search(t.Context(), "u1", "书", []string{idByName["源A"]}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) > 0 {
		t.Fatalf("源A 不应失败: %+v", skipped)
	}
	if got := names(books); len(got) != 1 || !got["甲书"] {
		t.Fatalf("只搜源A 应只返回甲书，实际 %+v", books)
	}

	// 空范围：默认全部启用书源，两个源都应命中
	books, _, err = svc.Search(t.Context(), "u1", "书", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(books); len(got) != 2 || !got["甲书"] || !got["乙书"] {
		t.Fatalf("空范围应搜到两个源的结果，实际 %+v", books)
	}

	// 范围里的源已不存在（删源/换设备）时退回全部启用，而不是搜不到
	books, _, err = svc.Search(t.Context(), "u1", "书", []string{"not-a-real-source"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(books); len(got) != 2 {
		t.Fatalf("失效范围应退回全部启用，实际 %+v", books)
	}
}
