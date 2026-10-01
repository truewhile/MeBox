package rule

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// 本文件用「聚合类登录书源」的典型结构验证服务端 JS 运行时：
//
//	jsLib（公共函数库）+ loginUrl（登录逻辑）+ loginUi（表单）
//
// 与真实书源（如光遇聚合，jsLib 约 15 万字符）同构，但精简为可维护的固件。
// 固件放在 testdata/，因此这些是常驻回归测试而非一次性 spike。

func loadTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("读取固件 %s 失败: %v", name, err)
	}
	return string(b)
}

// sampleSourceProps 构造注入了 jsLib/loginUrl/loginUi 的书源属性。
func sampleSourceProps(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"bookSourceUrl":  "https://v1.example-aggregate.com",
		"bookSourceName": "样例聚合源",
		"jsLib":          loadTestdata(t, "sample_jsLib.js"),
		"loginUrl":       loadTestdata(t, "sample_loginUrl.js"),
		"loginUi":        loadTestdata(t, "sample_loginUi.json"),
	}
}

// newSampleRunner 用聚合源固件构建运行时，网络由测试桩接管。
func newSampleRunner(t *testing.T, state SourceState) *JSRunner {
	t.Helper()
	props := sampleSourceProps(t)
	r := NewJSRunner(JSConfig{
		SourceProps: props,
		JSLib:       props["jsLib"].(string),
		State:       state,
		BaseURL:     "https://v1.example-aggregate.com",
		Fetch: func(req *Request) (string, string, int, error) {
			if strings.Contains(req.URL, "/login_api") {
				return `{"code":0,"key":"TOKEN_abcdefghijklmn"}`, req.URL, 200, nil
			}
			if strings.Contains(req.URL, "/user_api") {
				return `{"id":1,"email":"user@example.com","nickname":"tester"}`, req.URL, 200, nil
			}
			return `{}`, req.URL, 200, nil
		},
	})
	if err := r.JSLibErr(); err != nil {
		t.Fatalf("jsLib 执行失败: %v", err)
	}
	return r
}

// TestJSLibProvidesHelpers jsLib 里的函数与 lexical 绑定（let hosts）应跨执行可见。
func TestJSLibProvidesHelpers(t *testing.T) {
	r := newSampleRunner(t, NewMemoryState())
	v, err := r.EvalAction(`typeof getVariable + '|' + typeof request + '|' + typeof hosts`, nil)
	if err != nil {
		t.Fatalf("EvalAction 失败: %v", err)
	}
	if got := anyToString(v); got != "function|function|object" {
		t.Fatalf("jsLib 符号不可见: %q", got)
	}
	// BaseUrl() 依赖 lexical hosts 与 source.getVariable
	v, err = r.EvalAction(`BaseUrl()`, nil)
	if err != nil {
		t.Fatalf("BaseUrl 失败: %v", err)
	}
	if got := anyToString(v); got != "https://v1.example-aggregate.com" {
		t.Fatalf("BaseUrl() = %q", got)
	}
	// login 由 loginUrl 提供，未拼 loginUrl 时不应存在
	v, _ = r.EvalAction(`typeof login`, nil)
	if anyToString(v) != "undefined" {
		t.Fatalf("login 应仅由 loginUrl 定义，当前 %q", anyToString(v))
	}
}

// TestLoginFlowWritesCookies login() 应请求登录接口、写入 Cookie，且 getToken 能读回。
func TestLoginFlowWritesCookies(t *testing.T) {
	state := NewMemoryState()
	r := newSampleRunner(t, state)
	loginJS := loadTestdata(t, "sample_loginUrl.js")

	_, err := r.EvalAction(loginJS+"\nlogin(true)", map[string]any{
		"result": map[string]string{"邮箱": "user@example.com", "密码": "pw123456"},
	})
	if err != nil {
		t.Fatalf("login(true) 执行失败: %v", err)
	}
	v, err := r.EvalAction(`getToken()`, nil)
	if err != nil {
		t.Fatalf("getToken 失败: %v", err)
	}
	if got := anyToString(v); got != "TOKEN_abcdefghijklmn" {
		t.Fatalf("getToken() = %q，期望登录后能读到 token", got)
	}
	// setAllCookies 遍历 hosts；两条线路同属 example-aggregate.com，
	// 按 eTLD+1 归并成一条 Cookie，两个线路地址都应能读到。
	if len(state.Snapshot()) != 1 {
		t.Fatalf("同站线路应归并为一条 Cookie，实际 %v", state.Snapshot())
	}
	for _, host := range []string{"https://v1.example-aggregate.com", "https://v2.example-aggregate.com"} {
		if !strings.Contains(state.GetCookie(host), "qttoken=TOKEN_abcdefghijklmn") {
			t.Fatalf("%s 未读到 token: %q", host, state.GetCookie(host))
		}
	}
	if !strings.Contains(strings.Join(state.Toasts(), "\n"), "登录成功") {
		t.Fatalf("未收到登录成功提示: %v", state.Toasts())
	}
}

