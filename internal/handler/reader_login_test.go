package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
	"github.com/truewhile/MeBox/internal/service/reader"
)

// 书源登录相关的 HTTP 层测试：路由注册与 JSON 契约。

func newReaderHandlerContainer(t *testing.T) *service.Container {
	t.Helper()
	// 唯一库名 + cache=shared：阅读链路会并发写会话状态，
	// 而 :memory: 下每个连接各自一个库，并发写入对后续读取不可见。
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	cfg := &config.Config{}
	cfg.Secrets.JWTSecret = "test-secret"
	return &service.Container{
		Repo:   repos,
		Cfg:    cfg,
		Log:    zap.NewNop(),
		Reader: reader.NewReaderService(cfg, zap.NewNop(), repos),
	}
}

// loginHandlerSource 返回一个声明了 loginUrl/loginUi 的书源 JSON。
func loginHandlerSource(server string) string {
	loginJS := `function login(flag) {
  var payload = JSON.stringify({register_email: result['邮箱'], password: result['密码']});
  var res = java.ajax(baseUrl + '/login_api,{"method":"POST","headers":{"Content-Type":"application/json"},"body":' + JSON.stringify(payload) + '}');
  var data = JSON.parse(res);
  if (data.code == 0) { cookie.setCookie(baseUrl, 'qttoken=' + data.key); java.toast('登录成功'); return true; }
  java.toast(data.msg || '登录失败'); return false;
}`
	ui, _ := json.Marshal([]map[string]any{
		{"name": "邮箱", "type": "text"},
		{"name": "密码", "type": "password"},
		{"name": "登录", "type": "button", "action": "login(true)"},
	})
	src := map[string]any{
		"bookSourceUrl":  server,
		"bookSourceName": "登录源",
		"bookSourceType": 0,
		"loginUrl":       loginJS,
		"loginUi":        string(ui),
		"searchUrl":      server + "/search",
		"ruleSearch": map[string]any{
			"bookList": "class.item",
			"name":     "tag.h3@tag.a@text",
			"bookUrl":  "tag.h3@tag.a@href",
		},
	}
	b, _ := json.Marshal(src)
	return string(b)
}

