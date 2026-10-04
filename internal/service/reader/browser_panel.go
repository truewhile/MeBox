package reader

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

// 本文件：书源 JS 的宿主浏览器（java.startBrowser / java.startBrowserAwait）。
//
// legado 用内置 WebView 承载页面：书源在自己的 JS 里拼一段 HTML（线路设置页
// 之类）或给一个网址，让用户点选、登录、过防爬校验，然后（Await 版本）把用户
// 操作后的页面源码当成 StrResponse 返回，书源再从中解析出结果写回源变量。
//
// 服务端没有 WebView，于是把这一段拆成三步：
//  1. JS 侧 startBrowserAwait 登记一个待办并阻塞等待；
//  2. 前端轮询到待办，在 <iframe> 里承载页面（同源，因此可以直接读回 DOM）；
//  3. 用户点 √ 后把 iframe 的 DOM 回传，阻塞解除，JS 拿到 body()。
//
// 页面来源分两种，承载方式不同：
//   - data:text/html;base64,…：书源自带的内嵌页面，直接解码返回；
//   - http(s)：服务端带书源 Cookie/请求头抓取后返回。这一步很关键——书源的
//     登录态存在服务端，浏览器里没有这些 Cookie，若直接让浏览器打开源站，
//     「用户后台」这类页面必然是未登录状态。页面里的资源地址会改写到同源
//     代理，使后续请求同样带上 Cookie。

const (
	// browserAwaitTimeout 单次等待用户操作的上限。
	browserAwaitTimeout = 10 * time.Minute
	// browserPageTTL 待办条目与页面链接的有效期。
	browserPageTTL = 15 * time.Minute
	// browserMaxPageBytes 服务端抓取页面/资源的大小上限。
	browserMaxPageBytes = 4 << 20
	// browserFetchTimeout 抓取待承载页面/资源的超时。
	browserFetchTimeout = 20 * time.Second
	// browserModeWait 需要回传 DOM（startBrowserAwait）。
	browserModeWait = "wait"
	// browserModeOpen 只展示（startBrowser / showBrowser）。
	browserModeOpen = "open"
)

// BrowserPage 前端待承载的一个页面（登录对话框轮询用）。
type BrowserPage struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Mode wait = 需回传（点 √ 后回传 DOM）；open = 仅展示。
	Mode string `json:"mode"`
	// Seq 登记序号（单调递增）。待办表可能同时存多个页面，前端按它取最新的
	// 那个展示——不能依赖返回顺序，map 遍历是随机的。
	Seq int64 `json:"seq"`
	// PageURL 同源承载地址（iframe src），带 HMAC 签名。
	PageURL string `json:"page_url"`
	// Refetch 见 rule.BrowserTask.Refetch。
	Refetch bool `json:"refetch"`
	// SourceID 发起该请求的书源。
	SourceID string `json:"source_id"`
	// TargetURL 原始地址（data: 与超长地址不回传）。
	TargetURL string `json:"target_url,omitempty"`
}

// BrowserPageSnapshot 承载页面时返回的数据。
type BrowserPageSnapshot struct {
	ID       string
	HTML     string
	SourceID string
	FinalURL string
}

// pendingBrowser 一个待用户完成的页面。
// seq 为登记序号（越大越新，见 ReaderService.browserSeq），供前端挑选最新页面。
type pendingBrowser struct {
	id        string
	seq       int64
	sourceID  string
	sourceURL string
	userID    string
	request   rule.BrowserTask
	// cookies 是发起这次页面的会话 Cookie 累积区：页面在面板里发出的
	// 请求（接口代理 / 资源代理）会把 Set-Cookie 写回它，用户点「完成」后
	// 书源会话就能读到登录凭证。
	cookies browserCookieTarget
	// html 已就绪的页面源码（data: 直接解码；http 抓取后资源地址已改写）。
	html     string
	finalURL string
	mode     string
	expires  time.Time

	done     chan struct{}
	mu       sync.Mutex
	result   rule.BrowserResult
	err      error
	finished bool
}

// browserHost 把服务层的浏览器面板绑定到某个书源会话（实现 rule.BrowserHost）。
type browserHost struct {
	svc       *ReaderService
	sourceURL string
	sourceID  string
	userID    string
	// state / capture 是这次会话的 Cookie 累积区与 enabledCookieJar 开关。
	// 面板承载的页面自己发起的请求（ProxyBrowserXHR / 资源代理）会把
	// 响应 Set-Cookie 写进 state，从而让「页面里登录成功 = 书源会话拿到凭证」。
	state   *sourceState
	capture bool
}

func (h *browserHost) AwaitBrowser(ctx context.Context, req rule.BrowserTask) (rule.BrowserResult, error) {
	return h.svc.awaitBrowser(ctx, h.sourceURL, h.sourceID, h.userID, h.cookieTarget(), req)
}