// TestLoginMissingCredentials 缺少账号密码时应给出提示且不产生 Cookie。
func TestLoginMissingCredentials(t *testing.T) {
	state := NewMemoryState()
	r := newSampleRunner(t, state)
	loginJS := loadTestdata(t, "sample_loginUrl.js")

	if _, err := r.EvalAction(loginJS+"\nlogin(true)", map[string]any{"result": map[string]string{}}); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if len(state.Snapshot()) != 0 {
		t.Fatalf("未填表单不应写入 Cookie: %v", state.Snapshot())
	}
	if !strings.Contains(strings.Join(state.Toasts(), "\n"), "请先输入账号密码") {
		t.Fatalf("缺少提示语: %v", state.Toasts())
	}
}

// TestStartBrowserAwaitWithoutHost startBrowserAwait 在未注入宿主浏览器时应
// 明确报错，并记录待打开地址，避免书源逻辑把空 body 当成校验成功。
func TestStartBrowserAwaitWithoutHost(t *testing.T) {
	state := NewMemoryState()
	r := newSampleRunner(t, state)
	loginJS := loadTestdata(t, "sample_loginUrl.js")

	// 先造出登录态，让 user() 走到 startBrowserAwait
	state.SetCookie("https://v1.example-aggregate.com", "qttoken=TOKEN_abcdefghijklmn")
	_, err := r.EvalAction(loginJS+"\nuser()", nil)
	if err == nil || !strings.Contains(err.Error(), "浏览器") {
		t.Fatalf("应明确报不支持，实际: %v", err)
	}
	browsers := state.Browsers()
	if len(browsers) != 1 || !strings.HasSuffix(browsers[0].URL, "/user") {
		t.Fatalf("未记录待打开地址: %+v", browsers)
	}
}

