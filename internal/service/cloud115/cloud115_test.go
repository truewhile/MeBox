package cloud115

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockTransport 用 httptest server 替换 API 基址。
func mockAPI(t *testing.T, handler http.HandlerFunc) func() {
	t.Helper()
	pro := httptest.NewServer(handler)
	passport := httptest.NewServer(handler)
	qr := httptest.NewServer(handler)
	oldPro, oldPassport, oldQR := ProAPIBase, PassportAPIBase, QRCodeAPIBase
	ProAPIBase, PassportAPIBase, QRCodeAPIBase = pro.URL, passport.URL, qr.URL
	t.Cleanup(func() {
		ProAPIBase, PassportAPIBase, QRCodeAPIBase = oldPro, oldPassport, oldQR
		pro.Close()
		passport.Close()
		qr.Close()
	})
	return func() {}
}

func TestGetQrCode(t *testing.T) {
	var called bool
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/open/authDeviceCode" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostFormValue("client_id") != "100195125" {
			t.Errorf("bad client_id %q", r.PostFormValue("client_id"))
		}
		if r.PostFormValue("code_challenge") == "" {
			t.Errorf("missing code_challenge")
		}
		w.Write([]byte(`{"state":true,"data":{"uid":"U1","time":1700,"sign":"S1","qrcode":"https://img/qr.png"}}`))
	})
	c := NewOpenClient("100195125", "", "")
	qr, err := c.GetQrCode()
	if err != nil {
		t.Fatalf("get qr: %v", err)
	}
	if !called {
		t.Fatal("request not hit")
	}
	if qr.Uid != "U1" || qr.Qrcode == "" || qr.CodeVerifier == "" || len(qr.CodeVerifier) != 64 {
		t.Fatalf("bad qr data: %#v", qr)
	}
}

func TestQrCodeScanStatusSequence(t *testing.T) {
	calls := 0
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get/status/" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		calls++
		switch calls {
		case 1:
			w.Write([]byte(`{"state":true,"data":{"status":0}}`))
		case 2:
			w.Write([]byte(`{"state":true,"data":{"status":1}}`))
		default:
			w.Write([]byte(`{"state":true,"data":{"status":2}}`))
		}
	})
	c := NewOpenClient("", "", "")
	code := &QrCodeData{Uid: "U1", Time: 1700, Sign: "S1"}
	want := []QrCodeScanStatus{QrCodeScanStatusNotScanned, QrCodeScanStatusScanned, QrCodeScanStatusConfirmed}
	for i, exp := range want {
		got, err := c.QrCodeScanStatus(code)
		if err != nil {
			t.Fatalf("status %d: %v", i, err)
		}
		if got != exp {
			t.Fatalf("status %d: got %v want %v", i, got, exp)
		}
	}
}

func TestGetTokenAndRefresh(t *testing.T) {
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/deviceCodeToToken":
			w.Write([]byte(`{"state":true,"data":{"access_token":"at1","refresh_token":"rt1","expires_in":7200}}`))
		case "/open/refreshToken":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.PostFormValue("refresh_token") != "rt2" {
				t.Errorf("bad refresh_token %q", r.PostFormValue("refresh_token"))
			}
			w.Write([]byte(`{"state":true,"data":{"access_token":"at2","refresh_token":"rt2","expires_in":7200}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	c := NewOpenClient("100195125", "", "")
	token, err := c.GetToken(&QrCodeDataReturn{QrCodeData: QrCodeData{Uid: "U1"}, CodeVerifier: "v"})
	if err != nil {
		t.Fatalf("get token: %v", err)
	}
	if token.AccessToken != "at1" || c.AccessToken != "at1" {
		t.Fatalf("bad token: %#v", token)
	}
	token, err = c.RefreshToken("rt2")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if token.AccessToken != "at2" || c.RefreshTokenStr != "rt2" {
		t.Fatalf("bad refresh result: %#v", token)
	}
}

func TestRefreshTokenDead(t *testing.T) {
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"state":false,"code":40140119,"message":"refresh_token 已过期"}`))
	})
	c := NewOpenClient("100195125", "at", "rt-dead")
	_, err := c.RefreshToken("rt-dead")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsRefreshTokenDead(err) {
		t.Fatalf("expected dead refresh token error, got %v", err)
	}
	if c.AccessToken != "" {
		t.Fatalf("dead token should clear access token")
	}
}