// loginHandlerServer 模拟需要登录的站点。
func loginHandlerServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login_api":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["register_email"] != "u@e.com" || body["password"] != "pw" {
				_, _ = w.Write([]byte(`{"code":1,"msg":"账号或密码错误"}`))
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "qttoken", Value: "HTOKEN_1234567890", Path: "/"})
			_, _ = w.Write([]byte(`{"code":0,"key":"HTOKEN_1234567890"}`))
		case "/search":
			if !strings.Contains(r.Header.Get("Cookie"), "qttoken=HTOKEN_1234567890") {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`<html>未登录</html>`))
				return
			}
			_, _ = w.Write([]byte(`<html><body><div class="item"><h3><a href="/book/1">登录后可见</a></h3></div></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
}

// registerReaderRoutesForTest 挂载阅读路由（跳过鉴权中间件）。
func registerReaderRoutesForTest(cfg *service.Container) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerReaderRoutes(r.Group("/api"), cfg)
	return r
}

// TestReaderLoginRoutesEndToEnd 走完整 HTTP 路由：
// 查看登录信息 → 执行登录 → 搜索携带 Cookie 成功。
func TestReaderLoginRoutesEndToEnd(t *testing.T) {
	srv := loginHandlerServer(t)
	defer srv.Close()
	cfg := newReaderHandlerContainer(t)
	router := registerReaderRoutesForTest(cfg)

	// 导入书源
	body, _ := json.Marshal(map[string]string{"text": loginHandlerSource(srv.URL)})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reader/sources/import", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("导入书源 status=%d body=%s", w.Code, w.Body.String())
	}

	// 列表应标注 has_login
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reader/sources", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("书源列表 status=%d", w.Code)
	}
	var listResp struct {
		Sources []struct {
			ID       string `json:"id"`
			HasLogin bool   `json:"has_login"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil || len(listResp.Sources) != 1 {
		t.Fatalf("书源列表解析失败: %s", w.Body.String())
	}
	if !listResp.Sources[0].HasLogin {
		t.Fatal("has_login 应为 true")
	}
	sourceID := listResp.Sources[0].ID

	// GET 登录信息：应返回表单字段与未登录状态
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reader/sources/"+sourceID+"/login", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("登录信息 status=%d body=%s", w.Code, w.Body.String())
	}
	var info struct {
		HasLoginJS bool `json:"has_login_js"`
		LoggedIn   bool `json:"logged_in"`
		Fields     []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("登录信息解析失败: %s", w.Body.String())
	}
	if !info.HasLoginJS || info.LoggedIn {
		t.Fatalf("初始状态异常: %+v", info)
	}
	var names []string
	for _, f := range info.Fields {
		names = append(names, f.Name)
	}
	for _, want := range []string{"邮箱", "密码", "登录"} {
		if !slices.Contains(names, want) {
			t.Fatalf("缺少表单字段 %q: %v", want, names)
		}
	}

	// 密码错误 → ok=false，提示透传
	loginBody, _ := json.Marshal(map[string]any{
		"fields": map[string]string{"邮箱": "u@e.com", "密码": "bad"},
	})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/reader/sources/"+sourceID+"/login", strings.NewReader(string(loginBody)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录 status=%d body=%s", w.Code, w.Body.String())
	}
	var failRes struct {
		OK       bool     `json:"ok"`
		LoggedIn bool     `json:"logged_in"`
		Toasts   []string `json:"toasts"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &failRes)
	if failRes.LoggedIn {
		t.Fatalf("密码错误不应登录成功: %s", w.Body.String())
	}
	if !strings.Contains(strings.Join(failRes.Toasts, " "), "密码错误") {
		t.Fatalf("失败提示未透传: %s", w.Body.String())
	}

	// 正确密码 → ok=true 且已登录
	loginBody, _ = json.Marshal(map[string]any{
		"fields": map[string]string{"邮箱": "u@e.com", "密码": "pw"},
	})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/reader/sources/"+sourceID+"/login", strings.NewReader(string(loginBody)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录 status=%d body=%s", w.Code, w.Body.String())
	}
	var okRes struct {
		OK       bool              `json:"ok"`
		LoggedIn bool              `json:"logged_in"`
		Cookies  map[string]string `json:"cookies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &okRes); err != nil {
		t.Fatal(err)
	}
	if !okRes.OK || !okRes.LoggedIn || len(okRes.Cookies) == 0 {
		t.Fatalf("登录应成功并带 Cookie: %s", w.Body.String())
	}

	// 搜索应携带 Cookie 并拿到结果
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/reader/search", strings.NewReader(`{"key":"任意"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	var searchRes struct {
		Books   []struct{ Name string } `json:"books"`
		Skipped []struct {
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &searchRes)
	if len(searchRes.Books) != 1 || searchRes.Books[0].Name != "登录后可见" {
		t.Fatalf("登录后搜索应成功: %s", w.Body.String())
	}

	// 登出 → 登录态清空
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/reader/sources/"+sourceID+"/login", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("登出 status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reader/sources/"+sourceID+"/login", nil))
	var afterLogout struct {
		LoggedIn bool              `json:"logged_in"`
		Cookies  map[string]string `json:"cookies"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &afterLogout)
	if afterLogout.LoggedIn || len(afterLogout.Cookies) != 0 {
		t.Fatalf("登出后应无登录态: %s", w.Body.String())
	}
}

// TestReaderSetSourceVariableRoute 源变量接口应保存合法 JSON 并拒绝非法 JSON。
func TestReaderSetSourceVariableRoute(t *testing.T) {
	cfg := newReaderHandlerContainer(t)
	router := registerReaderRoutesForTest(cfg)

	body, _ := json.Marshal(map[string]string{"text": `{"bookSourceUrl":"https://v.example.com","bookSourceName":"变量源"}`})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reader/sources/import", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reader/sources", nil))
	var list struct {
		Sources []struct {
			ID string `json:"id"`
		} `json:"sources"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Sources) != 1 {
		t.Fatalf("应导入 1 个书源: %s", w.Body.String())
	}
	id := list.Sources[0].ID

	// 合法 JSON
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/reader/sources/"+id+"/variable",
		strings.NewReader(`{"variable":"{\"线路\":\"https://v2.example.com\"}"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("保存变量 status=%d body=%s", w.Code, w.Body.String())
	}

	// 非法 JSON 应 400
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/reader/sources/"+id+"/variable",
		strings.NewReader(`{"variable":"not-json"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法变量应 400，实际 %d body=%s", w.Code, w.Body.String())
	}
}
