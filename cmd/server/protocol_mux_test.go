package main

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestSplitListenerRoutesByFirstByte(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	split := newSplitListener(raw)
	addr := raw.Addr().String()

	// TLS：首字节 0x16。分流器偷看过的字节必须还能被上层读到，否则握手会失败。
	handshake := []byte{0x16, 0x03, 0x01, 0x00, 0x2c}
	tlsClient, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tlsClient.Close() }()
	if _, err := tlsClient.Write(handshake); err != nil {
		t.Fatal(err)
	}
	tlsConn := <-split.tlsCh
	defer func() { _ = tlsConn.Close() }()
	got := make([]byte, len(handshake))
	if _, err := io.ReadFull(tlsConn, got); err != nil {
		t.Fatalf("read peeked tls bytes: %v", err)
	}
	if string(got) != string(handshake) {
		t.Fatalf("tls bytes = %v, want %v", got, handshake)
	}

	// 明文 HTTP：请求行走另一路，同样要读到偷看过的首字节。
	request := "GET /api/health HTTP/1.1\r\nHost: example.com\r\n\r\n"
	plainClient, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plainClient.Close() }()
	if _, err := plainClient.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	plainConn := <-split.plainCh
	defer func() { _ = plainConn.Close() }()
	head := make([]byte, len("GET /"))
	if _, err := io.ReadFull(plainConn, head); err != nil {
		t.Fatalf("read peeked plain bytes: %v", err)
	}
	if string(head) != request[:len(head)] {
		t.Fatalf("plain bytes = %q, want prefix of %q", head, request)
	}

	// 底层监听关闭后两路 Accept 都要返回 net.ErrClosed，http.Server 才能收尾。
	_ = raw.Close()
	for _, ln := range []net.Listener{split.tlsListener(), split.plainListener()} {
		deadline := time.Now().Add(3 * time.Second)
		for {
			_, err := ln.Accept()
			if err == net.ErrClosed {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("Accept after close = %v, want net.ErrClosed", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// 只连不发的半开连接不应被分给任何一路，而应在偷看超时后被丢弃。
func TestSplitListenerDropsIdleConnection(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	split := newSplitListenerWithTimeout(raw, 200*time.Millisecond)

	conn, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	select {
	case c := <-split.plainCh:
		_ = c.Close()
		t.Fatal("idle connection should not be dispatched")
	case c := <-split.tlsCh:
		_ = c.Close()
		t.Fatal("idle connection should not be dispatched")
	case <-time.After(time.Second):
	}
}

func TestHTTPSRedirectTarget(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		rawURL  string
		want    string
		wantErr bool
	}{
		{name: "keeps host port path and query", host: "10.0.0.2:8080", rawURL: "/library?tab=1&q=a%20b", want: "https://10.0.0.2:8080/library?tab=1&q=a%20b"},
		{name: "root path", host: "nas.local:18080", rawURL: "/", want: "https://nas.local:18080/"},
		{name: "keeps escaping of user input", host: "10.0.0.2", rawURL: "/a%2Fb", want: "https://10.0.0.2/a%2Fb"},
		{name: "absolute form request line", host: "10.0.0.2:8080", rawURL: "http://10.0.0.2:8080/api/x?y=1", want: "https://10.0.0.2:8080/api/x?y=1"},
		{name: "rejects empty host", host: "", rawURL: "/", wantErr: true},
		{name: "rejects host with slash", host: "evil.com/path", rawURL: "/", wantErr: true},
		{name: "rejects userinfo host", host: "user@evil.com", rawURL: "/", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.rawURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = tc.host

			if host := redirectHostHeader(req); host == "" {
				if !tc.wantErr {
					t.Fatalf("redirectHostHeader(%q) rejected, want accepted", tc.host)
				}
				return
			}
			if tc.wantErr {
				t.Fatalf("redirectHostHeader(%q) accepted, want rejected", tc.host)
			}
			if got := httpsRedirectURL(tc.host, req); got != tc.want {
				t.Fatalf("redirect target = %q, want %q", got, tc.want)
			}
		})
	}
}
