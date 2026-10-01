package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

// 书源宿主浏览器的 HTTP 层测试：轮询待办 → 承载页面 → 回传 DOM。
//
// 这条链路对应真实书源的「切换线路」「用户后台」：
// java.startBrowserAwait 会阻塞在 POST /sources/:id/login 上，
// 前端必须能从 /reader/browser/pending 拿到页面，再把 DOM POST 回去。

// browserHandlerSource 构造一个用 startBrowserAwait 切换线路的书源。
func browserHandlerSource(sourceURL string) string {
	loginJS := `function switchLine() {
  let html = '<!DOCTYPE html><html><head></head><body><span id="serverValue">线路甲</span></body></html>';
  let body = java.startBrowserAwait('data:text/html;base64,' + java.base64Encode(html), '线路设置', false).body();
  let m = body.match(/id="serverValue"\s*>\s*([^<]*?)\s*<\/span>/);
  source.setVariable(JSON.stringify({线路: m ? m[1] : ''}));
}`
	src := map[string]any{
		"bookSourceUrl":  sourceURL,
		"bookSourceName": "浏览器面板源",
		"loginUrl":       loginJS,
		"loginUi":        `[{"name":"切换线路","type":"button","action":"switchLine()"}]`,
	}
	out, _ := json.Marshal(src)
	return string(out)
}

// registerBrowserRoutesForTest 挂载阅读路由 + 承载页面/资源代理（跳过鉴权）。
// 生产环境里后两个是公开路由（iframe 带不了 JWT），鉴权靠 HMAC 签名。
func registerBrowserRoutesForTest(cfg *service.Container) *gin.Engine {
	r := registerReaderRoutesForTest(cfg)
	r.GET("/api/reader/browser/page", readerBrowserPageHandler(cfg))
	r.GET("/api/reader/browser/asset", readerBrowserAssetHandler(cfg))
	return r
}

func TestReaderBrowserPanelRoutes(t *testing.T) {
	container := newReaderHandlerContainer(t)
	router := registerBrowserRoutesForTest(container)

	// 导入书源
	body, _ := json.Marshal(map[string]string{"text": browserHandlerSource("https://panel.example.com")})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reader/sources/import", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("导入书源 status=%d body=%s", w.Code, w.Body.String())
	}
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
	sourceID := list.Sources[0].ID

	// 执行切换线路：这个请求会阻塞等待用户操作，放到后台跑
	actionDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/reader/sources/"+sourceID+"/login",
			strings.NewReader(`{"action":"switchLine()","fields":{}}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		actionDone <- rec
	}()

	// 轮询待办页面
	var page struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Mode     string `json:"mode"`
		PageURL  string `json:"page_url"`
		SourceID string `json:"source_id"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("等待超时：未出现待办页面")
		}
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
			"/api/reader/browser/pending?source_id="+sourceID, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("轮询 status=%d body=%s", w.Code, w.Body.String())
		}
		var pending struct {
			Pages []json.RawMessage `json:"pages"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &pending); err != nil {
			t.Fatalf("待办返回不是 JSON: %s", w.Body.String())
		}
		if len(pending.Pages) > 0 {
			if err := json.Unmarshal(pending.Pages[0], &page); err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if page.Mode != "wait" || page.Title != "线路设置" {
		t.Fatalf("待办描述异常: %+v", page)
	}
	if page.SourceID != sourceID {
		t.Fatalf("待办应带书源 ID: %+v", page)
	}

	// 承载页面：签名正确应返回 HTML，签名错误应 403
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, page.PageURL, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("承载页面 status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `id="serverValue"`) {
		t.Fatalf("承载页面内容异常: %s", w.Body.String())
	}
	// 服务端注入的 DOM 回传脚本必须在（父窗口靠它取回用户操作后的页面）
	if !strings.Contains(w.Body.String(), "data-mebox-browser-bridge") {
		t.Fatalf("承载页面缺少 DOM 回传脚本: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reader/browser/page?id="+page.ID+"&s=forged", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("伪造签名应 403，实际 %d", w.Code)
	}

	// 回传用户操作后的 DOM（模拟点了 √）
	result, _ := json.Marshal(map[string]any{
		"id":   page.ID,
		"body": `<html><body><span id="serverValue">线路乙</span></body></html>`,
	})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/reader/browser/result", strings.NewReader(string(result)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("回传结果 status=%d body=%s", w.Code, w.Body.String())
	}

	// 阻塞的登录动作应被放行
	select {
	case rec := <-actionDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("登录动作 status=%d body=%s", rec.Code, rec.Body.String())
		}
		var res struct {
			OK bool `json:"ok"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
		if !res.OK {
			t.Fatalf("动作未成功: %s", rec.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("回传后阻塞未解除")
	}

	// 线路应已写进源变量
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reader/sources/"+sourceID+"/login", nil))
	var info struct {
		Variable string `json:"variable"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &info)
	if !strings.Contains(info.Variable, "线路乙") {
		t.Fatalf("线路未写入源变量: %q", info.Variable)
	}

	// 完成后待办清空
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/reader/browser/pending?source_id="+sourceID, nil))
	var after struct {
		Pages []json.RawMessage `json:"pages"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &after)
	if len(after.Pages) != 0 {
		t.Fatalf("完成后待办未清理: %s", w.Body.String())
	}
}

func TestReaderBrowserResultRejectsUnknownPage(t *testing.T) {
	container := newReaderHandlerContainer(t)
	router := registerBrowserRoutesForTest(container)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reader/browser/result",
		strings.NewReader(`{"id":"not-a-real-page","body":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未知待办应 400，实际 %d body=%s", w.Code, w.Body.String())
	}
}

func TestReaderBrowserResultRequiresID(t *testing.T) {
	container := newReaderHandlerContainer(t)
	router := registerBrowserRoutesForTest(container)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reader/browser/result", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺少 id 应 400，实际 %d", w.Code)
	}
}

// TestReaderBrowserPageBadQuery 承载地址参数缺失/非法时不应 500。
func TestReaderBrowserPageBadQuery(t *testing.T) {
	container := newReaderHandlerContainer(t)
	router := registerBrowserRoutesForTest(container)

	for _, target := range []string{"/api/reader/browser/page", "/api/reader/browser/page?id=x",
		"/api/reader/browser/asset", "/api/reader/browser/asset?id=x&u=!!!&s=y"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s 应 403，实际 %d", target, w.Code)
		}
	}
	// 合法 base64 但不匹配的签名
	w := httptest.NewRecorder()
	q := url.Values{"id": {"x"}, "u": {"aHR0cHM6Ly9leGFtcGxlLmNvbS8"}, "s": {"deadbeef"}}
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reader/browser/asset?"+q.Encode(), nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("签名不匹配应 403，实际 %d", w.Code)
	}
}
