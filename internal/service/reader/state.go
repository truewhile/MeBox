package reader

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

// 本文件：把书源会话状态（变量 / 登录信息 / 登录请求头 / Cookie）落到 DB。
// 对应 legado 的 CacheManager + CookieStore，按书源 URL 隔离，
// 使登录态在服务端重启后依然有效。

// sourceState 实现 rule.SourceState，按书源 URL 读写 ReaderSourceState。
// 写入是"读-改-写"：一次性把四个字段整体落库，保证并发下不丢字段。
type sourceState struct {
	svc       *ReaderService
	ctx       context.Context
	sourceURL string
	// 进程内累积区：一次规则执行内可能多次读写，先落内存再统一 flush。
	variable    string
	loginInfo   string
	loginHeader string
	// cookies / cookieMu：domain → "k=v; k=v"。
	// cookieMu 保护这张表：浏览器面板里的多个资源/接口代理请求会并发回写
	// Cookie（见 browser_panel.go），而书源 JS 同时在读，不加锁会 data race。
	cookies  map[string]string
	cookieMu sync.Mutex
	loaded   bool

	toasts   []string
	browsers []rule.BrowserRequest
	// uiRefresh 书源通过 java.reLoginView / refreshExplore / upLoginData
	// 请求重新渲染登录表单（对应 legado 直接操作对话框控件）。
	uiRefresh bool
	dirty     bool
}

// newSourceState 载入指定书源的会话状态（含解密）。
func (s *ReaderService) newSourceState(ctx context.Context, sourceURL string) *sourceState {
	st := &sourceState{svc: s, ctx: ctx, sourceURL: sourceURL, cookies: map[string]string{}}
	if s.repo == nil || sourceURL == "" {
		st.loaded = true
		return st
	}
	rec, err := s.repo.GetSourceState(ctx, sourceURL)
	if err != nil || rec == nil {
		st.loaded = true
		return st
	}
	st.variable = s.decrypt(rec.Variable)
	st.loginInfo = s.decrypt(rec.LoginInfo)
	st.loginHeader = s.decrypt(rec.LoginHeader)
	if ck := s.decrypt(rec.Cookies); ck != "" {
		_ = json.Unmarshal([]byte(ck), &st.cookies)
	}
	st.loaded = true
	return st
}

func (st *sourceState) GetVariable() string { return st.variable }

func (st *sourceState) SetVariable(v string) {
	if st.variable == v {
		return
	}
	st.variable = v
	st.dirty = true
}

// GetVariableKey / SetVariableKey 以键为单位读写源变量 map，
// 对应 legado source.variableMap（规则里的 @get:{} / @put:{} 与 java.get/put 走这里）。
func (st *sourceState) GetVariableKey(key string) string {
	if key == "" {
		return ""
	}
	var m map[string]string
	if json.Unmarshal([]byte(st.variable), &m) != nil {
		return ""
	}
	return m[key]
}

func (st *sourceState) SetVariableKey(key, value string) {
	if key == "" {
		return
	}
	m := map[string]string{}
	_ = json.Unmarshal([]byte(st.variable), &m)
	if m == nil {
		m = map[string]string{}
	}
	m[key] = value
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	st.SetVariable(string(b))
}

func (st *sourceState) GetLoginInfo() string { return st.loginInfo }

func (st *sourceState) SetLoginInfo(v string) {
	if st.loginInfo == v {
		return
	}
	st.loginInfo = v
	st.dirty = true
}

func (st *sourceState) GetLoginHeader() string { return st.loginHeader }

func (st *sourceState) SetLoginHeader(v string) {
	if st.loginHeader == v {
		return
	}
	st.loginHeader = v
	st.dirty = true
}

func (st *sourceState) GetCookie(rawURL string) string { return st.GetCookieKey(rawURL, "") }

func (st *sourceState) GetCookieKey(rawURL, key string) string {
	st.cookieMu.Lock()
	defer st.cookieMu.Unlock()
	return st.getCookieKeyLocked(rawURL, key)
}

// getCookieKeyLocked 的调用方需持有 cookieMu。
func (st *sourceState) getCookieKeyLocked(rawURL, key string) string {
	domain := rule.CookieDomain(rawURL)
	if domain == "" {
		return ""
	}
	raw, ok := st.cookies[domain]
	if !ok {
		return ""
	}
	if key == "" {
		return raw
	}
	return rule.ParseCookie(raw)[key]
}

func (st *sourceState) SetCookie(rawURL, cookie string) {
	domain := rule.CookieDomain(rawURL)
	if domain == "" || strings.TrimSpace(cookie) == "" {
		return
	}
	st.cookieMu.Lock()
	defer st.cookieMu.Unlock()
	merged := rule.MergeCookie(st.cookies[domain], cookie)
	if merged == st.cookies[domain] {
		return
	}
	st.cookies[domain] = merged
	st.dirty = true
}

