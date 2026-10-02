package reader

import (
	"testing"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

// TestApplyHeadersEvaluatesJSHeader 覆盖拷贝漫画（优++）这类书源：
// header 是 @js: 规则，运行时才用 baseUrl 拼出 platform/version/referer。
// 此前 applyHeaders 只做 json.Unmarshal，JS 形态被整体丢掉，请求少了这些
// 必需头，上游同样回 200 但结果是空列表 —— 表现为「源能导入却搜不到内容」。
func TestApplyHeadersEvaluatesJSHeader(t *testing.T) {
	svc, _ := newLoginTestService(t)
	const header = "@js:\nJSON.stringify({\"platform\":\"1\",\"version\":\"9.9.9\",\"referer\":baseUrl})"
	src := &model.ReaderBookSource{
		Name:      "拷贝漫画（优++）",
		SourceURL: "https://www.mangacopy.com/",
		Header:    header,
	}
	bs := &BookSource{BookSourceURL: src.SourceURL, BookSourceName: src.Name, Header: strPtr(header)}

	sess := svc.newSession(t.Context(), src, bs)
	defer sess.close()

	req := &rule.Request{Method: "GET", URL: "https://api.mangacopy.com/api/v3/search/comic", Headers: map[string]string{}}
	sess.applyHeaders(req, sess.runner("火影", 1))

	if got := req.Headers["platform"]; got != "1" {
		t.Fatalf("platform = %q，期望 \"1\"（JS 形态的 header 没有被执行）", got)
	}
	if got := req.Headers["version"]; got != "9.9.9" {
		t.Fatalf("version = %q，期望 \"9.9.9\"", got)
	}
	// baseUrl 必须绑成书源地址（对应 legado getHeaderMap 里的 getKey()）
	if got := req.Headers["referer"]; got != "https://www.mangacopy.com/" {
		t.Fatalf("referer = %q，期望书源地址", got)
	}
}

// TestApplyHeadersKeepsPlainJSONHeader 直接写 JSON 的 header 不能回归。
func TestApplyHeadersKeepsPlainJSONHeader(t *testing.T) {
	svc, _ := newLoginTestService(t)
	header := `{"User-Agent":"MyBox/1.0","X-Token":"abc"}`
	src := &model.ReaderBookSource{Name: "纯 JSON 源", SourceURL: "https://example.com/", Header: header}
	bs := &BookSource{BookSourceURL: src.SourceURL, Header: strPtr(header)}

	sess := svc.newSession(t.Context(), src, bs)
	defer sess.close()

	req := &rule.Request{Method: "GET", URL: "https://example.com/search", Headers: map[string]string{}}
	sess.applyHeaders(req, sess.runner("", 0))

	if got := req.Headers["User-Agent"]; got != "MyBox/1.0" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := req.Headers["X-Token"]; got != "abc" {
		t.Fatalf("X-Token = %q", got)
	}
}

// TestApplyHeadersSurvivesBrokenJSHeader 坏掉的 header JS 不能 panic，
// 也不该把半成品请求头发出去。
func TestApplyHeadersSurvivesBrokenJSHeader(t *testing.T) {
	svc, _ := newLoginTestService(t)
	const header = "@js:\nthrow new Error('boom')"
	src := &model.ReaderBookSource{Name: "坏 header 源", SourceURL: "https://example.com/", Header: header}
	bs := &BookSource{BookSourceURL: src.SourceURL, Header: strPtr(header)}

	sess := svc.newSession(t.Context(), src, bs)
	defer sess.close()

	req := &rule.Request{Method: "GET", URL: "https://example.com/search", Headers: map[string]string{}}
	sess.applyHeaders(req, sess.runner("", 0))

	if len(req.Headers) != 0 {
		t.Fatalf("求值失败的 header 不应写入任何头，实际: %v", req.Headers)
	}
}

func strPtr(s string) *string { return &s }
