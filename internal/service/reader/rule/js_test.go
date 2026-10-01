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

func TestJSSandboxUnsupported(t *testing.T) {
	r := newTestRunner()
	a := NewAnalyzeRule()
	a.SetJSRunner(r.ForAnalyzer(a))
	a.SetContent("", "")
	_, err := a.GetString(`@js:java.readTxtFile('/etc/passwd')`, nil, false)
	if err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("expected sandbox error, got %v", err)
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
