package reader

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 本文件：规则层必须拿到「解压后」的响应体。
//
// 回归背景：HTTPHeaderPresets 曾显式写 `Accept-Encoding: gzip, deflate, br`。
// net/http 只在调用方「没有」设置该头时才会自动补 gzip 并透明解压，显式设置会
// 让它原样交出压缩字节。于是凡是走了压缩的上游（静态 config.json、CDN 页面等）
// 都会把 gzip 字节喂给书源的 JSON.parse，报
//   SyntaxError: invalid character '\x1f' looking for beginning of value
// 表现出来就是「获取最新配置失败：SyntaxError: Unexpected end of JSON input」，
// 并且书源会误判为线路故障，逐条切换全部线路后仍然失败。

// gzipJSONServer 返回一个「客户端支持压缩时才压缩」的站点，模拟真实 CDN。
func gzipJSONServer(t *testing.T, payload string) (*httptest.Server, *string) {
	t.Helper()
	var seenAcceptEncoding string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAcceptEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if strings.Contains(seenAcceptEncoding, "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			_, _ = gz.Write([]byte(payload))
			_ = gz.Close()
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	return srv, &seenAcceptEncoding
}

// readConfigSourceJSON 构造一个用 java.ajax 取配置并 JSON.parse 的书源。
func readConfigSourceJSON(t *testing.T, serverURL, target string, sourceHeader string) string {
	t.Helper()
	loginJS := fmt.Sprintf(`function readConfig() {
  let data = java.ajax(%q);
  let js = JSON.parse(String(data));
  java.longToast('配置版本=' + js.version);
}`, target)
	src := map[string]any{
		"bookSourceUrl":  serverURL,
		"bookSourceName": "压缩测试源",
		"loginUrl":       loginJS,
		"loginUi":        `[{"name":"读配置","type":"button","action":"readConfig()"}]`,
	}
	if sourceHeader != "" {
		src["header"] = sourceHeader
	}
	out, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestReaderJSSeesDecompressedBody 上游压缩的响应必须先解压再进规则层。
func TestReaderJSSeesDecompressedBody(t *testing.T) {
	const payload = `{"version":"20260926"}`
	srv, seenAE := gzipJSONServer(t, payload)
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	sourceID := prepareLoginSource(t, svc, readConfigSourceJSON(t,
		srv.URL, srv.URL+"/static/source_config/config.json", ""))

	res, err := svc.RunLoginAction(t.Context(), readerTestUserID, sourceID, "readConfig()", nil)
	if err != nil {
		t.Fatalf("动作执行失败: %v", err)
	}
	if !res.OK {
		t.Fatalf("动作未成功: %+v", res)
	}
	joined := strings.Join(res.Toasts, "\n")
	if !strings.Contains(joined, "配置版本=20260926") {
		t.Fatalf("规则层拿到的不是解压后的 JSON（压缩字节泄漏到 JSON.parse）: %v", res.Toasts)
	}
	// 必须仍然协商压缩，否则等于用「不压缩」回避问题，真实站点该压还是压
	if !strings.Contains(*seenAE, "gzip") {
		t.Fatalf("请求应携带 gzip（由 net/http 自动添加），实际 %q", *seenAE)
	}
}

// TestReaderStripsAcceptEncodingFromSourceHeader 书源 header 字段里的
// Accept-Encoding 也必须被清掉：显式设置会让 net/http 放弃解压，
// 而且一旦服务端选了 brotli，我们用标准库根本解不出来。
func TestReaderStripsAcceptEncodingFromSourceHeader(t *testing.T) {
	const payload = `{"version":"20260926"}`
	srv, seenAE := gzipJSONServer(t, payload)
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	sourceID := prepareLoginSource(t, svc, readConfigSourceJSON(t,
		srv.URL, srv.URL+"/static/source_config/config.json",
		`{"Accept-Encoding":"br, gzip"}`))

	res, err := svc.RunLoginAction(t.Context(), readerTestUserID, sourceID, "readConfig()", nil)
	if err != nil {
		t.Fatalf("动作执行失败: %v", err)
	}
	if !res.OK {
		t.Fatalf("动作未成功: %+v", res)
	}
	if !strings.Contains(strings.Join(res.Toasts, "\n"), "配置版本=20260926") {
		t.Fatalf("规则层未拿到解压后的 JSON: %v", res.Toasts)
	}
	if strings.Contains(*seenAE, "br") {
		t.Fatalf("书源 header 里的 Accept-Encoding 未被清理（服务端可能回 brotli）: %q", *seenAE)
	}
}
