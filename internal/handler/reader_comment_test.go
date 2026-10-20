package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 段评打开接口的 HTTP 层契约：路由存在、参数校验生效、内网地址被拒。
//
// 正常路径（真的去抓评论页）依赖外部站点，放在服务层解析单测里覆盖；
// 这里只钉住契约与安全边界，避免把测试绑到网络。
func TestReaderOpenCommentRejectsBadURL(t *testing.T) {
	container := newReaderHandlerContainer(t)
	router := registerReaderRoutesForTest(container)

	post := func(body string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/reader/comments/open", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w.Code
	}

	cases := []struct {
		name string
		body string
	}{
		{"缺少 url", `{"book_id":"x"}`},
		{"非 http 协议", `{"book_id":"x","url":"javascript:alert(1)"}`},
		{"回环地址", `{"book_id":"x","url":"http://127.0.0.1/c"}`},
		{"内网地址", `{"book_id":"x","url":"http://192.168.1.1/c"}`},
	}
	for _, c := range cases {
		if code := post(c.body); code != http.StatusBadRequest {
			t.Fatalf("%s：应 400，得到 %d", c.name, code)
		}
	}
}
