package reader

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// 本文件：把响应下发的 Set-Cookie 收进书源会话。
//
// 之前只有 executeWithState 收 Set-Cookie，而且只看**最终**响应、按请求地址归档。
// 对「靠跳转链下发凭证」的登录源（典型是扫码登录：票据地址一跳 302 带
// Set-Cookie，再跳到业务域名）这三个做法都会漏：
//
//  1. 凭证在中间某一跳的 Set-Cookie 里，只看最终响应拿不到；
//  2. 签发站点（如 passport.biligame.com）声明的 Domain 可能是业务域
//     （Domain=.bilibili.com），按请求地址归档会存进错误的域桶，
//     之后 api.bilibili.com 的请求取不到这份 Cookie；
//  3. 浏览器面板里的页面自己发起的请求走 ProxyBrowserXHR，完全不落库，
//     页面在面板里「登录成功」但书源会话始终是空的。
//
// 因此统一成：一个 cookieSink 收集一次请求（含各跳）的 Set-Cookie，
// 再按「Cookie 自身的 Domain 优先、否则该跳请求地址」写入会话。

// capturedCookie 一条待写入会话的 Cookie 及其归属地址。
type capturedCookie struct {
	// url 归属地址：Cookie 声明了 Domain 时用 Domain（去掉前导点），
	// 否则用下发它的那一跳的请求地址。
	url    string
	cookie string
}

// cookieSink 收集一次请求（含重定向各跳）下发的 Set-Cookie。
// 并发安全：面板里的多个资源/接口请求会同时走这里。
type cookieSink struct {
	mu    sync.Mutex
	items []capturedCookie
}

// add 收集一份响应里的 Set-Cookie。reqURL 是产生该响应（或跳转前那一跳）的地址。
func (s *cookieSink) add(reqURL *url.URL, resp *http.Response) {
	if s == nil || resp == nil {
		return
	}
	items := make([]capturedCookie, 0, len(resp.Cookies()))
	for _, ck := range resp.Cookies() {
		if ck == nil || ck.Name == "" {
			continue
		}
		target := ""
		if d := strings.TrimSpace(ck.Domain); d != "" {
			target = strings.TrimPrefix(d, ".")
		} else if reqURL != nil {
			target = reqURL.String()
		}
		if target == "" {
			continue
		}
		items = append(items, capturedCookie{url: target, cookie: ck.Name + "=" + ck.Value})
	}
	if len(items) == 0 {
		return
	}
	s.mu.Lock()
	s.items = append(s.items, items...)
	s.mu.Unlock()
}

// drain 取出并清空已收集的 Cookie。
func (s *cookieSink) drain() []capturedCookie {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.items
	s.items = nil
	return out
}

// apply 把收集到的 Cookie 写入会话并清空（会话为 nil 时直接丢弃）。
func (s *cookieSink) apply(state *sourceState) {
	if s == nil {
		return
	}
	items := s.drain()
	if state == nil || len(items) == 0 {
		return
	}
	for _, it := range items {
		state.SetCookie(it.url, it.cookie)
	}
}

// ─── 请求级挂载：sink 放进 context，由 CheckRedirect 逐跳收集 ──────────────

type cookieSinkKey struct{}

// withCookieSink 在请求 context 上挂一个 sink；sink 为 nil 时原样返回。
func withCookieSink(ctx context.Context, sink *cookieSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, cookieSinkKey{}, sink)
}

// cookieSinkFrom 取出请求 context 上的 sink（没有则 nil）。
func cookieSinkFrom(ctx context.Context) *cookieSink {
	if ctx == nil {
		return nil
	}
	sink, _ := ctx.Value(cookieSinkKey{}).(*cookieSink)
	return sink
}

// captureRedirectCookies 作为 http.Client.CheckRedirect：
// 每发生一次跳转，就把「上一跳响应」里的 Set-Cookie 收进 context 上的 sink。
//
// 没有挂 sink 时是纯透传；跳转次数上限与 net/http 的默认实现保持一致
// （10 次），所以装上它不改变既有链路的行为。
func captureRedirectCookies(req *http.Request, via []*http.Request) error {
	if req != nil {
		if sink := cookieSinkFrom(req.Context()); sink != nil && req.Response != nil {
			// via 的最后一跳才是产生 req.Response 的那个请求。
			prev := req
			if n := len(via); n > 0 {
				prev = via[n-1]
			}
			sink.add(prev.URL, req.Response)
		}
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// enableCookieCapture 给 reader 的 HTTP 客户端装上「逐跳收集 Set-Cookie」。
// NewReaderService 调用一次即可：没有挂 sink 的请求完全不受影响。
func enableCookieCapture(c *http.Client) {
	if c == nil {
		return
	}
	c.CheckRedirect = captureRedirectCookies
}