func (h *browserHost) OpenBrowser(ctx context.Context, req rule.BrowserTask) error {
	return h.svc.openBrowser(ctx, h.sourceURL, h.sourceID, h.userID, h.cookieTarget(), req)
}

// cookieTarget 打包会话状态与开关；capture 为假时返回携带 nil state 的目标。
func (h *browserHost) cookieTarget() browserCookieTarget {
	if !h.capture {
		return browserCookieTarget{}
	}
	return browserCookieTarget{state: h.state}
}

// browserCookieTarget 面板链路要回写 Cookie 的目标会话。
type browserCookieTarget struct {
	state *sourceState
}

// captureFrom 把这次请求（含重定向各跳）下发的 Set-Cookie 写回会话。
// ctx 上挂了 sink 时以它为准（逐跳收集，见 cookies.go），最后再补最终响应。
// 归属域一律 Cookie 的 Domain 优先、否则该跳地址。
func (t browserCookieTarget) captureFrom(ctx context.Context, resp *http.Response) {
	if t.state == nil || resp == nil {
		return
	}
	sink := cookieSinkFrom(ctx)
	if sink == nil {
		sink = &cookieSink{}
	}
	if resp.Request != nil {
		sink.add(resp.Request.URL, resp)
	}
	sink.apply(t.state)
}

// ─── 服务层入口 ────────────────────────────────────────────────────────────

// awaitBrowser 登记待办并阻塞等待用户回传页面内容。
func (s *ReaderService) awaitBrowser(ctx context.Context, sourceURL, sourceID, userID string, cookies browserCookieTarget, req rule.BrowserTask) (rule.BrowserResult, error) {
	entry, err := s.registerBrowser(ctx, sourceURL, sourceID, userID, cookies, req, browserModeWait)
	if err != nil {
		return rule.BrowserResult{}, err
	}
	defer s.dropBrowser(entry.id)

	timeout := time.NewTimer(browserAwaitTimeout)
	defer timeout.Stop()
	select {
	case <-entry.done:
		entry.mu.Lock()
		defer entry.mu.Unlock()
		if entry.err != nil {
			return rule.BrowserResult{}, entry.err
		}
		return entry.result, nil
	case <-ctx.Done():
		return rule.BrowserResult{}, ctx.Err()
	case <-timeout.C:
		return rule.BrowserResult{}, fmt.Errorf("等待页面操作超时（%s），请重试", browserAwaitTimeout)
	}
}

// openBrowser 登记待办但不等待（页面展示给用户即可）。
func (s *ReaderService) openBrowser(ctx context.Context, sourceURL, sourceID, userID string, cookies browserCookieTarget, req rule.BrowserTask) error {
	_, err := s.registerBrowser(ctx, sourceURL, sourceID, userID, cookies, req, browserModeOpen)
	return err
}

// registerBrowser 准备页面内容并登记待办。
func (s *ReaderService) registerBrowser(ctx context.Context, sourceURL, sourceID, userID string, cookies browserCookieTarget, req rule.BrowserTask, mode string) (*pendingBrowser, error) {
	// 先分配 ID：页面里的资源代理地址需要用它签名。
	id := newBrowserID()
	html, finalURL, err := s.prepareBrowserPage(ctx, sourceURL, id, cookies, req)
	if err != nil {
		return nil, err
	}
	if finalURL == "" {
		finalURL = req.URL
	}
	entry := &pendingBrowser{
		id:        id,
		sourceID:  sourceID,
		sourceURL: sourceURL,
		userID:    userID,
		request:   req,
		cookies:   cookies,
		html:      html,
		finalURL:  finalURL,
		mode:      mode,
		expires:   time.Now().Add(browserPageTTL),
		done:      make(chan struct{}),
	}

	s.browserMu.Lock()
	if s.browserPending == nil {
		s.browserPending = map[string]*pendingBrowser{}
	}
	s.pruneBrowsersLocked()
	s.browserSeq++
	entry.seq = s.browserSeq
	s.browserPending[id] = entry
	s.browserMu.Unlock()

	if s.log != nil {
		s.log.Info("reader:待用户完成页面操作",
			zap.String("source", sourceID), zap.String("mode", mode), zap.String("title", req.Title))
	}
	return entry, nil
}

