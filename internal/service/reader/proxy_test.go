package reader

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
)

// TestProxyURLSignRoundTrip 验证媒体代理签名：往返还原 + 防篡改。
func TestProxyURLSignRoundTrip(t *testing.T) {
	s := &ReaderService{cfg: &config.Config{Secrets: config.SecretsConfig{JWTSecret: "test-secret"}}}
	const raw = "https://cdn.example.com/audio/ep1.mp3?token=abc"
	signed := s.ProxyURL("book-1", raw)

	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("b") != "book-1" {
		t.Fatalf("bookID = %q", q.Get("b"))
	}
	restored, err := s.VerifyProxyURL(q.Get("b"), q.Get("u"), q.Get("s"))
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if restored != raw {
		t.Fatalf("restored = %q", restored)
	}
	// 篡改签名必须被拒绝
	if _, err := s.VerifyProxyURL(q.Get("b"), q.Get("u"), "00000000000000000000000000000000"); err == nil {
		t.Fatal("expected signature rejection")
	}
	// 换书签必须被拒绝
	if _, err := s.VerifyProxyURL("book-2", q.Get("u"), q.Get("s")); err == nil {
		t.Fatal("expected cross-book signature rejection")
	}
	// 已是代理地址的不再二次包裹
	if again := s.ProxyURL("book-1", signed); again != signed {
		t.Fatalf("double wrap: %q", again)
	}
}

// TestSourceReferer 验证默认 Referer 只在 origin 是真正的 http(s) 书源地址时
// 才生成。聚合类书源的 origin 是显示名（如「光遇聚合」），拿它当 Referer 会被
// 图床判盗链，返回一张「请到本网站阅读」的占位图。
func TestSourceReferer(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		want   string
	}{
		{"普通书源地址", "https://www.example.com", "https://www.example.com/"},
		{"带尾斜杠", "https://www.example.com/", "https://www.example.com/"},
		{"带子路径", "https://www.example.com/site", "https://www.example.com/site/"},
		{"聚合书源显示名", "光遇聚合", ""},
		{"空值", "", ""},
		{"只有空白", "   ", ""},
		{"非 http 协议", "ftp://example.com", ""},
		{"没有主机", "https://", ""},
	}
	for _, c := range cases {
		if got := sourceReferer(c.origin); got != c.want {
			t.Errorf("%s: sourceReferer(%q) = %q，期望 %q", c.name, c.origin, got, c.want)
		}
	}
}

// TestFetchMediaRetriesTransientFailure 验证图床偶发 403（并发限流）会被重试掉，
// 否则漫画一屏并发取图时总有几页留成破图。
func TestFetchMediaRetriesTransientFailure(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write([]byte("real-image-bytes"))
	}))
	defer srv.Close()

	s := &ReaderService{http: &http.Client{Timeout: 10 * time.Second}}
	book := &model.ReaderBook{Origin: srv.URL}

	resp, err := s.FetchMedia(t.Context(), book, srv.URL+"/1.webp", "")
	if err != nil {
		t.Fatalf("FetchMedia 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（前两次 403 应被重试掉）", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "real-image-bytes" {
		t.Fatalf("body = %q", data)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("上游请求次数 = %d，期望 3", got)
	}
}

// TestFetchMediaDoesNotRetryNotFound 验证确定性的 4xx（如图片不存在）不做无谓重试，
// 避免首屏白白多等两轮退避。
func TestFetchMediaDoesNotRetryNotFound(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	s := &ReaderService{http: &http.Client{Timeout: 10 * time.Second}}
	book := &model.ReaderBook{Origin: srv.URL}

	resp, err := s.FetchMedia(t.Context(), book, srv.URL+"/missing.webp", "")
	if err != nil {
		t.Fatalf("FetchMedia 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("上游请求次数 = %d，期望 1（404 不该重试）", got)
	}
}
