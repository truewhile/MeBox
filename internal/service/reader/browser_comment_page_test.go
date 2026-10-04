package reader

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

// 本文件：段落评论翻页适配（browser_comment_page.go）的测试。
//
// 覆盖两件事：
//  1. page=N（N>1）被换成上一页响应给的 cursor；
//  2. 整页重复（接口不认 page 时的表现）时把 has_more 改成 false，页面才会收尾。

const commentParaBase = "https://cmt.example.com/para_review?item_id=X&para=1&source=QQ"

func TestCommentPagerRewritesPageToCursor(t *testing.T) {
	p := newCommentPager()

	// 第一页：原样请求，响应里的游标被记下
	target, key := p.rewriteRequest(commentParaBase + "&page=1")
	if key == "" {
		t.Fatal("段落评论接口应被识别")
	}
	if target != commentParaBase+"&page=1" {
		t.Fatalf("第一页不该改写: %q", target)
	}
	p.observeResponse(key, `{"code":0,"data":{"comments":[{"comment_id":"c1"}],"has_more":true,"next_cursor":"1500000000000"}}`)

	// 第二页：改成带 cursor 的请求，page 不再出现
	target, key2 := p.rewriteRequest(commentParaBase + "&page=2")
	if key2 != key {
		t.Fatalf("同一段落的状态键应一致: %q vs %q", key2, key)
	}
	if !strings.Contains(target, "cursor=1500000000000") || strings.Contains(target, "page=") {
		t.Fatalf("第二页应换成 cursor 请求: %q", target)
	}

	// 拿到新评论：游标前进到下一页
	p.observeResponse(key, `{"code":0,"data":{"comments":[{"comment_id":"c2"}],"has_more":true,"next_cursor":"1400000000000"}}`)
	target, _ = p.rewriteRequest(commentParaBase + "&page=3")
	if !strings.Contains(target, "cursor=1400000000000") {
		t.Fatalf("第三页应使用新的游标: %q", target)
	}

	// 重新加载（page=1）会把整轮状态清掉：没有游标就原样透传
	p.rewriteRequest(commentParaBase + "&page=1")
	target, _ = p.rewriteRequest(commentParaBase + "&page=2")
	if strings.Contains(target, "cursor=") {
		t.Fatalf("重置后不该还带着旧游标: %q", target)
	}
}

func TestCommentPagerStopsOnRepeatedPage(t *testing.T) {
	p := newCommentPager()
	_, key := p.rewriteRequest(commentParaBase + "&page=1")
	first := `{"code":0,"data":{"comments":[{"comment_id":"c1","like_count":562,"user":{"user_id":601948729051219}}],"has_more":true,"next_cursor":"1500000000000"}}`
	if got := p.observeResponse(key, first); got != first {
		t.Fatalf("第一页不该被改写: %q", got)
	}

	// 同一页又被返回一次（接口忽略 page）：改 has_more，让页面显示「没有更多了」
	repeat := `{"code":0,"data":{"comments":[{"comment_id":"c1","like_count":562,"user":{"user_id":601948729051219}}],"has_more":true,"next_cursor":"1500000000000"}}`
	got := p.observeResponse(key, repeat)
	if !strings.Contains(got, `"has_more":false`) {
		t.Fatalf("重复页应把 has_more 改成 false: %q", got)
	}
	// 只动 has_more 一处，其它字段（尤其是大整数）保持原样
	if strings.Replace(got, `"has_more":false`, `"has_more":true`, 1) != first {
		t.Fatalf("除 has_more 外不应改动响应: %q", got)
	}
	// 重复页不再推进游标
	target, _ := p.rewriteRequest(commentParaBase + "&page=2")
	if !strings.Contains(target, "cursor=1500000000000") {
		t.Fatalf("游标不该被重复页顶掉: %q", target)
	}
}