func TestConcurrentTokenFailuresShareOneRefresh(t *testing.T) {
	var mu sync.Mutex
	refreshCalls := 0
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/refreshToken" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		mu.Lock()
		refreshCalls++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"state":true,"data":{"access_token":"at-new","refresh_token":"rt-new","expires_in":7200}}`))
	})

	client := NewOpenClient("100195129", "at-old", "rt-old")
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- client.tryRefreshTokenLocked(context.Background(), "at-old")
		}()
	}
	wg.Wait()
	close(results)
	for ok := range results {
		if !ok {
			t.Fatal("concurrent refresh should reuse the refreshed token")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
	if client.CurrentAccessToken() != "at-new" {
		t.Fatalf("access token = %q, want at-new", client.CurrentAccessToken())
	}
}

func TestFsListAndDownload(t *testing.T) {
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/ufile/files":
			if r.Header.Get("Authorization") != "Bearer at1" {
				t.Errorf("missing auth header")
			}
			w.Write([]byte(`{"state":true,"path":[{"name":"根目录","cid":0}],"data":[
				{"fid":"100","fc":"0","fn":"Movies","fs":0},
				{"fid":"200","fc":"1","fn":"a.mkv","fs":123,"pc":"pickA"}]}`))
		case "/open/ufile/downurl":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.PostFormValue("pick_code") != "pickA" {
				t.Errorf("bad pick_code")
			}
			w.Write([]byte(`{"state":true,"data":{"200":{"file_name":"a.mkv","url":{"url":"https://cdn/x.mkv"}}}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	c := NewOpenClient("100195125", "at1", "rt1")
	files, pathStr, err := c.GetFsList(context.Background(), "0", 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d", len(files))
	}
	if !files[0].IsDir() {
		t.Fatalf("first entry should be dir")
	}
	if pathStr != "根目录" {
		t.Fatalf("path_str = %q", pathStr)
	}
	url, err := c.GetDownloadURL(context.Background(), "pickA")
	if err != nil {
		t.Fatalf("downurl: %v", err)
	}
	if url != "https://cdn/x.mkv" {
		t.Fatalf("bad url %q", url)
	}
}

func (f RemoteFile) IsDir() bool { return f.Category == TypeDir }

func TestGetFsDetailByCid(t *testing.T) {
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/folder/get_info" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`{"state":true,"data":{"file_id":"100","file_name":"Movies","file_category":"0","size_byte":123}}`))
	})
	c := NewOpenClient("100195125", "at1", "rt1")
	detail, err := c.GetFsDetailByCid(context.Background(), "100")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.FileId != "100" || detail.FileName != "Movies" {
		t.Fatalf("bad detail: %#v", detail)
	}
}

