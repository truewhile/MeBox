package reader

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 本文件：详情 init 链式取值 与 书籍类型翻译 的回归测试。
//
// 背景（光遇聚合）：
//   - ruleBookInfo.init 是 `<js>…</js>$.data` 组合规则，它的结果会成为后续
//     name/author/tocUrl 的解析内容（对应 legado
//     `analyzeRule.setContent(analyzeRule.getElement(infoRule.init))`）。
//     MeBox 早期把 init 结果当成 JSON 解析 map 再按写死的键名取值，导致
//     详情页「书名/作者为空、目录 0 章」。
//   - 书源在目录规则里用 legado 的 BookType 位掩码给 book.type 赋值
//     （8=文本 32=音频 64=图片 4=视频），而 MeBox 用 0/1/2/3，必须换算，
//     否则听书源会被当成文本，正文渲染成一串裸 URL。

// TestBookInfoInitResultBecomesContent init 的结果要成为后续字段的解析内容。
func TestBookInfoInitResultBecomesContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case "/detail":
			_, _ = w.Write([]byte(`{"code":0,"data":{"book_name":"宠魅","author":"某作者","thumb_url":"https://img.example.com/c.jpg","toc_url":"https://example.com/toc"}}`))
		default:
			_, _ = w.Write([]byte(`<html><body>book page</body></html>`))
		}
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	src := map[string]any{
		"bookSourceUrl":  srv.URL,
		"bookSourceName": "init 链式测试源",
		"ruleBookInfo": map[string]any{
			// 组合规则：先跑 JS 取详情接口，再用 $.data 取出对象
			"init":     fmt.Sprintf(`<js>java.ajax(%q)</js>$.data`, srv.URL+"/detail"),
			"name":     "$.book_name",
			"author":   "$.author",
			"coverUrl": "$.thumb_url",
			"tocUrl":   "$.toc_url",
		},
	}
	out, _ := json.Marshal(src)
	sourceID := prepareLoginSource(t, svc, string(out))

	info, err := svc.GetBookInfo(t.Context(), sourceID, "", srv.URL+"/book/1")
	if err != nil {
		t.Fatalf("取详情失败: %v", err)
	}
	if info.Name != "宠魅" {
		t.Fatalf("书名 = %q（init 结果未成为解析内容）", info.Name)
	}
	if info.Author != "某作者" {
		t.Fatalf("作者 = %q", info.Author)
	}
	if info.CoverURL != "https://img.example.com/c.jpg" {
		t.Fatalf("封面 = %q", info.CoverURL)
	}
	if info.TocURL != "https://example.com/toc" {
		t.Fatalf("目录地址 = %q", info.TocURL)
	}
}

// TestNormalizeBookType 把 legado 的 BookType 位掩码换算成 MeBox 的 0/1/2/3。
func TestNormalizeBookType(t *testing.T) {
	cases := map[int]int{
		0:  0, // 未知/默认 → 文本
		8:  0, // text
		32: 1, // audio（听书源默认值）
		64: 2, // image（漫画源默认值）
		4:  3, // video（短剧源默认值）
		1:  0, // 苹果端遗留值不再是音频
		2:  0,
		// 组合位：按 音频 > 图片 > 视频 > 文本 的优先级取一个
		32 | 8:  1,
		64 | 8:  2,
		4 | 8:   3,
		32 | 64: 1,
	}
	for in, want := range cases {
		if got := normalizeBookType(in); got != want {
			t.Errorf("normalizeBookType(%d) = %d，期望 %d", in, got, want)
		}
	}
}
