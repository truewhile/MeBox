package reader

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/helper"
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

// TestMediaHTTPClientHasNoOverallTimeout 媒体客户端不能用「整体超时」。
//
// 这是本次故障的第二个根因：http.Client.Timeout 覆盖「读完 body」这一阶段。
// 一个 4MB 的 m4a 在跨境链路上很容易超过 30 秒，超时后 net/http 直接掐断
// 响应体，而调用方的 io.Copy 只是正常返回——表现为**静默截断**（播放器拿到
// 半首歌，判「格式不支持」）或 502。所以媒体客户端必须没有整体超时。
func TestMediaHTTPClientHasNoOverallTimeout(t *testing.T) {
	client := helper.NewMediaHTTPClient(false)
	if client.Timeout != 0 {
		t.Fatalf("媒体 client 的整体 Timeout = %v，必须为 0（大文件下载会被静默截断）", client.Timeout)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型 = %T", client.Transport)
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Fatal("媒体 client 应设置 ResponseHeaderTimeout，避免上游不给首字节时无限挂起")
	}
	// 必须走 HTTP/1.1：腾讯 CDN 的 h2 边缘在长连接大文件上不回 WINDOW_UPDATE，
	// Go 的 h2 初始连接窗口是 1MiB，下载会稳定断在 1MiB/2MiB/3MiB 整数倍处。
	if tr.ForceAttemptHTTP2 {
		t.Error("媒体 client 必须关闭 HTTP/2（1MiB 窗口截断）")
	}
	if tr.TLSNextProto == nil || len(tr.TLSNextProto) != 0 {
		t.Error("媒体 client 需要清空 TLSNextProto，才能确保不用 h2")
	}
}

// TestFetchMediaStreamsLargeBodyWithoutTruncation 大响应体必须完整送达。
//
// 用「慢速分块发送」模拟跨境链路上的大音频：总耗时超过规则请求的 30 秒超时，
// 但媒体通路必须把每一个字节都交出来，绝不静默截断。
func TestFetchMediaStreamsLargeBodyWithoutTruncation(t *testing.T) {
	const totalBytes = 24 << 20 // 24MB
	const chunk = 512 << 10

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(totalBytes))
		payload := make([]byte, chunk)
		for off := 0; off < totalBytes; off += chunk {
			if _, err := w.Write(payload); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	book := &model.ReaderBook{Origin: srv.URL}

	resp, err := svc.FetchMedia(t.Context(), book, srv.URL+"/big.m4a", "")
	if err != nil {
		t.Fatalf("FetchMedia 失败: %v", err)
	}
	defer resp.Body.Close()
	got, readErr := io.Copy(io.Discard, resp.Body)
	if readErr != nil {
		t.Fatalf("读取响应体失败: %v", readErr)
	}
	if got != totalBytes {
		t.Fatalf("交付字节 = %d，期望 %d（少了 %d 字节 → 播放器会判格式不支持）",
			got, totalBytes, int64(totalBytes)-got)
	}
}