// browserBridgeScript 注入到承载页面里：父窗口请求时回传当前 DOM，把页面自己的
// fetch / XMLHttpRequest 转交给父窗口代为请求，并补上沙箱里不可用的存储对象。
//
// 为什么不让父窗口直接读 contentDocument：那要求 iframe 同源，而书源页面是
// 第三方 HTML，拿到同源权限就能读写 MeBox 自己的页面与 localStorage（JWT）。
// 改成 postMessage 后，iframe 可以只开 allow-scripts（不透明源）。
//
// 为什么要补存储对象：不透明源里访问 localStorage / sessionStorage / document.cookie
// 会直接抛 SecurityError，SPA 类的「用户后台」在启动读 token 时就崩了，
// 表现是一直停在「正在安全加载」。这里用内存版兜住，既不给出同源权限，
// 又让页面能正常跑完启动流程。
//
// 为什么要代发请求：iframe 是不透明源，页面自己的 XHR 既带不上书源 Cookie，
// 也会被 CORS 拦掉——靠接口取数的页面会取不到数据。
const browserBridgeScript = `<script data-mebox-browser-bridge="1">(function(){` +
	`if(window.__meboxBridgeInstalled){return}window.__meboxBridgeInstalled=true;` +
	// ── 沙箱内存储兜底 ──
	`function memStore(){var d={};return{` +
	`getItem:function(k){k=String(k);return Object.prototype.hasOwnProperty.call(d,k)?d[k]:null},` +
	`setItem:function(k,v){d[String(k)]=String(v)},` +
	`removeItem:function(k){delete d[String(k)]},` +
	`clear:function(){d={}},` +
	`key:function(i){var ks=Object.keys(d);return i>=0&&i<ks.length?ks[i]:null},` +
	`get length(){return Object.keys(d).length}}}` +
	`function patchStore(n){try{var s=memStore();Object.defineProperty(window,n,{configurable:true,get:function(){return s}})}catch(e){}}` +
	`patchStore('localStorage');patchStore('sessionStorage');` +
	// document.cookie 也要给：这类页面常用它判断是否已登录（真实 Cookie 在服务端，
	// 浏览器里没有）。把书源在该站点的 Cookie 预置进去，页面才会正常渲染。
	`var ck={};` +
	`(function(seed){String(seed||'').split(';').forEach(function(p){var i=p.indexOf('=');if(i>0){ck[p.slice(0,i).trim()]=p.slice(i+1).trim()}})})` + "(`__MEBOX_COOKIE__`)" + `;` +
	`try{Object.defineProperty(document,'cookie',{configurable:true,` +
	`get:function(){var o=[];for(var k in ck){o.push(k+'='+ck[k])}return o.join('; ')},` +
	`set:function(v){try{var p=String(v).split(';')[0].split('=');if(p[0]){ck[p[0].trim()]=p.slice(1).join('=')}}catch(e){}}})}catch(e){}` +
	// 书源页面自身的基地址：相对地址要按它解析，而不是按承载地址（我们自己的源）
	`var BASE=` + "`__MEBOX_BASE_URL__`" + `;` +
	`function abs(u){try{return new URL(u,BASE||location.href).href}catch(e){try{return new URL(u,location.href).href}catch(e2){return u}}}` +
	// ── DOM 回传 ──
	`window.addEventListener('message',function(e){` +
	`if(!e||e.data!=='__mebox_dom__'){return}` +
	`try{parent.postMessage({__mebox_dom__:true,html:document.documentElement.outerHTML},'*')}catch(err){}` +
	`},false);` +
	// ── 请求代理（父窗口持有登录态，由它转交服务端补 Cookie） ──
	`var seq=0,waiters={};` +
	`window.addEventListener('message',function(e){` +
	`var d=e&&e.data;` +
	`if(!d||d.__mebox_proxy_res__!==true){return}` +
	`var w=waiters[d.reqId];` +
	`if(!w){return}delete waiters[d.reqId];w(d);` +
	`},false);` +
	`function rpc(method,url,headers,body){` +
	`return new Promise(function(resolve,reject){` +
	`var id='r'+(++seq);waiters[id]=resolve;` +
	`try{parent.postMessage({__mebox_proxy__:true,reqId:id,url:url,method:method,headers:headers||{},body:body||''},'*')}catch(e){delete waiters[id];reject(e);return}` +
	`setTimeout(function(){if(waiters[id]){delete waiters[id];reject(new Error('mebox proxy timeout'))}},60000);` +
	`})}` +
	`function decode(res){return res&&res.base64?atob(res.body||''):((res&&res.body)||'')}` +
	`var origFetch=window.fetch;` +
	`if(origFetch){window.fetch=function(input,init){` +
	`init=init||{};` +
	`var url=typeof input==='string'?input:(input&&input.url);` +
	`if(!url){return origFetch.apply(this,arguments)}` +
	`var method=String(init.method||(input&&input.method)||'GET').toUpperCase();` +
	`var headers={};` +
	`try{new Headers(init.headers||(input&&input.headers)||{}).forEach(function(v,k){headers[k]=v})}catch(e){}` +
	`var body=init.body?String(init.body):'';` +
	`return rpc(method,abs(url),headers,body).then(function(res){` +
	`return new Response(decode(res),{status:(res&&res.status)||200,headers:{'Content-Type':(res&&res.contentType)||'text/plain'}})` +
	`})}}` +
	`var OrigXHR=window.XMLHttpRequest;` +
	`function ProxyXHR(){this._h={};this._m='GET';this._u='';this.readyState=0;this.status=0;this.response=null;this.responseText='';this.responseType=''}` +
	`ProxyXHR.prototype.open=function(m,u){this._m=m;this._u=u;this.readyState=1;this._fire('onreadystatechange')};` +
	`ProxyXHR.prototype.setRequestHeader=function(k,v){this._h[k]=v};` +
	`ProxyXHR.prototype._fire=function(n){try{if(this[n])this[n]()}catch(e){}};` +
	`ProxyXHR.prototype.send=function(body){` +
	`var self=this;` +
	`rpc(String(this._m).toUpperCase(),abs(this._u),this._h,body?String(body):'').then(function(res){` +
	`self.status=(res&&res.status)||200;self.readyState=4;self.responseText=decode(res);` +
	`if(self.responseType==='json'){try{self.response=JSON.parse(self.responseText)}catch(e){self.response=null}}else{self.response=self.responseText}` +
	`self._fire('onreadystatechange');self._fire('onload')` +
	`},function(){self.status=0;self.readyState=4;self._fire('onreadystatechange');self._fire('onerror')})};` +
	`ProxyXHR.prototype.getResponseHeader=function(){return null};` +
	`ProxyXHR.prototype.getAllResponseHeaders=function(){return ''};` +
	`ProxyXHR.prototype.abort=function(){};` +
	`window.XMLHttpRequest=ProxyXHR;` +
	`})();</script>`

