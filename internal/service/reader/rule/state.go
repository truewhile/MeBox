package rule

import (
	"net"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/net/publicsuffix"
)

// 本文件：书源会话状态（变量 / 登录信息 / 登录请求头 / Cookie）的抽象。
// 对应 legado BaseSource 的 getVariable/putVariable/getLoginInfo/putLoginInfo/
// getLoginHeader/putLoginHeader 与 CookieStore。实现由服务层落库提供；
// 未注入时退回进程内 MemoryState（单测、冒烟 CLI 用）。

// SourceState 书源会话状态读写接口。
type SourceState interface {
	// GetVariable / SetVariable 对应 source.getVariable / source.setVariable。
	GetVariable() string
	SetVariable(v string)

	// GetLoginInfo / SetLoginInfo 对应 source.getLoginInfo / source.putLoginInfo。
	// 内容为登录表单的 JSON（键为字段名）。
	GetLoginInfo() string
	SetLoginInfo(v string)

	// GetLoginHeader / SetLoginHeader 对应 source 的登录请求头（JSON），
	// 其中 Cookie 键会在请求时合并进 Cookie 头。
	GetLoginHeader() string
	SetLoginHeader(v string)

	// Cookie 读写：rawURL 可为完整 URL 或裸域名，按有效顶级域+1 归并。
	GetCookie(rawURL string) string
	GetCookieKey(rawURL, key string) string
	SetCookie(rawURL, cookie string)
	RemoveCookie(rawURL string)

	// Toast 收集宿主提示（java.toast / java.longToast），登录反馈靠它回传前端。
	Toast(msg string)
	// OpenBrowser 记录需要浏览器完成的地址（java.startBrowser）——
	// 服务端无法弹窗，前端据此提供「在新标签打开」。
	OpenBrowser(url, title string)
}

// CookieDomain 取 URL 的有效顶级域 +1（对应 legado NetworkUtils.getSubDomain）。
// 裸域名（无 scheme）按 http 处理；IP 原样返回；解析失败回退 host 本身。
func CookieDomain(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		if strings.HasPrefix(s, "//") {
			s = "http:" + s
		} else {
			s = "http://" + s
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" {
		return ""
	}
	if net.ParseIP(host) != nil {
		return host
	}
	etld1, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return host
	}
	return etld1
}

// ParseCookie 把 "a=1; b=2" 拆为键值对（值不 Trim 内部空白，仅去首尾）。
func ParseCookie(cookie string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(cookie, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 || kv[0] == "" {
			continue
		}
		out[kv[0]] = kv[1]
	}
	return out
}

// MergeCookie 将新 cookie 覆盖式合并进旧 cookie（对应 CookieStore.replaceCookie）。
func MergeCookie(old, newCookie string) string {
	merged := ParseCookie(old)
	for k, v := range ParseCookie(newCookie) {
		merged[k] = v
	}
	return formatCookie(merged)
}

func formatCookie(kv map[string]string) string {
	parts := make([]string, 0, len(kv))
	for k, v := range kv {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

// MemoryState 进程内会话状态（未注入持久化实现时的回退）。
type MemoryState struct {
	mu          sync.Mutex
	variable    string
	loginInfo   string
	loginHeader string
	cookies     map[string]map[string]string // domain → name → value
	toasts      []string
	browsers    []BrowserRequest
}

// BrowserRequest 前端可代为打开的浏览器地址（java.startBrowser 收集）。
type BrowserRequest struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// NewMemoryState 创建进程内状态。
func NewMemoryState() *MemoryState {
	return &MemoryState{cookies: map[string]map[string]string{}}
}

func (m *MemoryState) GetVariable() string { m.mu.Lock(); defer m.mu.Unlock(); return m.variable }
func (m *MemoryState) SetVariable(v string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.variable = v
}

func (m *MemoryState) GetLoginInfo() string { m.mu.Lock(); defer m.mu.Unlock(); return m.loginInfo }
func (m *MemoryState) SetLoginInfo(v string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loginInfo = v
}

func (m *MemoryState) GetLoginHeader() string { m.mu.Lock(); defer m.mu.Unlock(); return m.loginHeader }
func (m *MemoryState) SetLoginHeader(v string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loginHeader = v
}

func (m *MemoryState) GetCookie(rawURL string) string {
	return m.GetCookieKey(rawURL, "")
}

func (m *MemoryState) GetCookieKey(rawURL, key string) string {
	domain := CookieDomain(rawURL)
	if domain == "" {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	jar, ok := m.cookies[domain]
	if !ok {
		return ""
	}
	if key != "" {
		return jar[key]
	}
	return formatCookie(jar)
}

func (m *MemoryState) SetCookie(rawURL, cookie string) {
	domain := CookieDomain(rawURL)
	if domain == "" || strings.TrimSpace(cookie) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	jar, ok := m.cookies[domain]
	if !ok {
		jar = map[string]string{}
		m.cookies[domain] = jar
	}
	for k, v := range ParseCookie(cookie) {
		jar[k] = v
	}
}

func (m *MemoryState) RemoveCookie(rawURL string) {
	domain := CookieDomain(rawURL)
	if domain == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cookies, domain)
}

func (m *MemoryState) Toast(msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toasts = append(m.toasts, msg)
}

func (m *MemoryState) OpenBrowser(url, title string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.browsers = append(m.browsers, BrowserRequest{URL: url, Title: title})
}

// Toasts 返回并清空已收集的宿主提示。
func (m *MemoryState) Toasts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.toasts
	m.toasts = nil
	return out
}

// Browsers 返回并清空已收集的浏览器地址。
func (m *MemoryState) Browsers() []BrowserRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.browsers
	m.browsers = nil
	return out
}

// Snapshot 返回当前 Cookie（domain → cookie 串）副本。
func (m *MemoryState) Snapshot() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.cookies))
	for d, jar := range m.cookies {
		out[d] = formatCookie(jar)
	}
	return out
}

var _ SourceState = (*MemoryState)(nil)