func (st *sourceState) RemoveCookie(rawURL string) {
	domain := rule.CookieDomain(rawURL)
	if domain == "" {
		return
	}
	st.cookieMu.Lock()
	defer st.cookieMu.Unlock()
	if _, ok := st.cookies[domain]; !ok {
		return
	}
	delete(st.cookies, domain)
	st.dirty = true
}

func (st *sourceState) Toast(msg string) { st.toasts = append(st.toasts, msg) }

// RequestUIRefresh 实现 rule.UIState：书源要求重画登录表单。
func (st *sourceState) RequestUIRefresh() { st.uiRefresh = true }

// ApplyLoginData 实现 rule.UIState：把书源给出的值合并进已保存的登录信息。
func (st *sourceState) ApplyLoginData(data map[string]string) {
	if len(data) == 0 {
		return
	}
	cur := map[string]string{}
	if st.loginInfo != "" {
		_ = json.Unmarshal([]byte(st.loginInfo), &cur)
	}
	for k, v := range data {
		cur[k] = v
	}
	if b, err := json.Marshal(cur); err == nil {
		st.SetLoginInfo(string(b))
	}
	st.uiRefresh = true
}

// UIRefreshRequested 返回并清空「重画登录表单」标记。
func (st *sourceState) UIRefreshRequested() bool {
	out := st.uiRefresh
	st.uiRefresh = false
	return out
}

func (st *sourceState) OpenBrowser(url, title string) {
	st.browsers = append(st.browsers, rule.BrowserRequest{URL: url, Title: title})
}

// flush 把累积状态落库（登录信息与 Cookie 加密存储）。
func (st *sourceState) flush() {
	if !st.dirty || st.svc == nil || st.svc.repo == nil || st.sourceURL == "" {
		return
	}
	st.cookieMu.Lock()
	cookiesJSON := ""
	if len(st.cookies) > 0 {
		if b, err := json.Marshal(st.cookies); err == nil {
			cookiesJSON = string(b)
		}
	}
	st.cookieMu.Unlock()
	rec := &model.ReaderSourceState{
		SourceURL:   st.sourceURL,
		Variable:    st.variable,
		LoginInfo:   st.svc.encrypt(st.loginInfo),
		LoginHeader: st.svc.encrypt(st.loginHeader),
		Cookies:     st.svc.encrypt(cookiesJSON),
	}
	if err := st.svc.repo.SaveSourceState(st.ctx, rec); err != nil {
		if st.svc.log != nil {
			st.svc.log.Warn("reader: 保存书源会话状态失败: " + err.Error())
		}
		return
	}
	st.dirty = false
}

// snapshotCookies 返回 Cookie 副本（domain → cookie 串）。
func (st *sourceState) snapshotCookies() map[string]string {
	st.cookieMu.Lock()
	defer st.cookieMu.Unlock()
	out := make(map[string]string, len(st.cookies))
	for d, c := range st.cookies {
		out[d] = c
	}
	return out
}

// clearCookies 清空全部 Cookie 并标记待落库（对应 legado removeAllCookies）。
func (st *sourceState) clearCookies() {
	st.cookieMu.Lock()
	defer st.cookieMu.Unlock()
	if len(st.cookies) == 0 {
		return
	}
	st.cookies = map[string]string{}
	st.dirty = true
}

// CookieForRequest 返回应附加到该请求的 Cookie 串：
// 会话 Cookie 优先，其次是 loginHeader 中显式声明的 Cookie。
func (st *sourceState) CookieForRequest(rawURL string) string {
	if st == nil {
		return ""
	}
	cookie := st.GetCookie(rawURL)
	if h := strings.TrimSpace(st.loginHeader); h != "" {
		var m map[string]any
		if json.Unmarshal([]byte(h), &m) == nil {
			for k, v := range m {
				if strings.EqualFold(k, "cookie") {
					extra := strings.TrimSpace(toStringVal(v))
					if extra != "" {
						cookie = rule.MergeCookie(cookie, extra)
					}
				}
			}
		}
	}
	return cookie
}

// LoginHeaderMap 返回 loginHeader 的解析结果（除 Cookie 外的头）。
func (st *sourceState) LoginHeaderMap() map[string]string {
	out := map[string]string{}
	if st == nil || strings.TrimSpace(st.loginHeader) == "" {
		return out
	}
	var m map[string]any
	if json.Unmarshal([]byte(st.loginHeader), &m) != nil {
		return out
	}
	for k, v := range m {
		out[k] = toStringVal(v)
	}
	return out
}

func toStringVal(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// encrypt / decrypt 复用全局 CryptoService（密钥来自 JWTSecret）。
func (s *ReaderService) encrypt(plain string) string {
	if s.crypto == nil {
		return plain
	}
	return s.crypto.Encrypt(plain)
}

func (s *ReaderService) decrypt(value string) string {
	if s.crypto == nil {
		return value
	}
	return s.crypto.Decrypt(value)
}

var _ rule.SourceState = (*sourceState)(nil)