// TestRelayRoundTrip 中继加解密往返 + 回调解析。
func TestRelayRoundTrip(t *testing.T) {
	RelayEncryptionKey = "unit-test-shared-key"
	defer func() { RelayEncryptionKey = "" }()

	payload := `{"data":{"access_token":"at","refresh_token":"rt","expires_in":7200}}`
	encrypted, err := EncryptRelay(payload)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := DecryptRelay(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != payload {
		t.Fatalf("round trip mismatch: %q", decrypted)
	}

	provider, err := GetOAuthProvider(Source{SourceType: SourceTypeBuiltInRelay, Provider: ProviderQMediaSync, AppID: "QMediaSync"})
	if err != nil {
		t.Fatalf("relay provider should be available when key configured: %v", err)
	}
	token, err := provider.Confirm(context.Background(), map[string]string{"data": encrypted})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !token.Done || token.AccessToken != "at" || token.RefreshToken != "rt" {
		t.Fatalf("bad token: %#v", token)
	}
}

func TestRelayProviderRequiresKey(t *testing.T) {
	RelayEncryptionKey = ""
	if _, err := GetOAuthProvider(Source{SourceType: SourceTypeBuiltInRelay, Provider: ProviderQMediaSync, AppID: "QMediaSync"}); err == nil {
		t.Fatal("relay provider should fail without key")
	}
	if RelayAvailable() {
		t.Fatal("RelayAvailable should be false without key")
	}
}

func TestMoviePilotProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/u115/auth_url":
			w.Write([]byte(`{"auth_url":"https://passport.115/auth?state=ST","state":"ST"}`))
		case "/u115/token":
			if r.URL.Query().Get("state") != "ST" {
				t.Errorf("bad state")
			}
			w.Write([]byte(`{"state":true,"data":{"access_token":"at","refresh_token":"rt","expires_in":7200}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	provider := moviePilotOAuthProvider{authServer: srv.URL}
	result, err := provider.BuildAuth(context.Background(), OAuthURLRequest{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if result.AuthURL == "" || result.State != "ST" || !result.Polling {
		t.Fatalf("bad result: %#v", result)
	}
	token, err := provider.Poll(context.Background(), "ST")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !token.Done || token.AccessToken != "at" {
		t.Fatalf("bad token: %#v", token)
	}
}

func TestCloudDriveBuildAuth(t *testing.T) {
	provider := cloudDriveOAuthProvider{source: Source{SourceType: SourceTypeThirdPartyService, Provider: ProviderCloudDrive, AppID: "100195313"}}
	result, err := provider.BuildAuth(context.Background(), OAuthURLRequest{
		RedirectURL:     "http://127.0.0.1:8080/api/strm/oauth/callback",
		AuthorizationID: "auth-123",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(result.AuthURL, "client_id=100195313") {
		t.Fatalf("bad auth url: %s", result.AuthURL)
	}
	parsed, err := url.Parse(result.AuthURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	state, err := url.QueryUnescape(parsed.Query().Get("state"))
	if err != nil {
		t.Fatalf("unescape state: %v", err)
	}
	if !strings.Contains(state, "authorization_id=auth-123") {
		t.Fatalf("missing auth id in state: %s", result.AuthURL)
	}
	token, err := provider.Confirm(context.Background(), map[string]string{
		"access_token": "at", "refresh_token": "rt", "expires_in": "7200",
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !token.Done || token.AccessToken != "at" {
		t.Fatalf("bad token: %#v", token)
	}
}

func TestSourceCatalog(t *testing.T) {
	if len(BuiltInAppIDSources()) < 50 {
		t.Fatalf("built-in app catalog too small: %d", len(BuiltInAppIDSources()))
	}
	if _, ok := FindSource(SourceTypeBuiltInAppID, ProviderOfficialPKCE, "100195125"); !ok {
		t.Fatal("媒体播放器 app not found")
	}
	if len(BuiltInRelaySources()) == 0 || len(ThirdPartySources()) != 2 {
		t.Fatal("relay/thrid-party sources broken")
	}
}

func TestHTTP405AndThrottleRecovery(t *testing.T) {
	tm := NewThrottleManager(100 * time.Millisecond)
	qe := NewQueueExecutor(10, 200, 12000)
	qe.throttleManager = tm

	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("Method Not Allowed"))
	})

	c := NewOpenClient("100195125", "at1", "rt1")
	c.executor = qe

	_, err := c.GetDownloadURL(context.Background(), "pickTest")
	if err == nil {
		t.Fatal("expected 405 error")
	}
	if !tm.IsThrottled() {
		t.Fatal("405 should trigger throttle status")
	}

	// 验证熔断冷却后能正常恢复
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := tm.WaitThrottleRecovery(ctx); err != nil {
		t.Fatalf("wait throttle recovery failed: %v", err)
	}
	if tm.IsThrottled() {
		t.Fatal("throttle status should be cleared after duration")
	}
}

func TestThrottleCodeHandling(t *testing.T) {
	tm := NewThrottleManager(100 * time.Millisecond)
	qe := NewQueueExecutor(10, 200, 12000)
	qe.throttleManager = tm

	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"state":false,"code":770004,"message":"访问频率过高"}`))
	})

	c := NewOpenClient("100195125", "at1", "rt1")
	c.executor = qe

	_, _, err := c.GetFsList(context.Background(), "0", 0, 100)
	if err == nil {
		t.Fatal("expected throttle error")
	}
	if !tm.IsThrottled() {
		t.Fatal("code 770004 should trigger throttle status")
	}
}

func TestRemoteFileDetailRelativePath(t *testing.T) {
	rootCID := "3238787832374488117" // 影视库

	// 场景 1：115 目录 paths 中只有祖先目录链，不包含自身
	d1 := &RemoteFileDetail{
		FileId:   "3251154147730910635",
		FileName: "出包王女",
		Paths: []struct {
			FileId string `json:"file_id"`
			Name   string `json:"file_name"`
		}{
			{FileId: "0", Name: "根目录"},
			{FileId: "3238787832374488117", Name: "影视库"},
			{FileId: "3238787913223892116", Name: "动漫"},
		},
	}
	if got := d1.RelativePath(rootCID); got != "动漫/出包王女" {
		t.Errorf("d1.RelativePath = %q, want %q", got, "动漫/出包王女")
	}

	// 场景 2：祖先中间目录，自身在 paths 末尾
	d2 := &RemoteFileDetail{
		FileId:   "3238787913223892116",
		FileName: "动漫",
		Paths: []struct {
			FileId string `json:"file_id"`
			Name   string `json:"file_name"`
		}{
			{FileId: "0", Name: "根目录"},
			{FileId: "3238787832374488117", Name: "影视库"},
			{FileId: "3238787913223892116", Name: "动漫"},
		},
	}
	if got := d2.RelativePath(rootCID); got != "动漫" {
		t.Errorf("d2.RelativePath = %q, want %q", got, "动漫")
	}

	// 场景 3：根同步目录自身
	d3 := &RemoteFileDetail{
		FileId:   rootCID,
		FileName: "影视库",
		Paths: []struct {
			FileId string `json:"file_id"`
			Name   string `json:"file_name"`
		}{
			{FileId: "0", Name: "根目录"},
			{FileId: rootCID, Name: "影视库"},
		},
	}
	if got := d3.RelativePath(rootCID); got != "" {
		t.Errorf("d3.RelativePath = %q, want %q", got, "")
	}
}