func TestCommentPagerIgnoresOtherRequests(t *testing.T) {
	p := newCommentPager()
	cases := []string{
		"https://cmt.example.com/book/detail?page=2",               // 不是评论接口
		"https://cmt.example.com/para_review?item_id=X",            // 没有 page
		"https://cmt.example.com/para_review?item_id=X&cursor=1",   // 已经在用游标
		"https://cmt.example.com/para_review?item_id=X&page=0",     // page 非法
		"https://cmt.example.com/para_review?item_id=X&sort_by=1&", // 没有 page
	}
	for _, target := range cases {
		got, key := p.rewriteRequest(target)
		if got != target || key != "" {
			t.Fatalf("不该改写的请求被处理了: %q -> %q (key=%q)", target, got, key)
		}
	}
	// 非 JSON 响应原样返回
	if got := p.observeResponse("k", "<html></html>"); got != "<html></html>" {
		t.Fatalf("非 JSON 响应被改动: %q", got)
	}
}

// TestBrowserXHRCommentPagerFollowsCursor 端到端：页面按 page 翻页，
// 代理层换成 cursor，接口给出新评论，最后一页重复时收尾。
func TestBrowserXHRCommentPagerFollowsCursor(t *testing.T) {
	svc, _ := newLoginTestService(t)

	var mu sync.Mutex
	seenCursors := []string{}
	seenPages := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<html><body>comment page</body></html>`)
		case "/para_review":
			q := r.URL.Query()
			mu.Lock()
			seenCursors = append(seenCursors, q.Get("cursor"))
			seenPages = append(seenPages, q.Get("page"))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			// 上游只认 cursor：不认识 page，靠 next_cursor 一页页往前。
			switch q.Get("cursor") {
			case "":
				_, _ = io.WriteString(w, `{"code":0,"data":{"comments":[{"comment_id":"c1"}],"has_more":true,"next_cursor":"1500000000000"}}`)
			case "1500000000000":
				_, _ = io.WriteString(w, `{"code":0,"data":{"comments":[{"comment_id":"c2"}],"has_more":true,"next_cursor":"1400000000000"}}`)
			case "1400000000000":
				_, _ = io.WriteString(w, `{"code":0,"data":{"comments":[{"comment_id":"c3"}],"has_more":false,"next_cursor":""}}`)
			default:
				// 兜底分支：上游把同一页又给了一遍（页面的 page 循环就会走到这里）
				_, _ = io.WriteString(w, `{"code":0,"data":{"comments":[{"comment_id":"c3"}],"has_more":true,"next_cursor":"1300000000000"}}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	entry, err := svc.registerBrowser(t.Context(), srv.URL, "src-1", readerTestUserID,
		browserCookieTarget{}, rule.BrowserTask{URL: srv.URL + "/page", Title: "段评"}, browserModeOpen)
	if err != nil {
		t.Fatalf("登记承载页面失败: %v", err)
	}
	call := func(page string) string {
		t.Helper()
		res, err := svc.ProxyBrowserXHR(t.Context(), entry.id, http.MethodGet,
			srv.URL+"/para_review?item_id=X&para=1&source=QQ&page="+page, nil, "")
		if err != nil {
			t.Fatalf("代理请求失败: %v", err)
		}
		return res.Body
	}

	if body := call("1"); !strings.Contains(body, `"has_more":true`) || !strings.Contains(body, "c1") {
		t.Fatalf("第一页响应异常: %q", body)
	}
	if body := call("2"); !strings.Contains(body, "c2") {
		t.Fatalf("第二页应拿到下一页评论: %q", body)
	}
	if body := call("3"); !strings.Contains(body, "c3") || !strings.Contains(body, `"has_more":false`) {
		t.Fatalf("第三页应是真正的最后一页: %q", body)
	}
	// 页面不知道已经到底，还可能再要一页：上游又把最后一页给了一遍，
	// 适配层要把 has_more 改成 false，页面才会显示「没有更多了」并停下。
	last := call("4")
	if !strings.Contains(last, `"has_more":false`) {
		t.Fatalf("整页重复时应收尾（has_more=false）: %q", last)
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Join(seenPages, ",") != "1,,,4" {
		t.Fatalf("page 参数只应出现在没有游标可用的请求上: %v", seenPages)
	}
	if strings.Join(seenCursors, ",") != ",1500000000000,1400000000000," {
		t.Fatalf("游标传递不符预期: %v", seenCursors)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(last), &env); err != nil {
		t.Fatalf("收尾后的响应不是合法 JSON: %v", err)
	}
}
