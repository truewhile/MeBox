package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

func makeTestPairPEM(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"localhost"},
		// 带上回环 IP，HTTPS 客户端就能在校验证书的前提下连 127.0.0.1:port。
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	keyPEM = strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})))
	return certPEM, keyPEM
}

func newTestServerManager(t *testing.T) *serverManager {
	t.Helper()
	cfg := &config.Config{}
	cfg.App.Port = 18081
	return newServerManager(cfg, zap.NewNop(), http.NewServeMux())
}

func TestDesiredPairModes(t *testing.T) {
	m := newTestServerManager(t)

	if p, err := m.desiredPair(); err != nil || p != nil {
		t.Fatalf("disabled should be nil pair, got p=%v err=%v", p, err)
	}

	certPEM, keyPEM := makeTestPairPEM(t)
	m.cfg.App.HTTPSEnabled = true
	m.cfg.App.SSLCert, m.cfg.App.SSLKey = certPEM, keyPEM
	p, err := m.desiredPair()
	if err != nil || p == nil || p.version == "" {
		t.Fatalf("content pair failed: p=%v err=%v", p, err)
	}

	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, []byte(certPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	m.cfg.App.SSLCert, m.cfg.App.SSLKey = "", ""
	m.cfg.App.SSLCertPath, m.cfg.App.SSLKeyPath = certPath, keyPath
	p2, err := m.desiredPair()
	if err != nil || p2 == nil {
		t.Fatalf("path pair failed: %v", err)
	}

	m.cfg.App.SSLKeyPath = filepath.Join(dir, "nope.pem")
	if _, err := m.desiredPair(); err == nil {
		t.Fatal("expected error when key file missing")
	}
	m.cfg.App.SSLKeyPath = keyPath

	// 替换文件（换一套新的有效证书）后版本号应变化，触发热更新。
	newCert, newKey := makeTestPairPEM(t)
	if err := os.WriteFile(certPath, []byte(newCert), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(newKey), 0o600); err != nil {
		t.Fatal(err)
	}
	p3, err := m.desiredPair()
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if p3.version == p2.version {
		t.Fatal("version should change after files replaced")
	}
}

// 健康检查依赖该文件判断该用 http 还是 https 探测，内容与当前监听协议必须一致，
// 否则容器会一直显示 unhealthy 并刷 TLS handshake error 日志。
func TestWriteSchemeState(t *testing.T) {
	m := newTestServerManager(t)
	m.cfg.App.DataDir = t.TempDir()
	path := filepath.Join(m.cfg.App.DataDir, httpSchemeFileName)

	m.writeSchemeState(nil)
	if data, err := os.ReadFile(path); err != nil || strings.TrimSpace(string(data)) != "http" {
		t.Fatalf("plain HTTP state: data=%q err=%v", data, err)
	}

	m.writeSchemeState(&tlsPair{})
	if data, err := os.ReadFile(path); err != nil || strings.TrimSpace(string(data)) != "https" {
		t.Fatalf("HTTPS state: data=%q err=%v", data, err)
	}

	// DataDir 为空时不应写到工作目录（测试/裸跑二进制时会污染仓库）。
	m.cfg.App.DataDir = ""
	_ = os.Remove(path)
	m.writeSchemeState(nil)
	if _, err := os.Stat(httpSchemeFileName); !os.IsNotExist(err) {
		t.Fatalf("empty DataDir should not create %s in cwd: %v", httpSchemeFileName, err)
	}
}

// 开启 HTTPS 后同一个端口要同时接 TLS 与明文 HTTP：明文请求跳到 HTTPS，而不是
// 给用户看 "Client sent an HTTP request to an HTTPS server."。
func TestServerManagerRedirectsPlainHTTPToHTTPS(t *testing.T) {
	certPEM, keyPEM := makeTestPairPEM(t)
	cfg := &config.Config{}
	cfg.App.Port = 8080
	cfg.App.DataDir = t.TempDir()
	cfg.App.HTTPSEnabled = true
	cfg.App.SSLCert, cfg.App.SSLKey = certPEM, keyPEM

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "secure:"+r.URL.Path)
	})
	m := newServerManager(cfg, zap.NewNop(), handler)
	m.addr = "127.0.0.1:0" // 端口 0 交给内核分配，测试之间不会撞端口
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	addr := m.ln.Addr().String()
	// 端口 0 只在第一次分配时用；热切换要绑回同一个端口（生产里端口本来就是固定的）。
	m.addr = addr
	schemeFile := filepath.Join(cfg.App.DataDir, httpSchemeFileName)
	if data, err := os.ReadFile(schemeFile); err != nil || strings.TrimSpace(string(data)) != "https" {
		t.Fatalf("scheme state = %q err=%v, want https", data, err)
	}

	plain := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	resp, err := plain.Get("http://" + addr + "/library?tab=1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("plain GET status = %d, want %d", resp.StatusCode, http.StatusFound)
	}
	if got, want := resp.Header.Get("Location"), "https://"+addr+"/library?tab=1"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}

	postResp, err := plain.Post("http://"+addr+"/api/me/login", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = postResp.Body.Close() }()
	if postResp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("plain POST status = %d, want %d", postResp.StatusCode, http.StatusTemporaryRedirect)
	}
	if got, want := postResp.Header.Get("Location"), "https://"+addr+"/api/me/login"; got != want {
		t.Fatalf("POST Location = %q, want %q", got, want)
	}

	// 同一端口上的 TLS 连接照常拿到正常响应（校验证书，不做跳过校验）。
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(certPEM)) {
		t.Fatal("append test certificate to pool failed")
	}
	secure := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}
	secureResp, err := secure.Get("https://" + addr + "/library")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = secureResp.Body.Close() }()
	body, err := io.ReadAll(secureResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if secureResp.StatusCode != http.StatusOK || string(body) != "secure:/library" {
		t.Fatalf("https response = %d %q, want 200 %q", secureResp.StatusCode, body, "secure:/library")
	}

	// 关掉 HTTPS 后同一端口回到纯明文服务，不再有多余的跳转服务器。
	cfg.App.HTTPSEnabled = false
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if m.redirectSrv != nil {
		t.Fatal("redirect server should be released when HTTPS is off")
	}
	if data, err := os.ReadFile(schemeFile); err != nil || strings.TrimSpace(string(data)) != "http" {
		t.Fatalf("scheme state = %q err=%v, want http", data, err)
	}
	plainResp, err := plain.Get("http://" + addr + "/library")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plainResp.Body.Close() }()
	plainBody, err := io.ReadAll(plainResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if plainResp.StatusCode != http.StatusOK || string(plainBody) != "secure:/library" {
		t.Fatalf("plain response after disabling https = %d %q", plainResp.StatusCode, plainBody)
	}
}