// injectBrowserBridge 把回传/代理脚本插进页面 <head>（没有 head 就插在最前面），
// 并把书源页面的基地址与 Cookie 填进脚本：前者供相对地址解析，后者供页面判断
// 登录态（真实 Cookie 在服务端，页面看不到）。
func injectBrowserBridge(html, baseURL, cookie string) string {
	if strings.Contains(html, `data-mebox-browser-bridge`) {
		return html
	}
	script := strings.ReplaceAll(browserBridgeScript, "__MEBOX_BASE_URL__", jsStringEscape(baseURL))
	script = strings.ReplaceAll(script, "__MEBOX_COOKIE__", jsStringEscape(cookie))
	lower := strings.ToLower(html)
	if i := strings.Index(lower, "<head>"); i >= 0 {
		pos := i + len("<head>")
		return html[:pos] + script + html[pos:]
	}
	if i := strings.Index(lower, "<html"); i >= 0 {
		if j := strings.Index(lower[i:], ">"); j >= 0 {
			pos := i + j + 1
			return html[:pos] + script + html[pos:]
		}
	}
	return script + html
}

// jsStringEscape 转义要嵌进 JS 模板字符串的地址（反引号 / 反斜杠 / ${）。
func jsStringEscape(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "`", "\\`", "${", "\\${")
	return r.Replace(s)
}

// prepareBrowserPage 得到 iframe 要承载的页面源码。
//
// 返回的页面里会注入回传/代理脚本；脚本需要两样东西：
//   - 基地址：解析页面里的相对地址（书源自带的 data: 页面没有真实地址，
//     退化成书源站点地址——书源自己的 BaseUrl() 也指向它）；
//   - Cookie：页面常用 document.cookie 判断登录态，而真实 Cookie 在服务端，
//     不预置的话「用户后台」会以为未登录并把浏览器导到 /login。
func (s *ReaderService) prepareBrowserPage(ctx context.Context, sourceURL, id string, cookies browserCookieTarget, req rule.BrowserTask) (string, string, error) {
	// 1) 书源自带 HTML（显式 html 参数或 data: URL）
	if html := strings.TrimSpace(req.HTML); html != "" {
		return injectBrowserBridge(html, sourceURL, s.browserCookieHeader(ctx, sourceURL, sourceURL)), req.URL, nil
	}
	if html := rule.ParseDataHTML(req.URL); html != "" {
		return injectBrowserBridge(html, sourceURL, s.browserCookieHeader(ctx, sourceURL, sourceURL)), req.URL, nil
	}
	// 2) 外部地址：服务端带书源 Cookie 抓取，并把资源地址改写到代理
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		return "", "", fmt.Errorf("无法承载该地址: %s", truncateForLog(req.URL, 120))
	}
	body, finalURL, contentType, err := s.fetchBrowserPage(ctx, sourceURL, cookies, req.URL)
	if err != nil {
		return "", "", fmt.Errorf("打开页面失败: %w", err)
	}
	// 空响应没有可展示的内容：以前照样包一层空 <pre> 交给 iframe，面板就是一片白，
	// 用户看不出是地址有问题还是自己没登录。这里直接报错（如段评地址参数被改写、
	// 上游按缺参数返回空 body 的情形）。
	if strings.TrimSpace(body) == "" {
		return "", "", fmt.Errorf("目标地址没有返回任何内容: %s", truncateForLog(req.URL, 120))
	}
	cookie := s.browserCookieHeader(ctx, sourceURL, finalURL)
	if !strings.Contains(strings.ToLower(contentType), "html") {
		// 非 HTML（如 JSON 接口）：包一层 <pre>，至少让用户看到内容
		wrapped := "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>" + htmlEscape(req.Title) +
			"</title></head><body><pre style=\"white-space:pre-wrap;word-break:break-all;padding:16px;" +
			"font:13px/1.6 ui-monospace,monospace\">" + htmlEscape(body) + "</pre></body></html>"
		return injectBrowserBridge(wrapped, finalURL, cookie), finalURL, nil
	}
	return injectBrowserBridge(s.rewriteBrowserHTML(body, finalURL, id), finalURL, cookie), finalURL, nil
}

