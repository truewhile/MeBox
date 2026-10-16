package service

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

// hostAndPortOf 从测试服务器的 URL 里取出主机名与端口。
func hostAndPortOf(t *testing.T, raw string) (string, string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	return u.Hostname(), u.Port()
}

// 现场（2026-10-16，生产容器内实测）：放行名单里明明有 mebox-metatube-1，
// 回源仍然失败于
//
//	image proxy: requests to private/internal hosts are not allowed
//
// 原因：连接层校验（dialer 的 Control 回调）拿到的 address 是**解析后的 IP**，
// 而名单里存的是主机名（ConfiguredRemoteHosts 与
// configuredServiceImageHostsFromRepo 都返回 u.Hostname()），两者永远不相等：
// 那段 isAllowedRemoteHost 分支对主机名条目是死代码。
//
// 远程 Emby 挂载用 IP 配置时看不出问题，用主机名（NAS 名、docker 服务名）配置
// 时同样中招。这个用例锁住「按主机名放行」这条路径。
func TestImageProxyDialsAllowlistedHostname(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(testJPEG)
	}))
	defer upstream.Close()

	proxy := NewImageProxy(
		&config.Config{Cache: config.CacheConfig{CacheDir: filepath.Join(t.TempDir(), "cache")}},
		zap.NewNop(),
	)
	// 名单里放「主机名」而不是 IP —— 这正是生产里 mebox-metatube-1 的形态。
	proxy.SetAllowedRemoteHostsProvider(func() []string { return []string{"localhost"} })

	// 直接用该代理自己的 transport 拨号，才能命中连接层校验。
	_, port := hostAndPortOf(t, upstream.URL)
	resp, err := proxy.client.Get("http://localhost:" + port + "/poster.jpg")
	if err != nil {
		t.Fatalf("allowlisted hostname must be dialable, got: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// 反向约束：未放行的主机名解析到私网地址时仍必须被拒。
// 这是连接层校验存在的理由（DNS rebinding），不能为了修上面那条而拆掉。
func TestImageProxyStillBlocksUnlistedHostnameResolvingToPrivateIP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(testJPEG)
	}))
	defer upstream.Close()

	proxy := NewImageProxy(
		&config.Config{Cache: config.CacheConfig{CacheDir: filepath.Join(t.TempDir(), "cache")}},
		zap.NewNop(),
	)
	// 名单为空：localhost 解析到回环地址，必须被拦。
	proxy.SetAllowedRemoteHostsProvider(func() []string { return nil })

	_, port := hostAndPortOf(t, upstream.URL)
	resp, err := proxy.client.Get("http://localhost:" + port + "/poster.jpg")
	if err == nil {
		resp.Body.Close()
		t.Fatal("a non-allowlisted hostname resolving to a private IP must be refused")
	}
}
