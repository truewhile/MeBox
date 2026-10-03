package reader

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

// 本文件：书源宿主浏览器面板的服务层测试（browser_panel.go）。
//
// 覆盖真实书源依赖的三件事：
//  1. startBrowserAwait 登记待办 → 前端轮询到 → 回传 DOM → 阻塞解除；
//  2. http(s) 页面由服务端带书源 Cookie 抓取（浏览器里没有这些 Cookie）；
//  3. 页面里的资源地址被改写到同源代理，且签名校验拦得住伪造。

// browserPanelSourceJSON 构造一个内联书源：登录面板有「切换线路」按钮，
// 其实现与光遇聚合的 getServerSettings 同构——打开内嵌 HTML，回传 DOM 后
// 从 DOM 里解析线路写进源变量。
func browserPanelSourceJSON(t *testing.T, sourceURL string) string {
	t.Helper()
	loginJS := `function switchLine() {
  let html = '<!DOCTYPE html><html><body><span id="serverValue">线路甲</span></body></html>';
  let body = java.startBrowserAwait('data:text/html;base64,' + java.base64Encode(html), '线路设置', false).body();
  let m = body.match(/id="serverValue"\s*>\s*([^<]*?)\s*<\/span>/);
  source.setVariable(JSON.stringify({线路: m ? m[1] : ''}));
  return m ? m[1] : '';
}`
	src := map[string]any{
		"bookSourceUrl":  sourceURL,
		"bookSourceName": "浏览器面板测试源",
		"loginUrl":       loginJS,
		"loginUi":        `[{"name":"切换线路","type":"button","action":"switchLine()"}]`,
	}
	out, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// querySig 从签名地址里取出 s 参数。
func querySig(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("承载地址不合法: %q", raw)
	}
	return u.Query().Get("s")
}