// browserCookieHeader 取书源在目标站点上的 Cookie 串（页面里预置 document.cookie 用）。
func (s *ReaderService) browserCookieHeader(ctx context.Context, sourceURL, target string) string {
	if s.repo == nil || sourceURL == "" {
		return ""
	}
	return s.newSourceState(ctx, sourceURL).CookieForRequest(target)
}

// fetchBrowserPage 服务端抓取页面（附带书源 Cookie / 登录请求头 / 书源请求头）。
func (s *ReaderService) fetchBrowserPage(ctx context.Context, sourceURL string, cookies browserCookieTarget, target string) (string, string, string, error) {
	contentType, _, data, finalURL, err := s.requestBrowserResource(ctx, sourceURL, cookies, target)
	if err != nil {
		return "", "", "", err
	}
	return string(data), finalURL, contentType, nil
}

// FetchBrowserAsset 代理拉取页面资源（带书源 Cookie/请求头），供 iframe 内引用。
// 返回 (contentType, status, body, error)。按待办 ID 找会话，顺带把响应
// Set-Cookie 写回（页面可能靠资源响应续期会话）。
func (s *ReaderService) FetchBrowserAsset(ctx context.Context, id, target string) (string, int, []byte, error) {
	entry := s.lookupBrowser(id)
	if entry == nil {
		return "", 0, nil, errors.New("页面已过期，请重新打开")
	}
	contentType, status, data, _, err := s.requestBrowserResource(ctx, entry.sourceURL, entry.cookies, target)
	return contentType, status, data, err
}

// requestBrowserResource 带书源凭据请求一个外部地址。
func (s *ReaderService) requestBrowserResource(ctx context.Context, sourceURL string, cookies browserCookieTarget, target string) (string, int, []byte, string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, browserFetchTimeout)
	defer cancel()
	// 挂上 sink，让重定向各跳的 Set-Cookie 也能被收集。
	if cookies.state != nil {
		reqCtx = withCookieSink(reqCtx, &cookieSink{})
	}

	state := s.newSourceState(reqCtx, sourceURL)
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return "", 0, nil, "", err
	}
	for k, v := range state.LoginHeaderMap() {
		if strings.EqualFold(k, "cookie") {
			continue
		}
		httpReq.Header.Set(k, v)
	}
	if ck := state.CookieForRequest(target); ck != "" {
		httpReq.Header.Set("Cookie", ck)
	}
	// 书源级请求头（含 Referer / UA），与阅读请求保持一致
	if s.repo != nil {
		if src, findErr := s.repo.GetSourceByURL(reqCtx, sourceURL); findErr == nil && src != nil && src.Header != "" {
			var headers map[string]any
			if json.Unmarshal([]byte(src.Header), &headers) == nil {
				for k, v := range headers {
					if httpReq.Header.Get(k) == "" {
						httpReq.Header.Set(k, fmt.Sprintf("%v", v))
					}
				}
			}
		}
	}
	// Accept-Encoding 交给 net/http 管：显式设置会让它放弃自动解压，
	// 压缩过的页面/资源就会以原始字节回到 iframe（页面直接白屏或乱码）。
	helper.StripAcceptEncoding(httpReq.Header)
	resp, err := s.http.Do(httpReq)
	if err != nil {
		return "", 0, nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, browserMaxPageBytes))
	if err != nil {
		return "", 0, nil, "", err
	}
	data = helper.DecompressBody(resp, data)
	finalURL := target
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	// 页面/资源响应下发的 Set-Cookie 也写回会话（含跳转各跳）。
	cookies.captureFrom(reqCtx, resp)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return contentType, resp.StatusCode, data, finalURL, nil
}

// BrowserXHRResult 页面内 fetch/XHR 经服务端转发后的响应。
type BrowserXHRResult struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Body        string `json:"body"`
	// Base64 为真时 Body 是 base64（二进制资源）。
	Base64 bool `json:"base64"`
}

// browserXHRMaxBytes 转发接口响应的大小上限。
const browserXHRMaxBytes = 4 << 20