// TestFsListRefreshContinue 验证 access_token 在请求中途过期（40140126）时：
// 自动用 refresh_token 刷新得到新 token，然后对原请求重试成功（同步得以继续）。
func TestFsListRefreshContinue(t *testing.T) {
	var filesCalls int
	var refreshCalls int
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/refreshToken":
			refreshCalls++
			w.Write([]byte(`{"state":true,"data":{"access_token":"at2","refresh_token":"rt2","expires_in":7200}}`))
		case "/open/ufile/files":
			filesCalls++
			switch filesCalls {
			case 1:
				// 第一次用旧 access_token，返回过期错误，应触发刷新
				w.Write([]byte(`{"state":false,"code":40140126,"message":"access_token 校验失败"}`))
			default:
				// 刷新后续请求应使用新 access_token
				if got := r.Header.Get("Authorization"); got != "Bearer at2" {
					t.Errorf("retried request auth = %q, want Bearer at2", got)
				}
				w.Write([]byte(`{"state":true,"path":[],"data":[{"fid":"200","fc":"1","fn":"a.mkv","fs":123,"pc":"pickA"}]}`))
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	c := NewOpenClient("100195125", "at1", "rt1")
	files, _, err := c.GetFsList(context.Background(), "0", 0, 100)
	if err != nil {
		t.Fatalf("expected sync to continue after refresh, got error: %v", err)
	}
	if filesCalls != 2 {
		t.Fatalf("want 2 files calls (original + retried), got %d", filesCalls)
	}
	if refreshCalls == 0 {
		t.Fatal("expected refresh_token to be used once")
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
}

// TestGetDownloadURLsBatch 验证批量换链：多个 pick_code 合并为一次逗号分隔
// 请求；响应按文件 ID 为键、以条目内 pick_code 映射回请求侧；已缓存的
// pick_code 不再发起请求；缺失项（空 URL）不出现在结果中。
func TestGetDownloadURLsBatch(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/ufile/downurl" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		pc := r.PostFormValue("pick_code")
		mu.Lock()
		requests = append(requests, pc)
		mu.Unlock()
		w.Write([]byte(`{"state":true,"data":{
			"111":{"pick_code":"batch-pc-a","url":{"url":"https://cdn/a.mkv"}},
			"222":{"pick_code":"batch-pc-b","url":{"url":"https://cdn/b.jpg"}},
			"333":{"pick_code":"batch-pc-empty","url":{"url":""}}}}`))
	})
	t.Cleanup(func() {
		ClearDownloadURLCache("batch-pc-a")
		ClearDownloadURLCache("batch-pc-b")
		ClearDownloadURLCache("batch-pc-empty")
	})

	c := NewOpenClient("100195125", "at1", "rt1")
	urls, err := c.GetDownloadURLsBatch(context.Background(),
		[]string{"batch-pc-a", "batch-pc-b", "batch-pc-empty", "", "batch-pc-a"}, "")
	if err != nil {
		t.Fatalf("batch downurl: %v", err)
	}
	if urls["batch-pc-a"] != "https://cdn/a.mkv" || urls["batch-pc-b"] != "https://cdn/b.jpg" {
		t.Fatalf("bad urls: %#v", urls)
	}
	if _, ok := urls["batch-pc-empty"]; ok {
		t.Fatalf("empty-url entry should be absent: %#v", urls)
	}
	if _, ok := urls[""]; ok {
		t.Fatalf("empty pickcode should be absent: %#v", urls)
	}
	mu.Lock()
	if len(requests) != 1 || requests[0] != "batch-pc-a,batch-pc-b,batch-pc-empty" {
		mu.Unlock()
		t.Fatalf("unexpected downurl requests: %v", requests)
	}
	mu.Unlock()

	// 第二次调用全部命中缓存：不再发任何请求
	urls2, err := c.GetDownloadURLsBatch(context.Background(), []string{"batch-pc-a", "batch-pc-b"}, "")
	if err != nil {
		t.Fatalf("cached batch downurl: %v", err)
	}
	if urls2["batch-pc-a"] != "https://cdn/a.mkv" {
		t.Fatalf("cached url lost: %#v", urls2)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("cache hit should not issue requests, got %v", requests)
	}
}