// waitPending 轮询等待出现一个待办页面。
func waitPending(t *testing.T, svc *ReaderService, sourceID string) BrowserPage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pages := svc.PendingBrowserPages(readerTestUserID, sourceID)
		if len(pages) > 0 {
			return pages[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待超时：未出现待用户完成的页面")
	return BrowserPage{}
}

// TestBrowserPanelSwitchLineRoundTrip 端到端：书源阻塞 → 前端拿到页面 →
// 回传 DOM → 书源把线路写进源变量。
func TestBrowserPanelSwitchLineRoundTrip(t *testing.T) {
	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	sourceID := prepareLoginSource(t, svc, browserPanelSourceJSON(t, "https://panel.example.com"))

	type actionResult struct {
		res *LoginResult
		err error
	}
	done := make(chan actionResult, 1)
	go func() {
		res, err := svc.RunLoginAction(ctx, readerTestUserID, sourceID, "switchLine()", nil)
		done <- actionResult{res: res, err: err}
	}()

	page := waitPending(t, svc, sourceID)
	if page.Mode != browserModeWait {
		t.Fatalf("模式 = %q，期望 wait", page.Mode)
	}
	if page.Title != "线路设置" {
		t.Fatalf("标题 = %q", page.Title)
	}
	// data: 地址不应回传给前端展示（又长又没用）
	if page.TargetURL != "" {
		t.Fatalf("data: 地址不应回传: %q", page.TargetURL)
	}

	// 承载页面的签名必须有效，且内容是书源拼的那段 HTML
	snap, err := svc.VerifyBrowserPage(page.ID, querySig(t, page.PageURL))
	if err != nil {
		t.Fatalf("承载页面校验失败: %v", err)
	}
	if !strings.Contains(snap.HTML, `id="serverValue"`) {
		t.Fatalf("承载页面内容异常: %q", snap.HTML)
	}
	// 伪造签名必须被拒
	if _, err := svc.VerifyBrowserPage(page.ID, "deadbeef"); err == nil {
		t.Fatal("伪造签名不应通过校验")
	}

	// 模拟用户在页面里改选了「线路乙」后点 √：回传操作后的 DOM
	if err := svc.ResolveBrowser(page.ID, readerTestUserID,
		`<html><body><span id="serverValue">线路乙</span></body></html>`, "", false); err != nil {
		t.Fatalf("回传结果失败: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("登录动作失败: %v", r.err)
		}
		if !r.res.OK {
			t.Fatalf("动作未成功: %+v", r.res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("回传后阻塞未解除")
	}

	// 书源应已把线路写进源变量（真正切换生效）
	st := svc.newSourceState(ctx, "https://panel.example.com")
	if !strings.Contains(st.GetVariable(), "线路乙") {
		t.Fatalf("线路未写入源变量: %q", st.GetVariable())
	}
	// 待办应被清理
	if pages := svc.PendingBrowserPages(readerTestUserID, sourceID); len(pages) != 0 {
		t.Fatalf("完成后待办未清理: %+v", pages)
	}
}

// TestBrowserPanelCancelReleasesBlock 取消页面时要解除阻塞（书源走降级分支）。
func TestBrowserPanelCancelReleasesBlock(t *testing.T) {
	svc, _ := newLoginTestService(t)
	sourceID := prepareLoginSource(t, svc, browserPanelSourceJSON(t, "https://panel.example.com"))

	done := make(chan error, 1)
	go func() {
		// action 为空表示执行 login()；这里用 switchLine，取消后书源会抛异常
		_, err := svc.RunLoginAction(t.Context(), readerTestUserID, sourceID, "switchLine()", nil)
		done <- err
	}()

	page := waitPending(t, svc, sourceID)
	if err := svc.ResolveBrowser(page.ID, readerTestUserID, "", "", true); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	select {
	case err := <-done:
		// 书源未 catch，异常应回到调用方；关键是「不能一直卡住」
		if err == nil {
			t.Log("取消后动作用空 body 继续执行（书源自行降级）")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后阻塞未解除")
	}
}

// TestBrowserPanelInjectsSourceCookies http(s) 页面必须由服务端带书源 Cookie
// 抓取，否则「用户后台」在浏览器里永远是未登录状态。
func TestBrowserPanelInjectsSourceCookies(t *testing.T) {
	var userCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			userCookie = r.Header.Get("Cookie")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if !strings.Contains(userCookie, "qttoken=") {
				_, _ = w.Write([]byte(`<html><body>请先登录</body></html>`))
				return
			}
			_, _ = w.Write([]byte(`<html><body><h1>我的账号</h1>` +
				`<img src="/avatar.png"><a href="/orders">订单</a>` +
				`<a href="https://other-site.net/x">站外</a></body></html>`))
		case "/avatar.png":
			_, _ = w.Write([]byte("PNGDATA"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	sourceID := prepareLoginSource(t, svc, browserPanelSourceJSON(t, srv.URL))

	// 先造出登录态（等价于书源已登录成功）
	st := svc.newSourceState(ctx, srv.URL)
	st.SetCookie(srv.URL, "qttoken=SESSION_abcdef123456")
	st.flush()

	// 直接驱动服务层：等价于书源调用 startBrowser 打开用户后台
	if err := svc.openBrowser(ctx, srv.URL, sourceID, readerTestUserID, browserCookieTarget{}, rule.BrowserTask{
		URL: srv.URL + "/user", Title: "用户后台",
	}); err != nil {
		t.Fatalf("打开用户后台失败: %v", err)
	}
	if !strings.Contains(userCookie, "qttoken=SESSION_abcdef123456") {
		t.Fatalf("抓取页面时未带上书源 Cookie: %q", userCookie)
	}

	page := waitPending(t, svc, sourceID)
	if page.Mode != browserModeOpen {
		t.Fatalf("模式 = %q，期望 open", page.Mode)
	}
	if page.TargetURL != srv.URL+"/user" {
		t.Fatalf("原始地址 = %q", page.TargetURL)
	}
	snap, err := svc.VerifyBrowserPage(page.ID, querySig(t, page.PageURL))
	if err != nil {
		t.Fatal(err)
	}
	// 已登录内容（证明用的是服务端 Cookie 抓到的页面）
	if !strings.Contains(snap.HTML, "我的账号") {
		t.Fatalf("页面不是登录态内容: %q", snap.HTML)
	}
	// 资源地址应被改写到同源代理；站外链接保留原样
	if !strings.Contains(snap.HTML, "/api/reader/browser/asset?") {
		t.Fatalf("资源地址未改写: %q", snap.HTML)
	}
	if !strings.Contains(snap.HTML, "https://other-site.net/x") {
		t.Fatalf("站外链接不应改写: %q", snap.HTML)
	}
	// 注入脚本要带齐三样：DOM 回传、请求代理、以及书源登录态
	if !strings.Contains(snap.HTML, "data-mebox-browser-bridge") {
		t.Fatalf("缺少注入脚本: %q", snap.HTML)
	}
	if !strings.Contains(snap.HTML, "__mebox_proxy__") {
		t.Fatal("注入脚本缺少请求代理（页面自己的 fetch/XHR 会因不透明源被 CORS 拦）")
	}
	// 页面常用 document.cookie 判断登录态；真实 Cookie 在服务端，
	// 不预置的话「用户后台」会以为未登录并把浏览器导去 /login
	if !strings.Contains(snap.HTML, "qttoken=SESSION_abcdef123456") {
		t.Fatalf("注入脚本未预置书源 Cookie: %q", snap.HTML)
	}
	// 相对地址要按书源页面地址解析（沙箱里基地址不是承载地址）
	if !strings.Contains(snap.HTML, srv.URL) {
		t.Fatalf("注入脚本未带上页面基地址: %q", snap.HTML)
	}

	// 资源代理要能校验签名并把 Cookie 带上
	assetURL := extractAssetURL(t, snap.HTML)
	u, err := url.Parse(assetURL)
	if err != nil {
		t.Fatal(err)
	}
	target, gotSourceURL, err := svc.VerifyBrowserAsset(
		u.Query().Get("id"), u.Query().Get("u"), u.Query().Get("s"))
	if err != nil {
		t.Fatalf("资源签名校验失败: %v", err)
	}
	if !strings.HasSuffix(target, "/avatar.png") {
		t.Fatalf("资源目标 = %q", target)
	}
	if gotSourceURL != srv.URL {
		t.Fatalf("资源关联的书源 = %q", gotSourceURL)
	}
	contentType, status, data, err := svc.FetchBrowserAsset(ctx, page.ID, target)
	if err != nil {
		t.Fatalf("拉取资源失败: %v", err)
	}
	if status != http.StatusOK || string(data) != "PNGDATA" {
		t.Fatalf("资源内容异常: status=%d body=%q", status, data)
	}
	if contentType == "" {
		t.Fatal("资源缺少 Content-Type")
	}
	// 伪造资源签名必须被拒
	if _, _, err := svc.VerifyBrowserAsset(u.Query().Get("id"), u.Query().Get("u"), "deadbeef"); err == nil {
		t.Fatal("伪造资源签名不应通过校验")
	}
}

// TestBrowserPanelXHRProxy 页面内的接口请求要由服务端代发并补上书源 Cookie。
// 「用户后台」这类页面靠接口取数，iframe 自己的 XHR 带不上 Cookie 也会被 CORS 拦。
func TestBrowserPanelXHRProxy(t *testing.T) {
	var apiCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head></head><body>后台</body></html>`))
		case "/api/me":
			apiCookie = r.Header.Get("Cookie")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if !strings.Contains(apiCookie, "qttoken=") {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"未登录"}`))
				return
			}
			_, _ = w.Write([]byte(`{"nickname":"tester","vip":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	sourceID := prepareLoginSource(t, svc, browserPanelSourceJSON(t, srv.URL))
	st := svc.newSourceState(ctx, srv.URL)
	st.SetCookie(srv.URL, "qttoken=SESSION_abcdef123456")
	st.flush()

	if err := svc.openBrowser(ctx, srv.URL, sourceID, readerTestUserID, browserCookieTarget{}, rule.BrowserTask{
		URL: srv.URL + "/user", Title: "用户后台",
	}); err != nil {
		t.Fatalf("打开用户后台失败: %v", err)
	}
	page := waitPending(t, svc, sourceID)

	res, err := svc.ProxyBrowserXHR(ctx, page.ID, http.MethodGet, srv.URL+"/api/me",
		map[string]string{"X-From-Page": "1"}, "")
	if err != nil {
		t.Fatalf("转发请求失败: %v", err)
	}
	if res.Status != http.StatusOK {
		t.Fatalf("状态 = %d body=%q", res.Status, res.Body)
	}
	if !strings.Contains(apiCookie, "qttoken=SESSION_abcdef123456") {
		t.Fatalf("转发时未带上书源 Cookie: %q", apiCookie)
	}
	if !strings.Contains(res.Body, `"nickname":"tester"`) {
		t.Fatalf("响应体异常: %q", res.Body)
	}
	if !strings.Contains(res.ContentType, "json") {
		t.Fatalf("内容类型 = %q", res.ContentType)
	}
	if res.Base64 {
		t.Fatal("JSON 响应不应走 base64")
	}

	// 未知待办：拒绝（链接已过期）
	if _, err := svc.ProxyBrowserXHR(ctx, "nope", http.MethodGet, srv.URL+"/api/me", nil, ""); err == nil {
		t.Fatal("未知待办应报错")
	}
	// 非 http(s)：拒绝，避免被当成任意协议跳板
	if _, err := svc.ProxyBrowserXHR(ctx, page.ID, http.MethodGet, "file:///etc/passwd", nil, ""); err == nil {
		t.Fatal("非 http(s) 地址应报错")
	}
}

// extractAssetURL 从改写后的页面里取出第一个资源代理地址。
func extractAssetURL(t *testing.T, html string) string {
	t.Helper()
	idx := strings.Index(html, "/api/reader/browser/asset?")
	if idx < 0 {
		t.Fatalf("页面里没有资源代理地址: %q", html)
	}
	rest := html[idx:]
	// 到引号/尖括号为止
	end := strings.IndexAny(rest, `"'< `)
	if end < 0 {
		end = len(rest)
	}
	return strings.ReplaceAll(rest[:end], "&amp;", "&")
}

// TestBrowserPanelUnknownSourceIsolated 待办按书源隔离，避免串台。
func TestBrowserPanelUnknownSourceIsolated(t *testing.T) {
	svc, _ := newLoginTestService(t)
	sourceID := prepareLoginSource(t, svc, browserPanelSourceJSON(t, "https://panel.example.com"))

	done := make(chan struct{}, 1)
	go func() {
		_, _ = svc.RunLoginAction(t.Context(), readerTestUserID, sourceID, "switchLine()", nil)
		done <- struct{}{}
	}()
	page := waitPending(t, svc, sourceID)

	if pages := svc.PendingBrowserPages(readerTestUserID, "other-source"); len(pages) != 0 {
		t.Fatalf("其它书源不应看到待办: %+v", pages)
	}
	// 收尾，避免 goroutine 挂到超时
	_ = svc.ResolveBrowser(page.ID, readerTestUserID, "<span id=\"serverValue\">x</span>", "", false)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("未解除阻塞")
	}
}

// TestBrowserPanelOpenPageDroppedAfterResolve open 模式（java.startBrowser）
// 的页面在用户关闭后必须立刻离开待办表。
//
// 回归：这条日志只删除 wait 模式（awaitBrowser 的 defer），open 模式没有清理
// 路径，会一直躺到 15 分钟 TTL 到期。前端轮询到它就又把旧页面弹出来——光遇
// 聚合点过「❇️ 更新书源」后，接下来点「书源设置」「段评设置」都会跳到书源更新页。
func TestBrowserPanelOpenPageDroppedAfterResolve(t *testing.T) {
	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	sourceID := prepareLoginSource(t, svc, browserPanelSourceJSON(t, "https://panel.example.com"))

	// 等价于书源的 renderVersionPage()：java.startBrowser(data:..., '光遇书源更新')
	if err := svc.openBrowser(ctx, "https://panel.example.com", sourceID, readerTestUserID, browserCookieTarget{},
		rule.BrowserTask{URL: "data:text/html,<html><body>更新</body></html>", Title: "光遇书源更新"}); err != nil {
		t.Fatalf("登记 open 页面失败: %v", err)
	}
	old := waitPending(t, svc, sourceID)
	if old.Mode != browserModeOpen {
		t.Fatalf("模式 = %q，期望 open", old.Mode)
	}

	// 用户关掉页面
	if err := svc.ResolveBrowser(old.ID, readerTestUserID, "", "", true); err != nil {
		t.Fatalf("关闭页面失败: %v", err)
	}
	if pages := svc.PendingBrowserPages(readerTestUserID, sourceID); len(pages) != 0 {
		t.Fatalf("关闭后待办未清理: %+v", pages)
	}

	// 再点别的按钮：待办表里只应剩新页面，且序号更大
	if err := svc.openBrowser(ctx, "https://panel.example.com", sourceID, readerTestUserID, browserCookieTarget{},
		rule.BrowserTask{URL: "data:text/html,<html><body>设置</body></html>", Title: "光遇书源设置"}); err != nil {
		t.Fatalf("登记新页面失败: %v", err)
	}
	pages := svc.PendingBrowserPages(readerTestUserID, sourceID)
	if len(pages) != 1 || pages[0].Title != "光遇书源设置" {
		t.Fatalf("待办表应只剩新页面: %+v", pages)
	}
	if pages[0].Seq <= old.Seq {
		t.Fatalf("新页面 seq 应更大: %d <= %d", pages[0].Seq, old.Seq)
	}
}