// ProxyBrowserXHR 以书源身份转发页面内的接口请求。
//
// 承载页面是不透明源，页面自己的 XHR 既带不上书源 Cookie 也会被 CORS 拦掉，
// 所以由父窗口把请求转交进来，这里补上书源凭据再发出去。
// 返回的 HTTP 状态/内容类型原样回给页面，让页面自己的逻辑能正常分支。
func (s *ReaderService) ProxyBrowserXHR(ctx context.Context, id, method, target string, headers map[string]string, body string) (*BrowserXHRResult, error) {
	entry := s.lookupBrowser(id)
	if entry == nil {
		return nil, errors.New("页面已过期，请重新打开")
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		return nil, fmt.Errorf("仅支持 http(s) 地址: %s", truncateForLog(target, 120))
	}
	if method == "" {
		method = http.MethodGet
	}
	reqCtx, cancel := context.WithTimeout(ctx, browserFetchTimeout)
	defer cancel()
	// 挂上 sink：页面自己发起的请求（如扫码登录的轮询/取票跳转）下发的
	// Set-Cookie 要能落到书源会话里，否则「面板里登录成功」书源却始终未登录。
	if entry.cookies.state != nil {
		reqCtx = withCookieSink(reqCtx, &cookieSink{})
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, strings.ToUpper(method), target, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	// 先放页面自己声明的头，再用书源凭据补缺（Cookie 始终以服务端为准）
	for k, v := range headers {
		if strings.EqualFold(k, "cookie") || strings.EqualFold(k, "host") ||
			strings.EqualFold(k, "content-length") {
			continue
		}
		httpReq.Header.Set(k, v)
	}
	state := s.newSourceState(reqCtx, entry.sourceURL)
	for k, v := range state.LoginHeaderMap() {
		if strings.EqualFold(k, "cookie") {
			continue
		}
		if httpReq.Header.Get(k) == "" {
			httpReq.Header.Set(k, v)
		}
	}
	if httpReq.Header.Get("Cookie") == "" {
		if ck := state.CookieForRequest(target); ck != "" {
			httpReq.Header.Set("Cookie", ck)
		}
	}
	if s.repo != nil {
		if src, findErr := s.repo.GetSourceByURL(reqCtx, entry.sourceURL); findErr == nil && src != nil && src.Header != "" {
			var extra map[string]any
			if json.Unmarshal([]byte(src.Header), &extra) == nil {
				for k, v := range extra {
					if httpReq.Header.Get(k) == "" {
						httpReq.Header.Set(k, fmt.Sprintf("%v", v))
					}
				}
			}
		}
	}
	helper.StripAcceptEncoding(httpReq.Header)
	resp, err := s.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, browserXHRMaxBytes))
	if err != nil {
		return nil, err
	}
	data = helper.DecompressBody(resp, data)
	// 页面请求同样把 Set-Cookie 写回书源会话（含跳转各跳）。
	entry.cookies.captureFrom(reqCtx, resp)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/plain"
	}
	out := &BrowserXHRResult{Status: resp.StatusCode, ContentType: contentType}
	if isTextualContent(contentType) {
		out.Body = string(data)
	} else {
		out.Body = base64.StdEncoding.EncodeToString(data)
		out.Base64 = true
	}
	return out, nil
}

// isTextualContent 判断响应是否可以直接当字符串交给页面。
func isTextualContent(contentType string) bool {
	ct := strings.ToLower(contentType)
	for _, p := range []string{"text/", "json", "javascript", "xml", "html", "x-www-form-urlencoded", "csv"} {
		if strings.Contains(ct, p) {
			return true
		}
	}
	return false
}

// ─── 资源改写与代理 ────────────────────────────────────────────────────────

// cssURLRe 匹配 CSS 里的 url(...) 引用。
// Go 的 regexp 不支持反向引用，因此引号用可选的成对字符类近似匹配。
var cssURLRe = regexp.MustCompile(`url\(\s*['"]?([^'")]+)['"]?\s*\)`)

// rewriteBrowserHTML 把页面里的资源/表单/站内链接地址改写到同源代理，
// 使页面在 iframe 里的后续请求同样带上书源 Cookie。
func (s *ReaderService) rewriteBrowserHTML(body, baseURL, id string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return body
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return body
	}
	rewrite := func(sel, attr string) {
		doc.Find(sel).Each(func(_ int, node *goquery.Selection) {
			val, ok := node.Attr(attr)
			if !ok || strings.TrimSpace(val) == "" {
				return
			}
			if abs := absoluteBrowserURL(base, val); abs != "" {
				node.SetAttr(attr, s.browserAssetProxyURL(id, abs))
			}
		})
	}
	rewrite("img[src]", "src")
	rewrite("script[src]", "src")
	rewrite("link[href]", "href")
	rewrite("iframe[src]", "src")
	rewrite("source[src]", "src")
	rewrite("video[src]", "src")
	rewrite("audio[src]", "src")
	// 表单提交也走代理，避免 POST 丢掉 Cookie
	rewrite("form[action]", "action")
	// 站内链接走代理；站外链接保留（用户可能确实想出去）
	doc.Find("a[href]").Each(func(_ int, node *goquery.Selection) {
		val, ok := node.Attr("href")
		if !ok {
			return
		}
		abs := absoluteBrowserURL(base, val)
		if abs == "" || !sameSite(abs, baseURL) {
			return
		}
		node.SetAttr("href", s.browserAssetProxyURL(id, abs))
	})
	// 内联样式里的 url(...)
	doc.Find("[style]").Each(func(_ int, node *goquery.Selection) {
		style, ok := node.Attr("style")
		if !ok || !strings.Contains(style, "url(") {
			return
		}
		node.SetAttr("style", rewriteCSSURLs(style, base, func(abs string) string {
			return s.browserAssetProxyURL(id, abs)
		}))
	})
	out, err := doc.Html()
	if err != nil {
		return body
	}
	return "<!DOCTYPE html>" + out
}

