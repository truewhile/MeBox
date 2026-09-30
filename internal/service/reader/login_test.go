package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/database"
	"github.com/truewhile/MeBox/internal/repository"
)

// 本文件：登录类书源的服务层链路测试。
// 覆盖「登录 → Cookie 落库 → 后续请求自动携带 Cookie → 登出清理」。

// loginTestServer 模拟一个需要登录的书源站点：
//   - POST /login_api 校验账号密码并下发会话 Cookie
//   - GET  /search     读取 Cookie，无 Cookie 返回 401（模拟登录后才能搜索）
func loginTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login_api":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["register_email"] != "user@example.com" || body["password"] != "pw123456" {
				_, _ = w.Write([]byte(`{"code":1,"msg":"账号或密码错误"}`))
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "qttoken", Value: "SESSION_abcdef123456", Path: "/"})
			_, _ = w.Write([]byte(`{"code":0,"key":"SESSION_abcdef123456"}`))
		case r.URL.Path == "/search":
			if !strings.Contains(r.Header.Get("Cookie"), "qttoken=SESSION_abcdef123456") {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`<html><body>未登录</body></html>`))
				return
			}
			_, _ = w.Write([]byte(`<html><body>
<div class="item"><h3><a href="/book/9">会员专享书</a></h3><span class="author">作者</span></div>
</body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func loginTestSourceJSON(t *testing.T, server string) string {
	t.Helper()
	// loginUrl 是登录逻辑：读 result 里的表单值 → 调登录接口 → 写入 Cookie。
	// BaseUrl() 在真实书源里由 jsLib 提供，这里一并定义。
	loginJS := `function BaseUrl() { return baseUrl; }
function login(flag) {
  var payload = JSON.stringify({register_email: result['邮箱'], password: result['密码']});
  var res = java.ajax(BaseUrl() + '/login_api,{"method":"POST","headers":{"Content-Type":"application/json"},"body":' + JSON.stringify(payload) + '}');
  var data = JSON.parse(res);
  if (data.code == 0) {
    setAllCookies('qttoken=' + data.key);
    java.toast('登录成功');
    return true;
  }
  java.toast(data.msg || '登录失败');
  return false;
}
function setAllCookies(ck) { cookie.setCookie(BaseUrl(), ck); }`

	loginUI := []map[string]any{
		{"name": "邮箱", "type": "text"},
		{"name": "密码", "type": "password"},
		{"name": "登录", "type": "button", "action": "login(true)"},
	}
	uiJSON, err := json.Marshal(loginUI)
	if err != nil {
		t.Fatal(err)
	}

	src := map[string]any{
		"bookSourceUrl":    server,
		"bookSourceName":   "登录源",
		"bookSourceType":   0,
		"enabledCookieJar": true,
		"loginUrl":         loginJS,
		"loginUi":          string(uiJSON),
		"searchUrl":        server + "/search",
		"ruleSearch": map[string]any{
			"bookList": "class.item",
			"name":     "tag.h3@tag.a@text",
			"bookUrl":  "tag.h3@tag.a@href",
			"author":   "class.author@text",
		},
	}
	out, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func newLoginTestService(t *testing.T) (*ReaderService, *repository.Container) {
	t.Helper()
	// 唯一库名避免同包测试互相污染；cache=shared 让连接池共享同一份内存库
	// （多源搜索会并发写会话状态，而 :memory: 下每个连接各自一个库，
	// 并发写入对后续读取不可见，会造成测试假失败）。
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	cfg := &config.Config{}
	cfg.Secrets.JWTSecret = "test-secret"
	svc := NewReaderService(cfg, zap.NewNop(), repos)
	return svc, repos
}

// prepareLoginSource 导入测试书源并返回其 ID。
func prepareLoginSource(t *testing.T, svc *ReaderService, sourceJSON string) string {
	t.Helper()
	if _, err := svc.ImportSources(t.Context(), sourceJSON); err != nil {
		t.Fatal(err)
	}
	srcs, err := svc.ListSources(t.Context())
	if err != nil || len(srcs) == 0 {
		t.Fatalf("导入后应能读到书源: %v", err)
	}
	return srcs[0].ID
}

// TestSourceLoginEndToEnd 登录成功后：
//   - Cookie 落库（可再次读出）
//   - 搜索请求自动携带 Cookie 并通过鉴权
//   - 登出后 Cookie 清除，搜索重新变成未登录
func TestSourceLoginEndToEnd(t *testing.T) {
	srv := loginTestServer(t)
	defer srv.Close()
	svc, repos := newLoginTestService(t)
	ctx := t.Context()
	sourceID := prepareLoginSource(t, svc, loginTestSourceJSON(t, srv.URL))

	// ── 登录前：未鉴权，站点返回未登录页 → 搜不到书 ──
	if books, _, err := svc.Search(ctx, "会员"); err != nil {
		t.Fatal(err)
	} else if len(books) != 0 {
		t.Fatalf("未登录时不应搜到结果: %+v", books)
	}

	// ── 登录 ──
	res, err := svc.RunLoginAction(ctx, sourceID, "", map[string]string{
		"邮箱": "user@example.com", "密码": "pw123456",
	})
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if !res.OK {
		t.Fatalf("登录未成功: %+v", res)
	}
	if !res.LoggedIn {
		t.Fatalf("登录后应处于已登录态: %+v", res)
	}
	if len(res.Cookies) == 0 {
		t.Fatalf("登录后应有 Cookie 落库: %+v", res)
	}

	// ── 登录态应落库（换一个 service 实例仍可读到）──
	svc2 := NewReaderService(svc.cfg, zap.NewNop(), repos)
	info, err := svc2.GetSourceLogin(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !info.LoggedIn || len(info.Cookies) == 0 {
		t.Fatalf("新实例未能读到已持久化的登录态: %+v", info)
	}
	if info.Values["邮箱"] != "user@example.com" {
		t.Fatalf("登录表单值未持久化: %+v", info.Values)
	}

	// ── 登录后搜索：应携带 Cookie 并成功 ──
	books, skipped, err := svc.Search(ctx, "会员")
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) > 0 {
		t.Fatalf("已登录后搜索不应失败: %+v", skipped)
	}
	if len(books) != 1 || books[0].Name != "会员专享书" {
		t.Fatalf("搜索结果异常: %+v", books)
	}

	// ── 登出：Cookie 清除，搜索重新未登录 ──
	if err := svc.ClearSourceLogin(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	info, err = svc.GetSourceLogin(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if info.LoggedIn || len(info.Cookies) != 0 {
		t.Fatalf("登出后不应残留登录态: %+v", info)
	}
	if books, _, err := svc.Search(ctx, "会员"); err != nil {
		t.Fatal(err)
	} else if len(books) != 0 {
		t.Fatalf("登出后不应还能搜到结果: %+v", books)
	}
}

// TestSourceLoginWrongPassword 密码错误时登录动作应报失败并给出书源提示。
func TestSourceLoginWrongPassword(t *testing.T) {
	srv := loginTestServer(t)
	defer srv.Close()
	svc, _ := newLoginTestService(t)
	sourceID := prepareLoginSource(t, svc, loginTestSourceJSON(t, srv.URL))

	res, err := svc.RunLoginAction(t.Context(), sourceID, "", map[string]string{
		"邮箱": "user@example.com", "密码": "wrong",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.LoggedIn {
		t.Fatalf("密码错误不应处于已登录态: %+v", res)
	}
	if len(res.Toasts) == 0 {
		t.Fatalf("应把书源的失败提示回传：%+v", res)
	}
	if !strings.Contains(strings.Join(res.Toasts, " "), "密码错误") {
		t.Fatalf("提示语未透传: %v", res.Toasts)
	}
}

// TestSourceLoginInfo_ExposesUIFields 登录界面描述应包含 loginUi 的控件定义。
func TestSourceLoginInfo_ExposesUIFields(t *testing.T) {
	srv := loginTestServer(t)
	defer srv.Close()
	svc, _ := newLoginTestService(t)
	sourceID := prepareLoginSource(t, svc, loginTestSourceJSON(t, srv.URL))

	info, err := svc.GetSourceLogin(t.Context(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasLoginJS {
		t.Fatal("应识别到 loginUrl")
	}
	var names []string
	for _, f := range info.Fields {
		names = append(names, f.Name)
	}
	for _, want := range []string{"邮箱", "密码", "登录"} {
		if !containsStr(names, want) {
			t.Fatalf("loginUi 字段缺失 %q: %v", want, names)
		}
	}
	// 密码字段类型应保留，前端据此用 password 输入框
	for _, f := range info.Fields {
		if f.Name == "密码" && f.Type != "password" {
			t.Fatalf("密码字段类型 = %q", f.Type)
		}
	}
}

// TestSourceStateEncryptedAtRest 登录信息与 Cookie 应加密落库。
func TestSourceStateEncryptedAtRest(t *testing.T) {
	srv := loginTestServer(t)
	defer srv.Close()
	svc, repos := newLoginTestService(t)
	ctx := t.Context()
	sourceID := prepareLoginSource(t, svc, loginTestSourceJSON(t, srv.URL))

	if _, err := svc.RunLoginAction(ctx, sourceID, "", map[string]string{
		"邮箱": "user@example.com", "密码": "pw123456",
	}); err != nil {
		t.Fatal(err)
	}

	src, err := repos.Reader.GetSource(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	st, err := repos.Reader.GetSourceState(ctx, src.SourceURL)
	if err != nil || st == nil {
		t.Fatalf("未找到会话状态: %v", err)
	}
	if !strings.HasPrefix(st.LoginInfo, "enc:v1:") {
		t.Fatalf("登录信息应加密存储，实际: %q", st.LoginInfo)
	}
	if !strings.HasPrefix(st.Cookies, "enc:v1:") {
		t.Fatalf("Cookie 应加密存储，实际: %q", st.Cookies)
	}
	if strings.Contains(st.LoginInfo, "pw123456") {
		t.Fatal("明文密码出现在库中")
	}
}

// TestSourceVariableRoundTrip 源变量可通过接口读写，并被书源 JS getVariable 读到。
func TestSourceVariableRoundTrip(t *testing.T) {
	srv := loginTestServer(t)
	defer srv.Close()
	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	sourceID := prepareLoginSource(t, svc, loginTestSourceJSON(t, srv.URL))

	if err := svc.SetSourceVariable(ctx, sourceID, `{"线路":"https://v2.example.com"}`); err != nil {
		t.Fatal(err)
	}
	info, err := svc.GetSourceLogin(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(info.Variable), &m); err != nil {
		t.Fatalf("变量回读失败: %q", info.Variable)
	}
	if m["线路"] != "https://v2.example.com" {
		t.Fatalf("变量值不符: %+v", m)
	}

	// 非法 JSON 应被拒绝
	if err := svc.SetSourceVariable(ctx, sourceID, "not-json"); err == nil {
		t.Fatal("非法 JSON 变量应被拒绝")
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestEnabledCookieJarGating enabledCookieJar=false 时不再自动保存响应 Set-Cookie，
// 但书源 JS 主动 cookie.setCookie 写入的仍应保留（对应 legado 语义）。
func TestEnabledCookieJarGating(t *testing.T) {
	// 站点在响应里下发 Set-Cookie
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "auto", Value: "from-response", Path: "/"})
		_, _ = w.Write([]byte(`<html><body><div class="item"><h3><a href="/book/1">书</a></h3></div></body></html>`))
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	ctx := t.Context()

	// 造一个 searchUrl 指向该站点、enabledCookieJar=false 的书源
	srcJSON := `{
  "bookSourceUrl": "` + srv.URL + `",
  "bookSourceName": "无 CookieJar 源",
  "bookSourceType": 0,
  "enabledCookieJar": false,
  "searchUrl": "` + srv.URL + `/search",
  "ruleSearch": {"bookList":"class.item","name":"tag.h3@tag.a@text","bookUrl":"tag.h3@tag.a@href"}
}`
	sourceID := prepareLoginSource(t, svc, srcJSON)

	if _, _, err := svc.Search(ctx, "任意"); err != nil {
		t.Fatal(err)
	}
	// 自动捕获被关闭：不应出现 auto=from-response
	src, _ := reposReaderURL(ctx, svc, sourceID)
	st := svc.newSourceState(ctx, src)
	if got := st.GetCookie(srv.URL); strings.Contains(got, "auto=from-response") {
		t.Fatalf("enabledCookieJar=false 时不应自动保存 Set-Cookie: %q", got)
	}

	// 对照：开启时应当捕获
	srcJSONOn := strings.Replace(srcJSON, `"enabledCookieJar": false`, `"enabledCookieJar": true`, 1)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "auto", Value: "from-response", Path: "/"})
		_, _ = w.Write([]byte(`<html><body><div class="item"><h3><a href="/book/1">书</a></h3></div></body></html>`))
	}))
	defer srv2.Close()
	srcJSONOn = strings.ReplaceAll(srcJSONOn, srv.URL, srv2.URL)
	// prepareLoginSource 返回列表首个书源，这里按 URL 精确定位刚导入的对照源
	if _, err := svc.ImportSources(ctx, srcJSONOn); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Search(ctx, "任意"); err != nil {
		t.Fatal(err)
	}
	if got := svc.newSourceState(ctx, srv2.URL).GetCookie(srv2.URL); !strings.Contains(got, "auto=from-response") {
		t.Fatalf("enabledCookieJar 默认开启时应捕获 Set-Cookie: %q", got)
	}
}

// reposReaderURL 取书源的 SourceURL（会话状态按它索引）。
func reposReaderURL(ctx context.Context, svc *ReaderService, sourceID string) (string, error) {
	src, err := svc.repo.GetSource(ctx, sourceID)
	if err != nil {
		return "", err
	}
	return src.SourceURL, nil
}
