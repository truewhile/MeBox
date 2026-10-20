// Package helper provides shared HTTP client utilities
// with browser-like headers and Cloudflare/WAF bypass support.
package helper

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultUserAgent 是默认浏览器 User-Agent（用于 HTTP 请求头）。
const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

// maxDecompressedBody 解压后的响应体上限。
//
// 调用方只限制了压缩前的字节数（例如 reader 的 8MiB），而高压缩比响应可以
// 放大几个数量级：一个几 MB 的 gzip 就能在解压时把内存吃光。这里统一兜底，
// 超过上限时按解压失败处理（原样返回压缩字节，由调用方报错）。
const maxDecompressedBody = 32 << 20

// DecompressBody 兜底解压响应体（gzip / deflate）。
//
// 正常情况下用不到：只要不显式设置 Accept-Encoding，net/http 会自己带上 gzip
// 并透明解压。但书源 JSON 的 header 字段、或某些 CDN 的固定策略，都可能让响应
// 带着 Content-Encoding 回来，压缩字节一旦进入规则层，书源的 JSON.parse 就会
// 报 `invalid character '\x1f'`。这里做一次兜底，保证调用方拿到明文。
//
// brotli（br）无法在纯 Go 标准库里解，因此上面不再 advertise br；真遇到 br
// 响应则原样返回，由调用方按失败处理。
func DecompressBody(resp *http.Response, data []byte) []byte {
	if resp == nil || len(data) == 0 {
		return data
	}
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "gzip", "x-gzip":
		r, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return data
		}
		defer r.Close()
		// 不吞掉多个 member：默认 Multistream(true) 会让串接的 member 继续解，
		// 是压缩炸弹的常见放大手段。
		r.Multistream(false)
		if out, ok := readLimited(r); ok {
			return out
		}
	case "deflate":
		// deflate 有两种实际写法：zlib 包装与裸 DEFLATE，依次尝试。
		if out, ok := readLimited(flate.NewReader(bytes.NewReader(data))); ok {
			return out
		}
		if zr, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
			defer zr.Close()
			if out, ok := readLimited(zr); ok {
				return out
			}
		}
	}
	return data
}

// readLimited 读取解压流，超过上限返回 ok=false。
func readLimited(r io.Reader) ([]byte, bool) {
	out, err := io.ReadAll(io.LimitReader(r, maxDecompressedBody+1))
	if err != nil || len(out) > maxDecompressedBody {
		return nil, false
	}
	return out, true
}

// StripAcceptEncoding 移除显式设置的 Accept-Encoding，交回 net/http 管理。
//
// 只有「调用方没设置」时 net/http 才会自动解压，因此任何来源（预设头、书源
// header 字段）带来的 Accept-Encoding 都必须清掉。
func StripAcceptEncoding(h http.Header) {
	h.Del("Accept-Encoding")
}

// NewSiteHTTPClient builds an http.Client honoring per-site policies:
//   - timeout (seconds, defaults to 15)
//   - proxy via HTTP(S)_PROXY environment variables when site.UseProxy is on
//
// When useProxy is false, the client is created without proxy plumbing so
// the request goes out direct, matching the user's checkbox intent.
func NewSiteHTTPClient(timeoutSeconds int, useProxy bool) *http.Client {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 15
	}
	tr := &http.Transport{
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if useProxy {
		tr.Proxy = func(r *http.Request) (*url.URL, error) {
			return ProxyFromEnvironmentOrSystem(r)
		}
	}
	return &http.Client{
		Timeout:   time.Duration(timeoutSeconds) * time.Second,
		Transport: tr,
	}
}

// HTTPHeaderPresets returns a map of realistic browser HTTP headers.
// These mimic a real Chrome browser to avoid WAF/bot detection.
//
// 注意：这里刻意不设置 Accept-Encoding。
//
// net/http 只在「调用方没有显式设置 Accept-Encoding」时才会自己补上 gzip 并
// 透明解压；一旦我们显式写了这个头，它就原样把压缩字节交出来。之前这里写了
// "gzip, deflate, br"，于是所有经历了压缩的响应（静态 JSON、CDN 上的页面等）
// 都会以原始压缩字节进入规则层，书源的 JSON.parse 直接报
// `SyntaxError: invalid character '\x1f'`，表现为「获取最新配置失败」。
//
// 交给 net/http 管理后：请求仍会带 `Accept-Encoding: gzip`（浏览器常见取值），
// 且响应被自动解压；另外也避免服务端挑选我们无法解码的 br。
// 注意：Accept 刻意用 `*/*` 而不是浏览器页面导航的
// `text/html,application/xhtml+xml,...`。
//
// 很多书源接口是 DRF（Django REST framework）一类会做内容协商的后端：当 Accept
// 首选 text/html 时，它按浏览器语义返回「可浏览的 HTML 页面」（HTTP 200），
// 而不是 JSON。引擎再把这份 HTML 交给 JSONPath 解析，结果永远是 0 条——表现为
// 「同一个源在手机 App 里能搜到，在 MeBox 里搜不到」（拷贝系列源就是这个坑）。
// 手机端阅读 App 走 OkHttp，不显式设置 Accept，等价于 `*/*`；这里对齐该行为。
// 需要页面型 Accept 的书源可以在自身 header 里显式声明覆盖。
func HTTPHeaderPresets() map[string]string {
	return map[string]string{
		"User-Agent":                defaultUserAgent,
		"Accept":                    "*/*",
		"Accept-Language":           "zh-CN,zh;q=0.9,en;q=0.8",
		"Connection":                "keep-alive",
		"Upgrade-Insecure-Requests": "1",
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
		"Cache-Control":             "max-age=0",
	}
}
