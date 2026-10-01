package helper

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"testing"
)

// 本文件：压缩相关的回归测试。
//
// 背景：预设头里曾显式写 `Accept-Encoding: gzip, deflate, br`，导致 net/http
// 不再自动解压（它只在调用方没设置该头时才解压），压缩字节直接进入规则层。

// TestHeaderPresetsDoNotSetAcceptEncoding 预设头不得设置 Accept-Encoding。
// 一旦设置，net/http 的透明解压就失效，压缩响应会以原始字节交给上层。
func TestHeaderPresetsDoNotSetAcceptEncoding(t *testing.T) {
	for k := range HTTPHeaderPresets() {
		if http.CanonicalHeaderKey(k) == "Accept-Encoding" {
			t.Fatal("HTTPHeaderPresets 不应设置 Accept-Encoding（会让 net/http 放弃自动解压）")
		}
	}
}

// TestStripAcceptEncoding 任何来源带来的 Accept-Encoding 都应被清掉。
func TestStripAcceptEncoding(t *testing.T) {
	h := http.Header{}
	h.Set("Accept-Encoding", "br, gzip")
	StripAcceptEncoding(h)
	if got := h.Get("Accept-Encoding"); got != "" {
		t.Fatalf("Accept-Encoding 未清理: %q", got)
	}
}

// TestDecompressBody gzip / deflate 响应体应被还原成明文。
func TestDecompressBody(t *testing.T) {
	payload := `{"version":"20260926"}`

	t.Run("gzip", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte(payload))
		_ = gz.Close()

		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("Content-Encoding", "gzip")
		if got := string(DecompressBody(resp, buf.Bytes())); got != payload {
			t.Fatalf("gzip 解压 = %q", got)
		}
	})

	t.Run("未压缩原样返回", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{}}
		if got := string(DecompressBody(resp, []byte(payload))); got != payload {
			t.Fatalf("未压缩响应被改动: %q", got)
		}
	})

	t.Run("坏数据不 panic", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("Content-Encoding", "gzip")
		raw := []byte("not-gzip")
		if got := string(DecompressBody(resp, raw)); got != "not-gzip" {
			t.Fatalf("解压失败时应原样返回，实际 %q", got)
		}
	})

	t.Run("nil resp", func(t *testing.T) {
		if got := string(DecompressBody(nil, []byte(payload))); got != payload {
			t.Fatalf("nil resp 应原样返回，实际 %q", got)
		}
	})
}
