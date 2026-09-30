package reader

import (
	"net/url"
	"testing"

	"github.com/truewhile/MeBox/internal/config"
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
