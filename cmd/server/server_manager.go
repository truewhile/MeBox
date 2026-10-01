package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/service"
)

// tlsPair 记录当前正在服务的证书，用于判断是否需要重新绑定监听。
type tlsPair struct {
	cert    tls.Certificate
	certPEM string
	keyPEM  string
	// version 是解析后的证书/私钥指纹；内容或磁盘文件变化都会导致其改变，
	// 据此决定是否需要重新绑定监听。
	version string
}

// serverManager 负责 MeBox 的 HTTP/HTTPS 监听。HTTPS 设置保存后调用 Reload，
// 在同一个端口上把明文 HTTP 与 TLS 监听热切换，无需重启进程：
//
//   - 关闭旧监听释放端口（同一进程内 Windows 不允许重复绑定同一端口）；
//   - 按最新配置重新绑定并立即对外服务；
//   - 旧服务器随后优雅退出，正在进行的播放/请求不会被立刻掐断。
//
// 任何校验失败都会中止切换并保留旧监听，保证用户不会被锁在服务外面。
type serverManager struct {
	cfg     *config.Config
	log     *zap.Logger
	handler http.Handler
	addr    string

	mu                sync.Mutex
	srv               *http.Server
	ln                net.Listener
	pair              *tlsPair
	stopCh            chan struct{}
	autoReloadStarted bool
}

func newServerManager(cfg *config.Config, log *zap.Logger, handler http.Handler) *serverManager {
	return &serverManager{
		cfg:     cfg,
		log:     log,
		handler: handler,
		addr:    fmt.Sprintf(":%d", cfg.App.Port),
		stopCh:  make(chan struct{}),
	}
}

// Start 启动监听。即使 HTTPS 配置损坏也退回明文 HTTP 继续启动，避免服务冷启动失败。
func (m *serverManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pair, err := m.desiredPair()
	if err != nil {
		m.log.Error("invalid HTTPS config at startup, serving plain HTTP instead", zap.Error(err))
		pair = nil
	}
	if err := m.bind(pair); err != nil {
		return err
	}
	m.logServerReady()
	m.maybeStartAutoReloadLocked()
	return nil
}

// Reload 依据最新配置热切换监听。返回的错误会带给调用它的设置接口；若新监听
// 绑定失败会自动回滚到旧配置继续服务。
func (m *serverManager) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pair, err := m.desiredPair()
	if err != nil {
		m.log.Error("server reload aborted", zap.Error(err))
		return err
	}
	if m.pairEquals(pair) {
		return nil
	}

	oldSrv, oldLn, oldPair := m.srv, m.ln, m.pair
	if oldLn != nil {
		_ = oldLn.Close() // 释放端口后再绑定新监听
	}
	m.srv, m.ln, m.pair = nil, nil, nil

	firstErr := m.bind(pair)
	if firstErr != nil {
		m.log.Error("bind new listener failed, rolling back to previous", zap.Error(firstErr))
		if rbErr := m.bind(oldPair); rbErr != nil {
			return fmt.Errorf("reload failed: %v; rollback failed: %v", firstErr, rbErr)
		}
	}
	// 新监听已就绪，让旧服务器在新连接切换到新监听后优雅退出。
	m.drain(oldSrv)
	m.logServerReady()
	m.maybeStartAutoReloadLocked()
	return firstErr
}

// Shutdown 优雅停止当前服务器（用于进程退出）。
func (m *serverManager) Shutdown(ctx context.Context) error {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.srv == nil {
		return nil
	}
	return m.srv.Shutdown(ctx)
}

