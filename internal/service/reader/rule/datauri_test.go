package rule

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// 本文件：data: 参数信封 + URL 选项 type 的回归测试。
//
// 背景：聚合类书源把上一阶段的结果打包成
//   `data:;base64,<base64(参数JSON)>,{"type":"gysearch"}`
// 当 URL 用。legado 对它的处理是：① 本地 base64 解码取字节；② 因为声明了 type，
// 把字节按 hex 返回。MeBox 早期把带 type 的 URL 直接判为「不支持的能力」，
// 导致这类书源第一步就报「书源 URL 声明了不支持的 type」。

func TestDecodeDataURI(t *testing.T) {
	payload := `{"key":"宠魅","page":1}`
	enc := base64.StdEncoding.EncodeToString([]byte(payload))

	cases := map[string]string{
		"标准 base64":     "data:;base64," + enc,
		"带 MIME":        "data:application/json;base64," + enc,
		"无 padding":     "data:;base64," + base64.RawStdEncoding.EncodeToString([]byte(payload)),
		"URL-safe":      "data:;base64," + base64.URLEncoding.EncodeToString([]byte(payload)),
		"含空白/换行":       "data:;base64," + wrapBase64(enc),
	}
	for name, raw := range cases {
		got, ok := DecodeDataURI(raw)
		if !ok {
			t.Fatalf("%s: 应能解码", name)
		}
		if string(got) != payload {
			t.Fatalf("%s: 解码结果 %q", name, string(got))
		}
	}

	for _, raw := range []string{
		"https://example.com/a",
		"data:text/plain,hello", // 非 base64 形式
		"",
	} {
		if _, ok := DecodeDataURI(raw); ok {
			t.Fatalf("不应识别为 base64 数据地址: %q", raw)
		}
	}
}

// TestParseAnalyzeUrlDataTypeDeclaration 带 type 的 data: 地址应被解析为
// 「hex 返回」的请求，而不是不支持的能力。
func TestParseAnalyzeUrlDataTypeDeclaration(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte(`{"key":"宠魅"}`))
	raw := "data:;base64," + payload + `,{"type":"gysearch"}`

	req, err := ParseAnalyzeUrlWithJS(raw, "", 0, "https://example.com", NewJSRunner(JSConfig{}))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if req.Unsupported != nil {
		t.Fatalf("带 type 的地址不应被判为不支持: %v", req.Unsupported)
	}
	if !req.HexBody {
		t.Fatal("声明了 type 时应按 hex 返回响应")
	}
	// 选项里的 JSON 不能被当成数据载荷的一部分
	if strings.Contains(req.URL, `"type"`) {
		t.Fatalf("选项未与地址分离: %q", req.URL)
	}
	if _, ok := DecodeDataURI(req.URL); !ok {
		t.Fatalf("URL 不是可解码的数据地址: %q", req.URL)
	}
}

// TestParseAnalyzeUrlWithoutType 没有 type 时不应进入 hex 模式。
func TestParseAnalyzeUrlWithoutType(t *testing.T) {
	req, err := ParseAnalyzeUrlWithJS("https://example.com/a?x=1", "", 0, "https://example.com", NewJSRunner(JSConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	if req.HexBody {
		t.Fatal("未声明 type 时不应按 hex 返回")
	}
	if req.URL != "https://example.com/a?x=1" {
		t.Fatalf("URL = %q", req.URL)
	}
}

// TestEncodeRuleBody 声明 type 时返回原始字节的 hex，否则返回解码后的文本。
func TestEncodeRuleBody(t *testing.T) {
	raw := []byte(`{"key":"宠魅"}`)

	hexReq := &Request{HexBody: true}
	got := EncodeRuleBody(hexReq, raw, "")
	if got != hex.EncodeToString(raw) {
		t.Fatalf("hex 模式 = %q", got)
	}
	// 书源会用它还原
	back, err := hex.DecodeString(got)
	if err != nil || string(back) != string(raw) {
		t.Fatalf("hex 往返失败: %v %q", err, string(back))
	}

	plainReq := &Request{}
	if got := EncodeRuleBody(plainReq, raw, ""); got != string(raw) {
		t.Fatalf("非 hex 模式 = %q", got)
	}
}