// TestSourceLoginInfoMapInit 未保存登录信息时，getLoginInfoMap 用 loginUi 的
// 非按钮字段初始化（对应 legado）。
func TestSourceLoginInfoMapInit(t *testing.T) {
	r := newSampleRunner(t, NewMemoryState())
	v, err := r.EvalAction(`JSON.stringify(source.getLoginInfoMap())`, nil)
	if err != nil {
		t.Fatalf("getLoginInfoMap 失败: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(anyToString(v)), &m); err != nil {
		t.Fatalf("返回不是 JSON 对象: %q", anyToString(v))
	}
	if _, ok := m["邮箱"]; !ok {
		t.Fatalf("loginUi 的「邮箱」未出现在登录信息: %v", m)
	}
	if _, ok := m["密码"]; !ok {
		t.Fatalf("loginUi 的「密码」未出现在登录信息: %v", m)
	}
	for k := range m {
		if strings.Contains(k, "登录") || strings.Contains(k, "注册") || strings.Contains(k, "后台") {
			t.Fatalf("button 字段不应出现在登录信息: %v", m)
		}
	}
}

// TestSourceVariablesRoundTrip 变量经 source.setVariable 写入后可由 getVariable 读出。
func TestSourceVariablesRoundTrip(t *testing.T) {
	state := NewMemoryState()
	r := newSampleRunner(t, state)

	if _, err := r.EvalAction(`setVariable('线路','https://v2.example-aggregate.com',false)`, nil); err != nil {
		t.Fatalf("setVariable 失败: %v", err)
	}
	v, err := r.EvalAction(`getVariable('线路')`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := anyToString(v); got != "https://v2.example-aggregate.com" {
		t.Fatalf("getVariable('线路') = %q", got)
	}
	// 变量变更应反映到状态存储（持久化的前提）
	if !strings.Contains(state.GetVariable(), "v2.example-aggregate.com") {
		t.Fatalf("变量未写回状态存储: %q", state.GetVariable())
	}
	// BaseUrl 也应跟随变量变化
	v, _ = r.EvalAction(`BaseUrl()`, nil)
	if anyToString(v) != "https://v2.example-aggregate.com" {
		t.Fatalf("BaseUrl 未跟随变量: %q", anyToString(v))
	}
}

// TestCookieObjectIsolation Cookie 按站点隔离，同站子域共享，removeCookie 可清除。
func TestCookieObjectIsolation(t *testing.T) {
	state := NewMemoryState()
	r := NewJSRunner(JSConfig{State: state})

	if _, err := r.EvalAction(`cookie.setCookie('https://a.example.com/x','t=1; u=2')`, nil); err != nil {
		t.Fatal(err)
	}
	v, err := r.EvalAction(`cookie.getCookie('https://a.example.com/y','t')`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if anyToString(v) != "1" {
		t.Fatalf("getCookie key = %q", anyToString(v))
	}
	// 同站子域共享 Cookie（对应 legado getSubDomain 取 eTLD+1）
	v, _ = r.EvalAction(`cookie.getCookie('https://www.example.com/')`, nil)
	if anyToString(v) == "" {
		t.Fatal("同站子域应共享 Cookie")
	}
	// 不同站点互不污染
	v, _ = r.EvalAction(`cookie.getCookie('https://other-site.net/')`, nil)
	if anyToString(v) != "" {
		t.Fatalf("跨站点读到了 Cookie: %q", anyToString(v))
	}
	// removeCookie 后读不到
	if _, err := r.EvalAction(`cookie.removeCookie('https://a.example.com')`, nil); err != nil {
		t.Fatal(err)
	}
	v, _ = r.EvalAction(`cookie.getCookie('https://a.example.com/')`, nil)
	if anyToString(v) != "" {
		t.Fatalf("removeCookie 后仍读到: %q", anyToString(v))
	}
}

// TestSourceStatePersistsAcrossRunners 状态存储是跨运行时共享的：
// 模拟服务端重启（新建 runner）后登录态仍在。
func TestSourceStatePersistsAcrossRunners(t *testing.T) {
	state := NewMemoryState()
	loginJS := loadTestdata(t, "sample_loginUrl.js")

	r1 := newSampleRunner(t, state)
	if _, err := r1.EvalAction(loginJS+"\nlogin(true)", map[string]any{
		"result": map[string]string{"邮箱": "u@e.com", "密码": "pw"},
	}); err != nil {
		t.Fatal(err)
	}

	// 新运行时复用同一 state（对应从 DB 重新载入）
	r2 := newSampleRunner(t, state)
	v, err := r2.EvalAction(`getToken()`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if anyToString(v) != "TOKEN_abcdefghijklmn" {
		t.Fatalf("新运行时未读到登录态: %q", anyToString(v))
	}
	// 已登录时再次 login 应提示已登录（走 getToken 短路分支）
	if _, err := r2.EvalAction(loginJS+"\nlogin(true)", map[string]any{
		"result": map[string]string{"邮箱": "u@e.com", "密码": "pw"},
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(state.Toasts(), "\n"), "已登录") {
		t.Fatalf("已登录时应有提示: %v", state.Toasts())
	}
}

// TestLoginCheckJsReplacesBody loginCheckJs 返回新响应体时应替换原 body
// （书源借此检测会话失效并重取页面）。
func TestLoginCheckJsReplacesBody(t *testing.T) {
	r := NewJSRunner(JSConfig{State: NewMemoryState()})
	// 返回响应对象（对应 legado 要求 loginCheckJs 返回 StrResponse）
	js := `if (result.body().indexOf('未登录') >= 0) { java.toast('会话失效，重新登录'); }
	var ok = JSON.stringify({code: 0, body: result.body()});
	result.body()`
	body, changed, err := r.EvalLoginCheck(js, "未登录", 200, "https://x.com/a")
	if err != nil {
		t.Fatalf("EvalLoginCheck 失败: %v", err)
	}
	if !changed || body != "未登录" {
		t.Fatalf("body=%q changed=%v", body, changed)
	}
}

// TestLoginCheckJsReturnsString loginCheckJs 直接返回字符串时也应生效。
func TestLoginCheckJsReturnsString(t *testing.T) {
	r := NewJSRunner(JSConfig{State: NewMemoryState()})
	body, changed, err := r.EvalLoginCheck(`'已修复内容'`, "原始", 200, "https://x.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || body != "已修复内容" {
		t.Fatalf("body=%q changed=%v", body, changed)
	}
}

// TestLoginCheckJsResponseAccessors 验证 result 暴露 code()/url()/header()。
func TestLoginCheckJsResponseAccessors(t *testing.T) {
	r := NewJSRunner(JSConfig{State: NewMemoryState()})
	body, changed, err := r.EvalLoginCheck(
		`result.code() + '|' + result.url()`, "x", 403, "https://x.com/y")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || body != "403|https://x.com/y" {
		t.Fatalf("body=%q", body)
	}
}
