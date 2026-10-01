package rule

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件：书源宿主浏览器（java.startBrowser / startBrowserAwait）的运行时测试。
//
// 这些用例对应真实聚合类书源的「切换线路」「用户后台」路径：
// 书源把页面交给宿主，宿主回传用户操作后的页面源码，书源再从 DOM 里解析结果。

// fakeBrowserHost 记录宿主调用并按用例给定的函数回传结果。
type fakeBrowserHost struct {
	mu         sync.Mutex
	awaitCalls []BrowserTask
	openCalls  []BrowserTask
	respond    func(req BrowserTask) (BrowserResult, error)
	openErr    error
}

func (f *fakeBrowserHost) AwaitBrowser(_ context.Context, req BrowserTask) (BrowserResult, error) {
	f.mu.Lock()
	f.awaitCalls = append(f.awaitCalls, req)
	respond := f.respond
	f.mu.Unlock()
	if respond == nil {
		return BrowserResult{URL: req.URL, Body: ""}, nil
	}
	return respond(req)
}

func (f *fakeBrowserHost) OpenBrowser(_ context.Context, req BrowserTask) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openCalls = append(f.openCalls, req)
	return f.openErr
}

// newBrowserRunner 用聚合源固件构建运行时，并注入宿主浏览器。
func newBrowserRunner(t *testing.T, state SourceState, host BrowserHost, timeout time.Duration) *JSRunner {
	t.Helper()
	props := sampleSourceProps(t)
	r := NewJSRunner(JSConfig{
		SourceProps: props,
		JSLib:       props["jsLib"].(string),
		State:       state,
		BaseURL:     "https://v1.example-aggregate.com",
		Browser:     host,
		Timeout:     timeout,
		Fetch: func(req *Request) (string, string, int, error) {
			return `{}`, req.URL, 200, nil
		},
	})
	if err := r.JSLibErr(); err != nil {
		t.Fatalf("jsLib 执行失败: %v", err)
	}
	return r
}

// TestParseDataHTML 内嵌页面地址应被解码（base64 与百分号编码两种形式）。
func TestParseDataHTML(t *testing.T) {
	page := "<html><body>线路</body></html>"
	cases := map[string]string{
		"base64": "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(page)),
		"charset+base64": "data:text/html;charset=utf-8;base64," +
			base64.StdEncoding.EncodeToString([]byte(page)),
		"urlencoded": "data:text/html," + "%3Chtml%3E%3Cbody%3E%23%3C%2Fbody%3E%3C%2Fhtml%3E",
	}
	for name, raw := range cases {
		got := ParseDataHTML(raw)
		if got == "" {
			t.Fatalf("%s: 未解码出页面内容", name)
		}
	}
	if got := ParseDataHTML("https://example.com/a"); got != "" {
		t.Fatalf("非 data 地址不应解码，实际 %q", got)
	}
	if got := ParseDataHTML("data:image/png;base64,AAAA"); got != "" {
		t.Fatalf("非文本 MIME 不应解码，实际 %q", got)
	}
	// base64 带换行（书源拼接长 HTML 时常见）
	wrapped := "data:text/html;base64," + wrapBase64(base64.StdEncoding.EncodeToString([]byte(page)))
	if got := ParseDataHTML(wrapped); got != page {
		t.Fatalf("带换行的 base64 解码失败: %q", got)
	}
}

