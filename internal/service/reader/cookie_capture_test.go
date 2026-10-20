package reader

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

// 本文件：响应 Set-Cookie 回写会话的回归测试（cookies.go）。
//
// 背景：扫码登录类书源把凭证放在**跳转链中间那一跳**的 Set-Cookie 里
// （B站 的 crossDomain 票据地址就是如此），并且签到站点会声明业务域的
// Domain（Domain=.bilibili.com）。之前的实现只看最终响应、按请求地址归档，
// 于是凭证永远进不了会话，表现为「面板显示已授权，书源却始终未登录」。

// cookieCaptureServer 构造跳转链：/redeem 下发声明了 Domain 的凭证并 302，
// /done 只回 200。这样「中间那一跳的 Set-Cookie」与「最终响应的 Set-Cookie」
// 能被分开断言。
func cookieCaptureServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/redeem", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name: "SESSDATA", Value: "cred-abc", Domain: ".example.com", Path: "/",
		})
		http.Redirect(w, r, "/done", http.StatusFound)
	})
	mux.HandleFunc("/done", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "biz", Value: "1", Path: "/"})
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/plain", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body>面板</body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestExecuteCapturesRedirectHopCookies 主链路：跳转中间那一跳的 Set-Cookie
// 必须被收下，且按 Cookie 自己的 Domain 归档（而不是按请求地址）。
func TestExecuteCapturesRedirectHopCookies(t *testing.T) {
	srv := cookieCaptureServer(t)
	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	state := svc.newSourceState(ctx, srv.URL)

	req := &rule.Request{Method: "GET", URL: srv.URL + "/redeem", Headers: map[string]string{}}
	if _, _, _, err := svc.executeWithState(ctx, req, state, true); err != nil {
		t.Fatalf("请求失败: %v", err)
	}

	// 凭证声明的是 .example.com，就应该落在 example.com 这个域桶里。
	if got := state.GetCookie("https://example.com/"); !strings.Contains(got, "SESSDATA=cred-abc") {
		t.Fatalf("跳转中间那一跳的凭证没有按 Domain 归档: %q", got)
	}
	// 最终响应的 Cookie 按该跳地址归档。
	if got := state.GetCookie(srv.URL); !strings.Contains(got, "biz=1") {
		t.Fatalf("最终响应的 Cookie 没有被收下: %q", got)
	}
}

// TestExecuteSkipsCookieCaptureWhenJarDisabled enabledCookieJar=false 时
// 主链路不应自动累积 Cookie。
func TestExecuteSkipsCookieCaptureWhenJarDisabled(t *testing.T) {
	srv := cookieCaptureServer(t)
	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	state := svc.newSourceState(ctx, srv.URL)

	req := &rule.Request{Method: "GET", URL: srv.URL + "/redeem", Headers: map[string]string{}}
	if _, _, _, err := svc.executeWithState(ctx, req, state, false); err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if got := state.GetCookie("https://example.com/"); got != "" {
		t.Fatalf("关掉 CookieJar 后不应自动累积: %q", got)
	}
	if got := state.GetCookie(srv.URL); got != "" {
		t.Fatalf("关掉 CookieJar 后不应自动累积: %q", got)
	}
}

// TestProxyBrowserXHRCapturesCookies 面板链路：页面自己发起的请求（经
// ProxyBrowserXHR 转发）拿到的 Set-Cookie 也要写回书源会话并能落库。
// 这正是「页面里登录成功、书源却始终未登录」的那个缺口。
func TestProxyBrowserXHRCapturesCookies(t *testing.T) {
	srv := cookieCaptureServer(t)
	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	const sourceURL = "https://api.example.com"
	const sourceID = "panel-source-1"
	state := svc.newSourceState(ctx, sourceURL)

	// 承载页面本身不下发凭证（/plain），确保断言只针对代理请求。
	if err := svc.openBrowser(ctx, sourceURL, sourceID, readerTestUserID,
		browserCookieTarget{state: state},
		rule.BrowserTask{URL: srv.URL + "/plain", Title: "面板"}); err != nil {
		t.Fatalf("登记页面失败: %v", err)
	}
	pages := svc.PendingBrowserPages(readerTestUserID, sourceID)
	if len(pages) != 1 {
		t.Fatalf("应登记 1 个待办页面，实际 %d", len(pages))
	}

	// 页面请求票据地址：302 时下发凭证，最终响应只带回 JSON。
	if _, err := svc.ProxyBrowserXHR(ctx, pages[0].ID, http.MethodGet, srv.URL+"/redeem", nil, ""); err != nil {
		t.Fatalf("代理请求失败: %v", err)
	}
	if got := state.GetCookie("https://example.com/"); !strings.Contains(got, "SESSDATA=cred-abc") {
		t.Fatalf("面板请求的凭证没有写回会话: %q", got)
	}

	// 登录态必须能持久化：重新读一条会话也应看到这份 Cookie。
	state.flush()
	reloaded := svc.newSourceState(ctx, sourceURL)
	if got := reloaded.GetCookie("https://example.com/"); !strings.Contains(got, "SESSDATA=cred-abc") {
		t.Fatalf("面板拿到的凭证没有落库: %q", got)
	}
}

// TestProxyBrowserXHRWithoutSessionSkipsCapture 没有会话可回写时（如段评面板）
// 代理请求照常工作，只是不做 Cookie 累积。
func TestProxyBrowserXHRWithoutSessionSkipsCapture(t *testing.T) {
	srv := cookieCaptureServer(t)
	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	const sourceID = "panel-source-2"

	if err := svc.openBrowser(ctx, srv.URL, sourceID, readerTestUserID,
		browserCookieTarget{},
		rule.BrowserTask{URL: srv.URL + "/plain", Title: "面板"}); err != nil {
		t.Fatalf("登记页面失败: %v", err)
	}
	pages := svc.PendingBrowserPages(readerTestUserID, sourceID)
	if len(pages) != 1 {
		t.Fatalf("应登记 1 个待办页面，实际 %d", len(pages))
	}
	res, err := svc.ProxyBrowserXHR(ctx, pages[0].ID, http.MethodGet, srv.URL+"/done", nil, "")
	if err != nil {
		t.Fatalf("代理请求应照常成功: %v", err)
	}
	if res.Status != http.StatusOK {
		t.Fatalf("代理状态 = %d", res.Status)
	}
}

// TestBrowserHostCookieTargetRespectsJarFlag 面板链路同样受 enabledCookieJar 控制。
func TestBrowserHostCookieTargetRespectsJarFlag(t *testing.T) {
	st := &sourceState{cookies: map[string]string{}}
	if got := (&browserHost{state: st, capture: false}).cookieTarget(); got.state != nil {
		t.Fatal("enabledCookieJar=false 时不应把会话交给面板回写 Cookie")
	}
	if got := (&browserHost{state: st, capture: true}).cookieTarget(); got.state != st {
		t.Fatal("开启时应把会话交给面板回写 Cookie")
	}
}

// TestSourceStateCookieConcurrency 面板里的资源/接口代理是并发回写 Cookie 的，
// 必须与书源 JS 的读取安全共存（-race 下能抓到缺锁）。
func TestSourceStateCookieConcurrency(t *testing.T) {
	st := &sourceState{cookies: map[string]string{}}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			url := "https://example.com/"
			st.SetCookie(url, "k=1")
			_ = st.GetCookie(url)
			_ = st.snapshotCookies()
		}(i)
	}
	wg.Wait()
	if got := st.GetCookie("https://example.com/"); !strings.Contains(got, "k=1") {
		t.Fatalf("并发写入后应能读到 Cookie: %q", got)
	}
}
