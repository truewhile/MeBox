package rule

import (
	"strings"
	"testing"
)

// 本文件：URL query 规范化 与 规则 JS 上下文绑定的回归测试。
//
// 背景（光遇聚合）：
//   - 书源把参数拼进 query 后交给 java.ajax，且 POST 也把参数放在 query 里；
//     MeBox 早期只对 GET 重编码 query，POST 的原生中文/引号会直接写进请求行，
//     服务端回 400/空响应，书源判定「线路报错」并把全部线路试一遍。
//   - 书源用 String(baseUrl).startsWith("data:") 判断当前层是不是自搭的参数信封，
//     所以规则 JS 的 baseUrl 必须是「当前处理的页面地址」，不能固定成书源地址。

// TestNormalizeQueryEncodesNonASCII 非 ASCII 与非法字符必须被百分号编码。
func TestNormalizeQueryEncodesNonASCII(t *testing.T) {
	raw := `https://example.com/detail?id=abc&source=番茄&variable={"custom":""}`
	got := normalizeQuery(raw, "")
	if strings.Contains(got, "番茄") {
		t.Fatalf("中文未被编码: %s", got)
	}
	if !strings.Contains(got, "source=%E7%95%AA%E8%8C%84") {
		t.Fatalf("中文编码结果异常: %s", got)
	}
	if strings.Contains(got, `{"custom":""}`) {
		t.Fatalf("引号未被编码: %s", got)
	}
	// 分隔符与等号必须保留，否则参数结构会散掉
	if !strings.Contains(got, "&") || !strings.Contains(got, "=") {
		t.Fatalf("query 结构被破坏: %s", got)
	}
}

// TestNormalizeQueryKeepsEncoded 已编码好的 query 原样保留，不二次编码。
func TestNormalizeQueryKeepsEncoded(t *testing.T) {
	raw := "https://example.com/s?q=%E5%AE%A0%E9%AD%85&page=1"
	if got := normalizeQuery(raw, ""); got != raw {
		t.Fatalf("已编码 query 被改动:\n got=%s\nwant=%s", got, raw)
	}
}

// TestNormalizeQueryLeavesDataURI data: 地址的载荷不是 query，不能动。
func TestNormalizeQueryLeavesDataURI(t *testing.T) {
	raw := `data:;base64,eyJhIjoxfQ==`
	if got := normalizeQuery(raw, ""); got != raw {
		t.Fatalf("data 地址被改动: %s", got)
	}
}

// TestParseAnalyzeUrlEncodesPOSTQuery POST 的 query 也要编码。
// 回归：之前只有 GET 分支做重编码，POST 的原生中文直接上线。
func TestParseAnalyzeUrlEncodesPOSTQuery(t *testing.T) {
	raw := `https://example.com/detail?source=番茄,{"method":"POST","headers":{"Content-Type":"application/json"},"body":"{\"html\":\"\"}"}`
	req, err := ParseAnalyzeUrlWithJS(raw, "", 0, "https://example.com", NewJSRunner(JSConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" {
		t.Fatalf("method = %q", req.Method)
	}
	if strings.Contains(req.URL, "番茄") {
		t.Fatalf("POST query 未编码: %s", req.URL)
	}
	if !strings.Contains(req.URL, "source=%E7%95%AA%E8%8C%84") {
		t.Fatalf("POST query 编码异常: %s", req.URL)
	}
	if req.Body != `{"html":""}` {
		t.Fatalf("body 被改动: %q", req.Body)
	}
}

// TestRuleJSBaseUrlPrefersPageURL 规则 JS 的 baseUrl 应是「当前页面地址」。
//
// 书源用 String(baseUrl).startsWith("data:") 判断当前层是不是自搭的参数信封；
// 若把 baseUrl 固定成书源地址，书源会走 else 分支把 hex 原文当结果返回。
func TestRuleJSBaseUrlPrefersPageURL(t *testing.T) {
	const pageURL = `data:;base64,eyJhIjoxfQ==`
	r := NewJSRunner(JSConfig{BaseURL: "https://source.example.com"})
	ar := NewAnalyzeRule()
	ar.SetContent("body", pageURL)

	v, err := r.Run(ar, `baseUrl`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := anyToString(v); got != pageURL {
		t.Fatalf("baseUrl = %q，期望页面地址 %q", got, pageURL)
	}
	if !strings.HasPrefix(anyToString(v), "data:") {
		t.Fatal("书源的 data: 分支判断会失效")
	}
}

// TestRuleJSBaseUrlFallsBackToSource 页面地址为空时回退书源地址。
func TestRuleJSBaseUrlFallsBackToSource(t *testing.T) {
	r := NewJSRunner(JSConfig{BaseURL: "https://source.example.com"})
	v, err := r.Run(NewAnalyzeRule(), `baseUrl`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := anyToString(v); got != "https://source.example.com" {
		t.Fatalf("baseUrl = %q", got)
	}
}

// TestRuleJSBookObject 规则 JS 的 book 对象要具备书源依赖的成员。
//
// 回归：早期只绑了 {"name": ...}，书源一碰 book.setUseReplaceRule(false)
// 就 TypeError，整段详情/目录规则 JS 直接失败。
func TestRuleJSBookObject(t *testing.T) {
	state := NewMemoryState()
	r := NewJSRunner(JSConfig{State: state})
	ar := NewAnalyzeRule()
	ar.SetBookContext("宠魅", nil)
	ar.SetChapterContext("第1集", nil)
	ar.SetChapterIndex(0)

	js := `(function(){
	  book.setUseReplaceRule(false);              // 早期会 TypeError
	  var v = String(book.getVariable('custom')); // 缺省必须是 ""（不是 "null"）
	  book.type = 32;                             // 听书源这样声明类型
	  book.imageStyle = 'TEXT';
	  return book.name + '|' + v + '|' + book.type + '|' + chapter.title;
	})()`
	v, err := r.Run(ar, js, nil, "")
	if err != nil {
		t.Fatalf("book 对象成员缺失: %v", err)
	}
	if got := anyToString(v); got != "宠魅||32|第1集" {
		t.Fatalf("book 行为异常: %q", got)
	}
	// 书源声明的类型要被服务层读回
	if bt, ok := ar.BookTypeOverride(); !ok || bt != 32 {
		t.Fatalf("BookTypeOverride = %v/%v，期望 32", bt, ok)
	}
	// putVariable 写回后 getVariable 能读到
	if _, err := r.Run(ar, `book.putVariable('custom','v1')`, nil, ""); err != nil {
		t.Fatal(err)
	}
	v, err = r.Run(ar, `String(book.getVariable('custom'))`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := anyToString(v); got != "v1" {
		t.Fatalf("putVariable 后 getVariable = %q", got)
	}
}