func wrapBase64(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%40 == 0 {
			b.WriteByte('\n')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TestSwitchLineViaStartBrowserAwait 核心回归：书源用 startBrowserAwait 打开
// 内嵌页面，宿主回传用户操作后的 DOM，书源据此切换线路。
func TestSwitchLineViaStartBrowserAwait(t *testing.T) {
	state := NewMemoryState()
	host := &fakeBrowserHost{}
	host.respond = func(req BrowserTask) (BrowserResult, error) {
		// 宿主应当已经把 data: 地址解码成可承载的页面源码
		if !strings.Contains(req.HTML, `id="serverValue"`) {
			t.Errorf("宿主未收到解码后的页面: %q", req.HTML)
		}
		if req.Title != "线路设置" {
			t.Errorf("标题 = %q", req.Title)
		}
		if req.Refetch {
			t.Error("第 3 个参数为 false 时不应要求重新抓取")
		}
		// 模拟用户在页面里选了 v2 线路后回传的 DOM
		return BrowserResult{
			URL:  req.URL,
			Body: `<html><body><span id="serverValue">https://v2.example-aggregate.com</span></body></html>`,
		}, nil
	}
	r := newBrowserRunner(t, state, host, 5*time.Second)
	loginJS := loadTestdata(t, "sample_loginUrl.js")

	v, err := r.EvalAction(loginJS+"\nswitchLine()", nil)
	if err != nil {
		t.Fatalf("switchLine 执行失败: %v", err)
	}
	if got := anyToString(v); got != "https://v2.example-aggregate.com" {
		t.Fatalf("switchLine 返回 %q", got)
	}
	// 书源应把解析出的线路写进源变量，并让 BaseUrl 跟随
	if got := state.GetVariable(); !strings.Contains(got, "v2.example-aggregate.com") {
		t.Fatalf("线路未写入源变量: %q", got)
	}
	base, err := r.EvalAction(`BaseUrl()`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := anyToString(base); got != "https://v2.example-aggregate.com" {
		t.Fatalf("BaseUrl 未跟随线路: %q", got)
	}
	if len(host.awaitCalls) != 1 {
		t.Fatalf("宿主 await 调用次数 = %d", len(host.awaitCalls))
	}
}

// TestStartBrowserAwaitPausesJSTimeout 等待人工操作期间不应被 JS 执行超时打断。
// 宿主故意比 Timeout 慢，若看门狗没有暂停，goja 会在恢复执行时抛出超时异常。
func TestStartBrowserAwaitPausesJSTimeout(t *testing.T) {
	state := NewMemoryState()
	host := &fakeBrowserHost{}
	host.respond = func(req BrowserTask) (BrowserResult, error) {
		// 模拟用户慢慢点选：远超 Timeout
		time.Sleep(300 * time.Millisecond)
		return BrowserResult{URL: req.URL, Body: "ok"}, nil
	}
	r := newBrowserRunner(t, state, host, 80*time.Millisecond)
	loginJS := loadTestdata(t, "sample_loginUrl.js")

	if _, err := r.EvalAction(loginJS+"\nswitchLine()", nil); err != nil {
		t.Fatalf("等待人工操作期间被误判为 JS 超时: %v", err)
	}
}

// TestStartBrowserAwaitCancel 用户取消页面时应把异常抛回书源，
// 书源走自己的降级分支（提示语），而不是拿到空 body 当成成功。
func TestStartBrowserAwaitCancel(t *testing.T) {
	state := NewMemoryState()
	host := &fakeBrowserHost{}
	host.respond = func(BrowserTask) (BrowserResult, error) {
		return BrowserResult{}, ErrBrowserCancelled
	}
	r := newBrowserRunner(t, state, host, 5*time.Second)
	loginJS := loadTestdata(t, "sample_loginUrl.js")

	_, err := r.EvalAction(loginJS+"\nswitchLine()", nil)
	if err == nil {
		t.Fatal("取消后应把异常抛回书源")
	}
	if !strings.Contains(err.Error(), "取消") {
		t.Fatalf("错误信息未说明取消: %v", err)
	}
}

// TestUserBackendUsesAwait 用户后台按钮同样走 startBrowserAwait（普通 http 地址）。
func TestUserBackendUsesAwait(t *testing.T) {
	state := NewMemoryState()
	state.SetCookie("https://v1.example-aggregate.com", "qttoken=TOKEN_abcdefghijklmn")
	host := &fakeBrowserHost{}
	host.respond = func(req BrowserTask) (BrowserResult, error) {
		if !strings.HasSuffix(req.URL, "/user") {
			t.Errorf("用户后台地址 = %q", req.URL)
		}
		return BrowserResult{URL: req.URL, Body: "<html>用户后台</html>"}, nil
	}
	r := newBrowserRunner(t, state, host, 5*time.Second)
	loginJS := loadTestdata(t, "sample_loginUrl.js")

	if _, err := r.EvalAction(loginJS+"\nuser()", nil); err != nil {
		t.Fatalf("user() 执行失败: %v", err)
	}
	if len(host.awaitCalls) != 1 {
		t.Fatalf("应为用户后台打开页面，await 调用 = %d", len(host.awaitCalls))
	}
}

// TestStartBrowserDoesNotWait startBrowser 只展示，不阻塞也不回传。
func TestStartBrowserDoesNotWait(t *testing.T) {
	state := NewMemoryState()
	host := &fakeBrowserHost{}
	r := newBrowserRunner(t, state, host, 5*time.Second)

	if _, err := r.EvalAction(`java.startBrowser('https://vip.example.com', '光遇看书')`, nil); err != nil {
		t.Fatalf("startBrowser 失败: %v", err)
	}
	if len(host.openCalls) != 1 || len(host.awaitCalls) != 0 {
		t.Fatalf("startBrowser 应只展示不等待: open=%d await=%d", len(host.openCalls), len(host.awaitCalls))
	}
	if host.openCalls[0].URL != "https://vip.example.com" {
		t.Fatalf("展示地址 = %q", host.openCalls[0].URL)
	}
}

// TestShowBrowserOpensDialog showBrowser 与 startBrowser 一样是「打开即返回」。
func TestShowBrowserOpensDialog(t *testing.T) {
	state := NewMemoryState()
	host := &fakeBrowserHost{}
	r := newBrowserRunner(t, state, host, 5*time.Second)

	if _, err := r.EvalAction(`java.showBrowser('https://example.com/settings', '', '光遇书源设置', '')`, nil); err != nil {
		t.Fatalf("showBrowser 失败: %v", err)
	}
	if len(host.openCalls) != 1 {
		t.Fatalf("showBrowser 应展示页面: %d", len(host.openCalls))
	}
}

// TestEnvProbeAPIsThrow 服务端不具备的 UI 函数必须抛异常，而不是静默成功。
//
// 这条对真实书源很关键：聚合源用 checkEnv() 探测运行环境，其中
//   try { java.qread(); return "轻阅"; } catch (e) {}
// 旧实现把 qread 做成「返回 null 的空操作」，于是探测结果变成「轻阅」，
// 书源据此跳过自己的降级分支，表现为「按钮点了没反应」。
func TestEnvProbeAPIsThrow(t *testing.T) {
	r := NewJSRunner(JSConfig{State: NewMemoryState()})
	for _, name := range []string{"qread", "showReadingBrowser", "startBrowserDp", "open", "searchBook"} {
		if _, err := r.EvalAction("java."+name+"()", nil); err == nil {
			t.Fatalf("java.%s 应当抛异常（服务端无此能力）", name)
		}
	}
	// checkEnv 结构：qread 抛异常后应落到最后的 "改版"，而不是 "轻阅"
	v, err := r.EvalAction(`(function () {
		try { java.qread(); return '轻阅'; } catch (e) {}
		try { java.deviceID(); return '苹果'; } catch (e) {}
		return '改版';
	})()`, nil)
	if err != nil {
		t.Fatalf("环境探测执行失败: %v", err)
	}
	if got := anyToString(v); got != "改版" {
		t.Fatalf("环境探测 = %q，期望 改版", got)
	}
}

// TestUpLoginDataAndReLoginView 表单回填与重画信号应被显式记录。
func TestUpLoginDataAndReLoginView(t *testing.T) {
	state := NewMemoryState()
	r := NewJSRunner(JSConfig{State: state})

	if _, err := r.EvalAction(`java.upLoginData({邮箱:'user@example.com', 昵称:'tester'})`, nil); err != nil {
		t.Fatalf("upLoginData 失败: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(state.GetLoginInfo()), &m); err != nil {
		t.Fatalf("登录信息不是 JSON: %q", state.GetLoginInfo())
	}
	if m["邮箱"] != "user@example.com" || m["昵称"] != "tester" {
		t.Fatalf("表单值未回填: %v", m)
	}
	if !state.UIRefreshRequested() {
		t.Fatal("upLoginData 应请求重画表单")
	}
	// 标记是「取走即清」
	if state.UIRefreshRequested() {
		t.Fatal("重画标记应被消费")
	}
	if _, err := r.EvalAction(`java.reLoginView()`, nil); err != nil {
		t.Fatalf("reLoginView 失败: %v", err)
	}
	if !state.UIRefreshRequested() {
		t.Fatal("reLoginView 应请求重画表单")
	}
	if _, err := r.EvalAction(`java.refreshExplore()`, nil); err != nil {
		t.Fatalf("refreshExplore 失败: %v", err)
	}
	if !state.UIRefreshRequested() {
		t.Fatal("refreshExplore 应请求重画表单")
	}
}