// desiredPair 根据当前配置计算目标监听形态：nil 表示明文 HTTP，非 nil 表示 TLS。
// 证书/私钥按"路径优先、内容兜底"解析，并校验是否匹配。
func (m *serverManager) desiredPair() (*tlsPair, error) {
	// 与 ApplyRuntimeSetting 的写锁配对：HTTPS 相关字段可能被运行时设置
	// 热更新，无锁读存在数据竞争（string 撕裂）。
	config.RuntimeMu.RLock()
	httpsEnabled := m.cfg != nil && m.cfg.App.HTTPSEnabled
	cert := m.cfg.App.SSLCert
	certPath := m.cfg.App.SSLCertPath
	key := m.cfg.App.SSLKey
	keyPath := m.cfg.App.SSLKeyPath
	config.RuntimeMu.RUnlock()
	if !httpsEnabled {
		return nil, nil
	}
	certPEM, err := service.ResolveSSLMaterial(cert, certPath, "证书")
	if err != nil {
		return nil, err
	}
	keyPEM, err := service.ResolveSSLMaterial(key, keyPath, "私钥")
	if err != nil {
		return nil, err
	}
	if err := service.ValidateSSLKeyPair(certPEM, keyPEM); err != nil {
		return nil, err
	}
	pairCert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("SSL 证书/私钥无效：%v", err)
	}
	return &tlsPair{
		cert:    pairCert,
		certPEM: certPEM,
		keyPEM:  keyPEM,
		version: certPEM + "\x00" + keyPEM,
	}, nil
}

// maybeStartAutoReloadLocked 在证书/私钥通过文件路径配置时，幂等地启动后台轮询，
// 便于运行中切换到路径方式（或换证）后无需重启也能热更新。调用方需持有 m.mu。
func (m *serverManager) maybeStartAutoReloadLocked() {
	if m.autoReloadStarted {
		return
	}
	if !m.pathBased() {
		return
	}
	m.autoReloadStarted = true
	m.startAutoReload()
}

// pathBased 是否至少有一侧证书/私钥通过文件路径配置。
func (m *serverManager) pathBased() bool {
	config.RuntimeMu.RLock()
	defer config.RuntimeMu.RUnlock()
	return strings.TrimSpace(m.cfg.App.SSLCertPath) != "" || strings.TrimSpace(m.cfg.App.SSLKeyPath) != ""
}

// startAutoReload 后台轮询文件变更并自动热更新，方便换证。
func (m *serverManager) startAutoReload() {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-m.stopCh:
				return
			case <-ticker.C:
				if !m.pathBased() {
					continue // 路径已清空（改回内容配置），不再轮询
				}
				if err := m.Reload(); err != nil {
					m.log.Warn("periodic https reload failed", zap.Error(err))
				}
			}
		}
	}()
}

// pairEquals 判断目标配置与当前监听是否一致，一致则无需重新绑定。
func (m *serverManager) pairEquals(pair *tlsPair) bool {
	if pair == nil && m.pair == nil {
		return true
	}
	if pair == nil || m.pair == nil {
		return false
	}
	return pair.version == m.pair.version
}

// bind 创建并按需启用 TLS 的监听，异步开始服务。
func (m *serverManager) bind(pair *tlsPair) error {
	ln, err := net.Listen("tcp", m.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", m.addr, err)
	}
	srv := &http.Server{
		Handler:           m.handler,
		ReadHeaderTimeout: 15 * time.Second,
	}
	if pair != nil {
		ln = tls.NewListener(ln, &tls.Config{
			Certificates: []tls.Certificate{pair.cert},
			MinVersion:   tls.VersionTLS12,
		})
	}
	m.srv, m.ln, m.pair = srv, ln, pair
	go m.serve(srv, ln)
	return nil
}

func (m *serverManager) serve(s *http.Server, ln net.Listener) {
	if err := s.Serve(ln); err != nil &&
		!errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		m.log.Fatal("listen failed", zap.Error(err))
	}
}

// drain 让旧服务器在后台优雅退出（等待进行中的连接完成或在超时后强制关闭）。
func (m *serverManager) drain(s *http.Server) {
	if s == nil {
		return
	}
	go func(s *http.Server) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			m.log.Warn("drain old server failed", zap.Error(err))
		}
	}(s)
}

func (m *serverManager) logServerReady() {
	scheme := "http"
	if m.pair != nil {
		scheme = "https"
	}
	localIP := getLocalIP()
	m.log.Info("server is ready",
		zap.String("scheme", scheme),
		zap.String("local", fmt.Sprintf("%s://%s:%d", scheme, localIP, m.cfg.App.Port)),
		zap.String("listen", m.addr),
	)
	if m.pair != nil {
		m.log.Info("HTTPS is enabled; plain HTTP is no longer served on this port",
			zap.String("addr", m.addr),
		)
	}
}
