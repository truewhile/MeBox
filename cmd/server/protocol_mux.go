package main

import (
	"bufio"
	"net"
	"sync"
	"time"
)

// 开启 HTTPS 后，同一个端口会同时面对两类连接：
//
//   - TLS 客户端（浏览器、播放器）——正常提供 HTTPS 服务；
//   - 明确访问 http:// 的客户端——以前只会撞出一句
//     "Client sent an HTTP request to an HTTPS server."，用户看不懂。
//
// 一个端口上只有 TLS 监听时无法区分两者，所以在 Accept 之后先偷看第一个字节：
// TLS 握手的记录类型固定是 0x16（handshake），而 HTTP 请求行必然是可见字符。
// 据此把连接分流成两路，明文那一路交给只做跳转的 http.Server，于是 http:// 会被
// 自动重定向到 https://（见 server_manager.go 的 httpsRedirectHandler）。
const tlsRecordHandshake = 0x16

// defaultPeekTimeout 限制"偷看首字节"阶段的等待，避免只连不发的半开连接长期占住
// goroutine。分流器按实例持有该值，便于测试用更短的超时。
const defaultPeekTimeout = 10 * time.Second

// splitListener 是监听分流器：Accept 到底层连接后按首字节投递给对应的一路。
type splitListener struct {
	ln      net.Listener
	tlsCh   chan net.Conn
	plainCh chan net.Conn
	quit    chan struct{}
	once    sync.Once
	peek    time.Duration
}

func newSplitListener(ln net.Listener) *splitListener {
	return newSplitListenerWithTimeout(ln, defaultPeekTimeout)
}

func newSplitListenerWithTimeout(ln net.Listener, peek time.Duration) *splitListener {
	s := &splitListener{
		ln:      ln,
		tlsCh:   make(chan net.Conn),
		plainCh: make(chan net.Conn),
		quit:    make(chan struct{}),
		peek:    peek,
	}
	go s.acceptLoop()
	return s
}

// tlsListener 返回只收到 TLS 连接的 listener。
func (s *splitListener) tlsListener() net.Listener {
	return &channelListener{addr: s.ln.Addr(), conns: s.tlsCh, quit: s.quit}
}

// plainListener 返回只收到明文 HTTP 连接的 listener。
func (s *splitListener) plainListener() net.Listener {
	return &channelListener{addr: s.ln.Addr(), conns: s.plainCh, quit: s.quit}
}

func (s *splitListener) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			// 底层监听关闭（换证、关停）后两路 Accept 一起返回 net.ErrClosed，
			// 由持有它们的 http.Server 自行收尾。
			s.stop()
			return
		}
		go s.dispatch(conn)
	}
}

func (s *splitListener) dispatch(conn net.Conn) {
	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(s.peek))
	first, err := br.Peek(1)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return
	}

	peeked := &peekedConn{Conn: conn, r: br}
	ch := s.plainCh
	if first[0] == tlsRecordHandshake {
		ch = s.tlsCh
	}
	select {
	case ch <- peeked: // 交给对应的 http.Server，由它负责读写与关闭
	case <-s.quit:
		_ = conn.Close()
	}
}

func (s *splitListener) stop() {
	s.once.Do(func() { close(s.quit) })
}

// channelListener 把一路 channel 包装成 net.Listener，供 http.Server.Serve 使用。
type channelListener struct {
	addr  net.Addr
	conns <-chan net.Conn
	quit  <-chan struct{}
}

func (l *channelListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.quit:
		return nil, net.ErrClosed
	}
}

// Close 是空实现：真正要释放的端口在 splitListener 的底层监听上，它一旦关闭，
// 这里的 Accept 就会返回 net.ErrClosed，http.Server 随之退出。
func (l *channelListener) Close() error { return nil }

func (l *channelListener) Addr() net.Addr { return l.addr }

// peekedConn 把已经读进 bufio 缓冲的首字节还给上层读取者。
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
