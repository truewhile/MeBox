package reader

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// 上游限流（HTTP 429）在免费/低配后端上是常态。书源没声明 retry 时，
// GET 也会自动补一次：第一次 429、第二次正常时，详情必须能拿到数据。
func TestGetBookInfoRetriesUpstream429(t *testing.T) {
	upstream := e2eServer()
	defer upstream.Close()

	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		n := hits[r.URL.Path]
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"projectConcurrencyLimitExceeded","message":"当前访问人数较多"}`))
			return
		}
		resp, err := http.Get(upstream.URL + r.URL.RequestURI())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 32*1024)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	sourceID := importTestSource(t, svc, e2eSourceJSON(srv.URL), srv.URL)

	info, err := svc.GetBookInfo(t.Context(), sourceID, "", srv.URL+"/book/1")
	if err != nil {
		t.Fatalf("第一次 429、重试后应成功: %v", err)
	}
	if info.Name == "" {
		t.Fatalf("重试后详情应有书名，实际为空")
	}
}

// 持续限流时必须报错，而不是把 429 的响应体解析成「空目录 / 0 章」。
func TestGetTocReportsUpstream429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":"projectConcurrencyLimitExceeded","message":"当前访问人数较多"}`))
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	sourceID := importTestSource(t, svc, e2eSourceJSON(srv.URL), srv.URL)

	_, err := svc.GetToc(t.Context(), "u1", sourceID, srv.URL, srv.URL+"/book/1", srv.URL+"/book/1")
	if err == nil {
		t.Fatalf("持续 429 时应返回错误，而不是空目录")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("错误信息应包含 429，实际: %v", err)
	}
}