// RewriteBrowserCSS 改写 CSS 文本里的 url(...) 引用（资源代理用）。
func (s *ReaderService) RewriteBrowserCSS(css, baseURL, id string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return css
	}
	return rewriteCSSURLs(css, base, func(abs string) string {
		return s.browserAssetProxyURL(id, abs)
	})
}

func rewriteCSSURLs(css string, base *url.URL, proxy func(abs string) string) string {
	return cssURLRe.ReplaceAllStringFunc(css, func(m string) string {
		sub := cssURLRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		abs := absoluteBrowserURL(base, sub[1])
		if abs == "" {
			return m
		}
		return `url("` + proxy(abs) + `")`
	})
}

// absoluteBrowserURL 把页面里的相对地址解析成绝对地址；不可代理的协议返回空串。
func absoluteBrowserURL(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "#") {
		return ""
	}
	lower := strings.ToLower(ref)
	for _, p := range []string{"data:", "javascript:", "mailto:", "tel:", "blob:", "about:", "ws:", "wss:"} {
		if strings.HasPrefix(lower, p) {
			return ""
		}
	}
	parsed, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	abs := base.ResolveReference(parsed).String()
	if !strings.HasPrefix(abs, "http://") && !strings.HasPrefix(abs, "https://") {
		return ""
	}
	return abs
}

// sameSite 判断两个地址是否同站（eTLD+1）。
func sameSite(a, b string) bool {
	return rule.CookieDomain(a) == rule.CookieDomain(b)
}

// ─── 签名与地址 ────────────────────────────────────────────────────────────

// browserSign 用 JWT 密钥做 HMAC，绑定待办 ID（页面）或 ID+目标地址（资源）。
func (s *ReaderService) browserSign(payload string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.Secrets.JWTSecret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func (s *ReaderService) browserPageSig(id string) string { return s.browserSign("page|" + id) }

func (s *ReaderService) browserAssetSig(id, target string) string {
	return s.browserSign("asset|" + id + "|" + target)
}

// browserPageURL 生成 iframe 承载地址。
//
// 承载页面常常靠 location.search 取值：光遇聚合的段评页就是从 ?item_id&para&source
// 读出「哪本书的哪一段、哪个来源」，再据此请求 /para_review。承载地址是我们自己的
// 代理路径，页面看到的 query 只有 id/s，原地址的参数全丢了——页面于是按缺参数去
// 请求评论接口，气泡数照样显示，点开却一条段评都没有。
//
// 所以把原地址的 query 原样拼在签名参数之后：页面看到的 search 与原站一致。
// 我们自己的 id/s 放最前，Gin 取同名的第一个值，不会被原地址里的同名参数顶掉。
func (s *ReaderService) browserPageURL(id, target string) string {
	u := "/api/reader/browser/page?id=" + url.QueryEscape(id) + "&s=" + s.browserPageSig(id)
	if q := browserTargetQuery(target); q != "" {
		u += "&" + q
	}
	return u
}

// browserTargetQuery 取原地址的 query（不含 `?`）；data: 地址或解析失败时返回空串。
func browserTargetQuery(target string) string {
	target = strings.TrimSpace(target)
	if target == "" || strings.HasPrefix(strings.ToLower(target), "data:") {
		return ""
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return ""
	}
	return parsed.RawQuery
}

// browserAssetProxyURL 生成资源代理地址（iframe 页面内引用用）。
func (s *ReaderService) browserAssetProxyURL(id, target string) string {
	return "/api/reader/browser/asset?id=" + url.QueryEscape(id) +
		"&s=" + s.browserAssetSig(id, target) +
		"&u=" + base64.RawURLEncoding.EncodeToString([]byte(target))
}

// VerifyBrowserPage 校验页面承载签名，返回待办快照。
func (s *ReaderService) VerifyBrowserPage(id, sig string) (*BrowserPageSnapshot, error) {
	if !hmac.Equal([]byte(s.browserPageSig(id)), []byte(sig)) {
		return nil, errors.New("页面地址签名校验失败")
	}
	entry := s.lookupBrowser(id)
	if entry == nil {
		return nil, errors.New("页面已过期，请重新打开")
	}
	return &BrowserPageSnapshot{
		ID:       entry.id,
		HTML:     entry.html,
		SourceID: entry.sourceID,
		FinalURL: entry.finalURL,
	}, nil
}

// VerifyBrowserAsset 校验资源代理签名并还原目标地址，返回（目标地址, 书源 URL）。
func (s *ReaderService) VerifyBrowserAsset(id, encoded, sig string) (string, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", errors.New("资源地址解码失败")
	}
	target := string(raw)
	if !hmac.Equal([]byte(s.browserAssetSig(id, target)), []byte(sig)) {
		return "", "", errors.New("资源地址签名校验失败")
	}
	entry := s.lookupBrowser(id)
	if entry == nil {
		return "", "", errors.New("页面已过期，请重新打开")
	}
	return target, entry.sourceURL, nil
}

