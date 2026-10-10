package reader

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// TestGetTocFollowsNextTocUrl 长书目录按 offset/limit 分页时必须翻页取全。
// 这是「目录少章」的根因：nextTocUrl 没实现时只会拿到第一页
// （实测某漫画站点 total=399/limit=100，MeBox 只得到 100 章）。
func TestGetTocFollowsNextTocUrl(t *testing.T) {
	pages := map[string]string{
		"0": `{"count":4,"list":[{"name":"第1话","url":"/c/1"},{"name":"第2话","url":"/c/2"}]}`,
		"2": `{"count":4,"list":[{"name":"第3话","url":"/c/3"},{"name":"第4话","url":"/c/4"}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		body, ok := pages[offset]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	defer srv.Close()

	// 与真实书源（拷贝漫画）一致的写法：用 baseUrl 拼下一页地址
	nextTocURL := `<js>baseUrl.indexOf('offset=0') >= 0 ? baseUrl.replace('offset=0','offset=2') : ''</js>`
	header := ""
	src := &model.ReaderBookSource{Name: "分页目录测试源", SourceURL: srv.URL}
	bs := &BookSource{
		BookSourceURL: srv.URL,
		Header:        &header,
		RuleToc: &TocRule{
			ChapterList: strPtr("$.list[*]"),
			ChapterName: strPtr("$.name"),
			ChapterURL:  strPtr("$.url"),
			NextTocURL:  strPtr(nextTocURL),
		},
	}

	svc, _ := newLoginTestService(t)
	tocURL := srv.URL + "/toc?limit=2&offset=0"
	chapters, _, err := svc.getTocFrom(t.Context(), src, bs, srv.URL+"/book/1", tocURL, nil)
	if err != nil {
		t.Fatalf("取目录失败: %v", err)
	}
	if len(chapters) != 4 {
		t.Fatalf("章节数 = %d，期望 4（第二页没有被抓取）", len(chapters))
	}
	for i, want := range []string{"第1话", "第2话", "第3话", "第4话"} {
		if chapters[i].Title != want {
			t.Errorf("章节[%d] = %q，期望 %q", i, chapters[i].Title, want)
		}
		if chapters[i].Index != i {
			t.Errorf("章节[%d] 的 Index = %d，应按翻页顺序重排", i, chapters[i].Index)
		}
	}
}

// TestGetTocNextTocUrlListsAllPagesFromFirstPage 书源只在第一页能算出完整后续页列表
// （拷贝漫画的写法：规则锚定 offset=0，第二页上算出来还是同一批地址）。
// 这种源必须把所有后续页一次性入队，否则只翻一页就停。
func TestGetTocNextTocUrlListsAllPagesFromFirstPage(t *testing.T) {
	pages := map[string]string{
		"0":   `{"total":4,"list":[{"name":"第1话","url":"/c/1"},{"name":"第2话","url":"/c/2"}]}`,
		"100": `{"total":4,"list":[{"name":"第3话","url":"/c/3"},{"name":"第4话","url":"/c/4"}]}`,
	}
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		hits = append(hits, offset)
		body, ok := pages[offset]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"total":4,"list":[]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	defer srv.Close()

	// 与真实书源一致：一次列出全部后续页，且锚点写死 offset=0（在第二页上等于原地踏步）
	nextTocURL := `<js>
list=[];
for(i=0;i<=1;i++){list.push(baseUrl.replace('offset=0','offset='+100*i))}
list
</js>`
	header := ""
	src := &model.ReaderBookSource{Name: "全量下一页测试源", SourceURL: srv.URL}
	bs := &BookSource{
		BookSourceURL: srv.URL,
		Header:        &header,
		RuleToc: &TocRule{
			ChapterList: strPtr("$.list[*]"),
			ChapterName: strPtr("$.name"),
			ChapterURL:  strPtr("$.url"),
			NextTocURL:  strPtr(nextTocURL),
		},
	}

	svc, _ := newLoginTestService(t)
	chapters, _, err := svc.getTocFrom(t.Context(), src, bs, srv.URL+"/book/1", srv.URL+"/toc?limit=2&offset=0", nil)
	if err != nil {
		t.Fatalf("取目录失败: %v", err)
	}
	if len(chapters) != 4 {
		t.Fatalf("章节数 = %d，期望 4（第一页列出的后续页没有被全部抓取）", len(chapters))
	}
	// 每页只抓一次（队列去重）
	if len(hits) != 2 {
		t.Fatalf("上游请求次数 = %d（%v），期望 2", len(hits), hits)
	}
}

// TestGetTocNextTocUrlSelfReferenceStops nextTocUrl 指回当前页时必须停下，
// 不能靠「规则永远给下一页」把自己转死。
func TestGetTocNextTocUrlSelfReferenceStops(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"list":[{"name":"第1话","url":"/c/1"}]}`)
	}))
	defer srv.Close()

	// 永远返回同一页地址（用 baseUrl 原样返回）
	nextTocURL := `<js>baseUrl</js>`
	header := ""
	src := &model.ReaderBookSource{Name: "自引用目录测试源", SourceURL: srv.URL}
	bs := &BookSource{
		BookSourceURL: srv.URL,
		Header:        &header,
		RuleToc: &TocRule{
			ChapterList: strPtr("$.list[*]"),
			ChapterName: strPtr("$.name"),
			ChapterURL:  strPtr("$.url"),
			NextTocURL:  strPtr(nextTocURL),
		},
	}

	svc, _ := newLoginTestService(t)
	chapters, _, err := svc.getTocFrom(t.Context(), src, bs, srv.URL+"/book/1", srv.URL+"/toc", nil)
	if err != nil {
		t.Fatalf("取目录失败: %v", err)
	}
	if len(chapters) != 1 {
		t.Fatalf("章节数 = %d，期望 1", len(chapters))
	}
	if hits != 1 {
		t.Fatalf("上游被请求 %d 次，自引用时应只请求 1 次", hits)
	}
}
