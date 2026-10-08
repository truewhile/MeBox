package rule

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ─── 基础执行与绑定 ─────────────────────────────────────────────────────────

func newTestRunner() *JSRunner {
	return NewJSRunner(JSConfig{Key: "斗", Page: 2})
}

func TestJSBasicEval(t *testing.T) {
	r := newTestRunner()
	a := NewAnalyzeRule()
	v, err := r.Run(a, "1 + 2", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if anyToString(v) != "3" {
		t.Fatalf("1+2 = %v", v)
	}
	// key/page 绑定
	v, err = r.Run(a, "key + page", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if anyToString(v) != "斗2" {
		t.Fatalf("key+page = %v", v)
	}
}

func TestJSTimeoutInterrupt(t *testing.T) {
	r := NewJSRunner(JSConfig{Timeout: 200 * 1e6}) // 200ms
	_, err := r.Run(NewAnalyzeRule(), "while(true){}", nil, "")
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

// TestJSTimeoutPausedDuringFetch 网络等待期间不能被 JS 超时打断。
//
// 回归：书源把「线路重试」写在规则 JS 里（光遇聚合的 request() 串行试 7 条线路，
// 单条最长等到 HTTP 客户端超时）。看门狗不暂停的话，整条规则会被 10s 的 JS 超时
// 中断，而这个中断是 goja 的 Go panic，书源自己写的 try/catch 接不住——表现就是
// 「线路还在重试，接口已经 400」。
func TestJSTimeoutPausedDuringFetch(t *testing.T) {
	r := NewJSRunner(JSConfig{
		Timeout: 100 * time.Millisecond,
		Fetch: func(req *Request) (string, string, int, error) {
			time.Sleep(400 * time.Millisecond) // 远超过 JS 超时：模拟慢线路
			return `{"content":"正文"}`, req.URL, 200, nil
		},
	})
	ar := NewAnalyzeRule()
	v, err := r.Run(ar, `(function(){
		try { return java.ajax('https://slow.example.com/content'); }
		catch (e) { return 'caught:' + e; }
	})()`, nil, "")
	if err != nil {
		t.Fatalf("网络等待期间不应触发 JS 超时: %v", err)
	}
	if got := anyToString(v); !strings.Contains(got, "正文") {
		t.Fatalf("ajax 返回值 = %q，期望上游正文", got)
	}

	// 暂停只覆盖网络等待：回到 JS 里的纯 CPU 死循环仍然要被超时打断。
	_, err = r.Run(ar, `java.ajax('https://slow.example.com/content'); while(true){}`, nil, "")
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("网络等待之后的 CPU 死循环应当仍然超时，实际: %v", err)
	}
}

// ─── 规则引擎中的 JS（{{}} / @js: / <js>） ──────────────────────────────────

func TestAnalyzeRuleJSEval(t *testing.T) {
	r := newTestRunner()
	a := NewAnalyzeRule()
	a.SetJSRunner(r.ForAnalyzer(a))
	a.SetContent("正文内容", "http://x.com")

	// {{js}} 内嵌
	got, err := a.GetString(`{{baseUrl}}/next`, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://x.com/next" {
		t.Fatalf("{{baseUrl}} = %q", got)
	}
	// @js: 前缀
	got, err = a.GetString(`@js:'hello ' + (40 + 2)`, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello 42" {
		t.Fatalf("@js = %q", got)
	}
	// <js></js> 块
	got, err = a.GetString(`<js>"结果：" + result</js>`, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "结果：正文内容" {
		t.Fatalf("<js> = %q", got)
	}
}

func TestJSGetStringBridge(t *testing.T) {
	r := newTestRunner()
	a := NewAnalyzeRule()
	a.SetJSRunner(r.ForAnalyzer(a))
	a.SetContent(testHTML, "http://x.com")
	got, err := a.GetString(`<js>java.getString("class.item.0@tag.a@text")</js>`, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "斗破苍穹" {
		t.Fatalf("java.getString = %q", got)
	}
}

// ─── 编码与摘要 ─────────────────────────────────────────────────────────────

func TestJSEncodeFunctions(t *testing.T) {
	r := newTestRunner()
	a := NewAnalyzeRule()
	a.SetJSRunner(r.ForAnalyzer(a))
	a.SetContent("", "") // 规则循环要求 content 非空（与 legado 语义一致）

	cases := []struct{ js, want string }{
		{`@js:java.base64Encode('你好')`, base64.StdEncoding.EncodeToString([]byte("你好"))},
		{`@js:java.base64Decode('` + base64.StdEncoding.EncodeToString([]byte("你好")) + `')`, "你好"},
		{`@js:java.hexEncodeToString('AB')`, "4142"},
		{`@js:java.hexDecodeToString('4142')`, "AB"},
		{`@js:java.md5Encode('abc')`, "900150983cd24fb0d6963f7d28e17f72"},
		{`@js:java.md5Encode16('abc')`, "3cd24fb0d6963f7d"},
		{`@js:java.digestHex('abc','SHA-256')`, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	}
	for _, c := range cases {
		got, err := a.GetString(c.js, nil, false)
		if err != nil {
			t.Fatalf("%s: %v", c.js, err)
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.js, got, c.want)
		}
	}
}

// ─── 对称加解密 ─────────────────────────────────────────────────────────────

func TestJSSymmetricCryptoRoundTrip(t *testing.T) {
	r := newTestRunner()
	a := NewAnalyzeRule()
	a.SetJSRunner(r.ForAnalyzer(a))
	a.SetContent("", "")

	script := `
var key = '0123456789abcdef';
var iv = 'abcdef9876543210';
var c = java.createSymmetricCrypto('AES/CBC/PKCS5Padding', key, iv);
var enc = c.encryptBase64('测试明文内容');
c.decryptStr(enc)
`
	got, err := a.GetString(`@js:`+script, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "测试明文内容" {
		t.Fatalf("roundtrip = %q", got)
	}
}

func TestAESVector(t *testing.T) {
	// 用 Go 标准库生成固定密文，验证 decryptAuto + PKCS7 对齐
	block, _ := aes.NewCipher([]byte("0123456789abcdef"))
	iv := []byte("abcdef9876543210")
	plain := []byte("hello legado")
	padded := applyPadding(plain, block.BlockSize(), "PKCS5Padding")
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	ct := base64.StdEncoding.EncodeToString(out)

	r := newTestRunner()
	a := NewAnalyzeRule()
	a.SetJSRunner(r.ForAnalyzer(a))
	a.SetContent("", "")
	got, err := a.GetString(`@js:java.aesBase64DecodeToString('`+ct+`', '0123456789abcdef', 'AES/CBC/PKCS5Padding', 'abcdef9876543210')`, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello legado" {
		t.Fatalf("aes decode = %q", got)
	}
	if _, err := hex.DecodeString("00"); err != nil {
		t.Fatal(err)
	}
}

func TestECBCipher(t *testing.T) {
	r := newTestRunner()
	a := NewAnalyzeRule()
	a.SetJSRunner(r.ForAnalyzer(a))
	a.SetContent("", "")
	script := `
var c = java.createSymmetricCrypto('AES/ECB/PKCS5Padding', '0123456789abcdef', '');
var enc = c.encryptBase64('ECB模式测试');
c.decryptStr(enc)
`
	got, err := a.GetString(`@js:`+script, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ECB模式测试" {
		t.Fatalf("ecb roundtrip = %q", got)
	}
}

// ─── 网络桥（httptest） ─────────────────────────────────────────────────────

func TestJSAjaxBridge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "tk" {
			http.Error(w, "no token", 401)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":"ok"}`))
	}))
	defer srv.Close()

	fetchCalled := false
	r := NewJSRunner(JSConfig{
		Fetch: func(req *Request) (string, string, int, error) {
			fetchCalled = true
			// 经完整 ParseAnalyzeUrl 执行（headers 已在 req 上）
			resp, err := http.Get(req.URL)
			if err != nil {
				return "", "", 0, err
			}
			defer resp.Body.Close()
			// 补上测试头
			req.Headers["X-Token"] = "tk"
			return `{"code":0,"data":"ok"}`, req.URL, 200, nil
		},
	})
	a := NewAnalyzeRule()
	a.SetJSRunner(r.ForAnalyzer(a))
	a.SetContent("", "")

	got, err := a.GetString(`@js:java.get('`+srv.URL+`/api', {"X-Token":"tk"}).body()`, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !fetchCalled || !strings.Contains(got, `"ok"`) {
		t.Fatalf("ajax bridge = %q", got)
	}

	// ajax 返回字符串
	got, err = a.GetString(`@js:java.ajax('`+srv.URL+`/api')`, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"ok"`) {
		t.Fatalf("ajax = %q", got)
	}
}

// ─── URL 规则 JS ────────────────────────────────────────────────────────────

func TestParseAnalyzeUrlWithJSBlocks(t *testing.T) {
	r := NewJSRunner(JSConfig{Key: "斗罗", Page: 3})
	req, err := ParseAnalyzeUrlWithJS(`<js>'https://e.com/search?q=' + encodeURIComponent(key) + '&p=' + page</js>`, "斗罗", 3, "", r)
	if err != nil {
		t.Fatal(err)
	}
	if req.Unsupported != nil {
		t.Fatalf("unsupported: %v", req.Unsupported)
	}
	if !strings.Contains(req.URL, "p=3") || !strings.Contains(req.URL, "q=") {
		t.Fatalf("url = %q", req.URL)
	}
}

func TestParseAnalyzeUrlJSTemplate(t *testing.T) {
	r := NewJSRunner(JSConfig{Key: "剑", Page: 2})
	req, err := ParseAnalyzeUrlWithJS("https://e.com/api?page={{page + 1}}&kw={{key}}", "剑", 2, "", r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.URL, "page=3") || !strings.Contains(req.URL, "kw=%E5%89%91") {
		t.Fatalf("url = %q", req.URL)
	}
}

func TestParseAnalyzeUrlBodyJs(t *testing.T) {
	r := newTestRunner()
	// 与 legado 一致：option.js 里通过 result 引用当前 url
	req, err := ParseAnalyzeUrlWithJS(`https://e.com/x,{"bodyJs":"result + '!'","js":"result + '#anchor'"}`, "", 0, "", r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.URL, "#anchor") {
		t.Fatalf("url = %q", req.URL)
	}
	if req.BodyJsFn == nil {
		t.Fatal("BodyJsFn should be set")
	}
	if got := req.BodyJsFn("body-x"); got != "body-x!" {
		t.Fatalf("bodyJs = %q", got)
	}
}

func TestJSUnsupportedStillWorks(t *testing.T) {
	// runner 为 nil 时，P0 行为保持：标记 Unsupported
	req, err := ParseAnalyzeUrl(`<js>'x'</js>`, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Unsupported == nil {
		t.Fatal("expected unsupported without runner")
	}
}

// ─── 沙箱边界 ───────────────────────────────────────────────────────────────

// TestJSSandboxUnsupported 服务端不接受书源读取宿主任意文件。
//
// 文件类接口（cacheFile/downloadFile/readTxtFile/...）已实现，但路径被限制在
// 书源自己的缓存目录内：越权路径安全地返回空，而不是把 /etc/passwd 交出去，
// 也不应因为越权路径抛异常（否则书源会误判为「线路故障」反复重试）。
// 真正无实现的能力（解压包、无头浏览器等）继续明确抛「不支持」。
func TestJSSandboxUnsupported(t *testing.T) {
	r := newTestRunner()
	a := NewAnalyzeRule()
	a.SetJSRunner(r.ForAnalyzer(a))
	a.SetContent("", "")
	got, err := a.GetString(`@js:java.readTxtFile('/etc/passwd')`, nil, false)
	if err != nil {
		t.Fatalf("越权路径应安全返回空而不是抛异常: %v", err)
	}
	if strings.Contains(got, "root:") || strings.Contains(got, "/bin/") {
		t.Fatalf("沙箱被突破，读到了宿主文件: %q", got)
	}
	for _, call := range []string{`java.unzipFile('/tmp/a.zip')`, `java.webView('x')`} {
		if _, err := a.GetString(`@js:`+call, nil, false); err == nil || !strings.Contains(err.Error(), "不支持") {
			t.Fatalf("%s 应继续抛「不支持」，实际 %v", call, err)
		}
	}
}

// TestRuleJSTopLevelReturn legado 允许规则 JS 顶层 return；无 return 时仍取最后
// 一条语句的值（`@js:1+2` → 3）。
func TestRuleJSTopLevelReturn(t *testing.T) {
	r := NewJSRunner(JSConfig{})
	ar := NewAnalyzeRule()

	v, err := r.Run(ar, "if (1) { return '早退'; }\nreturn '兜底';", nil, "")
	if err != nil {
		t.Fatalf("顶层 return 应可用: %v", err)
	}
	if got := anyToString(v); got != "早退" {
		t.Fatalf("top-level return = %q, want 早退", got)
	}

	v2, err := r.Run(ar, "1 + 2", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := anyToString(v2); got != "3" {
		t.Fatalf("最后一条语句的值 = %q, want 3", got)
	}
}

func TestJSTimeFormatShape(t *testing.T) {
	r := newTestRunner()
	a := NewAnalyzeRule()
	a.SetJSRunner(r.ForAnalyzer(a))
	a.SetContent("", "")
	got, err := a.GetString(`@js:java.timeFormat(1700000000000, 'yyyy-MM-dd HH:mm')`, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$`).MatchString(got) {
		t.Fatalf("timeFormat = %q", got)
	}
}