// ─── 待办表 ────────────────────────────────────────────────────────────────

// PendingBrowserPages 返回该用户在某书源下待用户完成的页面（前端轮询）。
// 含 open 模式：startBrowser 只展示不回传，同样需要前端把页面呈现出来。
func (s *ReaderService) PendingBrowserPages(userID, sourceID string) []BrowserPage {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	s.pruneBrowsersLocked()
	out := make([]BrowserPage, 0, len(s.browserPending))
	for _, e := range s.browserPending {
		if e.sourceID != sourceID {
			continue
		}
		// 已交付/已取消的页面不再返回：否则前端下一次轮询会把它当成新页面弹出来。
		if e.finished {
			continue
		}
		// 待办是短生命周期对象；未标注用户的老调用路径一律放行。
		if e.userID != "" && userID != "" && e.userID != userID {
			continue
		}
		out = append(out, s.pageOfLocked(e))
	}
	return out
}

// ResolveBrowser 用户完成/取消页面后回传结果，解除 JS 侧的阻塞。
//
// 交付后立刻把条目移出待办表：open 模式（java.startBrowser）没有别的清理
// 路径，留着会一直躺到 TTL 到期——光遇聚合的「❇️ 更新书源」就是这么在
// 15 分钟内反复串到其它按钮上的。
func (s *ReaderService) ResolveBrowser(id, otherUserID, body, finalURL string, cancelled bool) error {
	entry := s.lookupBrowser(id)
	if entry == nil {
		return errors.New("页面已过期或已完成")
	}
	// 注意锁序：browserMu 必须在 entry.mu 之外获取（pruneBrowsersLocked 是
	// browserMu → entry.mu），这里不能持着 entry.mu 去等 browserMu。
	entry.mu.Lock()
	if entry.finished {
		entry.mu.Unlock()
		return errors.New("该页面已完成")
	}
	if cancelled {
		entry.err = rule.ErrBrowserCancelled
	} else {
		resultURL := strings.TrimSpace(finalURL)
		if resultURL == "" {
			resultURL = entry.finalURL
		}
		entry.result = rule.BrowserResult{URL: resultURL, Body: body}
	}
	entry.finished = true
	if entry.done != nil {
		close(entry.done)
	}
	entry.mu.Unlock()

	s.dropBrowser(id)
	return nil
}

func (s *ReaderService) lookupBrowser(id string) *pendingBrowser {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	s.pruneBrowsersLocked()
	return s.browserPending[id]
}

func (s *ReaderService) dropBrowser(id string) {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	delete(s.browserPending, id)
}

// pruneBrowsersLocked 清掉过期条目并唤醒仍在等待的调用方（避免泄漏协程）。
func (s *ReaderService) pruneBrowsersLocked() {
	now := time.Now()
	for id, e := range s.browserPending {
		if now.Before(e.expires) {
			continue
		}
		e.mu.Lock()
		if !e.finished {
			e.finished = true
			e.err = errors.New("页面等待超时")
			if e.done != nil {
				close(e.done)
			}
		}
		e.mu.Unlock()
		delete(s.browserPending, id)
	}
}

// browserPageOf 生成前端展示用的描述（自行加锁）。
// registerBrowser 之后需要把页面直接回给调用方（如打开一条段评）时用它。
func (s *ReaderService) browserPageOf(e *pendingBrowser) BrowserPage {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	return s.pageOfLocked(e)
}

// pageOfLocked 生成前端展示用的描述（调用方需持有 browserMu）。
func (s *ReaderService) pageOfLocked(e *pendingBrowser) BrowserPage {
	target := e.request.URL
	if strings.HasPrefix(target, "data:") || len(target) > 512 {
		target = ""
	}
	return BrowserPage{
		ID:        e.id,
		Title:     e.request.Title,
		Mode:      e.mode,
		Seq:       e.seq,
		PageURL:   s.browserPageURL(e.id, e.request.URL),
		Refetch:   e.request.Refetch,
		SourceID:  e.sourceID,
		TargetURL: target,
	}
}

// ─── 小工具 ────────────────────────────────────────────────────────────────

func newBrowserID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
