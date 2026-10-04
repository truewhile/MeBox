package service

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// remoteEmbyImageTokenTTL 是 host → api_key 的缓存时长。海报墙首屏一口气请求
// 几十张图，逐个回读数据库不值得；token 轮换时 401 重认证会立即覆盖这份缓存。
const remoteEmbyImageTokenTTL = 60 * time.Second

type remoteEmbyImageToken struct {
	token string
	at    time.Time
}

// SetRemoteEmbyAuthProvider 注入「按主机名取当前远程 Emby api_key」的回调。
// 图片代理回源已挂载的远程 Emby 时用它替换 URL 里可能已过期的凭据。
func (p *ImageProxy) SetRemoteEmbyAuthProvider(fn func(ctx context.Context, host string) (string, bool)) {
	if p == nil {
		return
	}
	p.remoteEmbyTokenFn = fn
}

// SetRemoteEmbyAuthRefresher 注入「强制重新登录并返回新 api_key」的回调。上游以
// 401/403 拒绝旧 token 时调用一次，再重试拉图，从而不需要人工改账号配置。
func (p *ImageProxy) SetRemoteEmbyAuthRefresher(fn func(ctx context.Context, host string) (string, error)) {
	if p == nil {
		return
	}
	p.refreshRemoteEmbyTokenFn = fn
}

// remoteEmbyTokenForHost 返回挂载 Emby 的当前 token，短期缓存避免每张图都查库。
// 「不是挂载主机」的结果同样缓存，否则普通图床（TMDb/Douban 等）的每张海报都会
// 白白查一次账号表。
func (p *ImageProxy) remoteEmbyTokenForHost(ctx context.Context, host string) string {
	if p == nil || p.remoteEmbyTokenFn == nil || strings.TrimSpace(host) == "" {
		return ""
	}
	p.remoteEmbyTokenMu.Lock()
	if cached, ok := p.remoteEmbyTokenCache[host]; ok && time.Since(cached.at) < remoteEmbyImageTokenTTL {
		p.remoteEmbyTokenMu.Unlock()
		return cached.token
	}
	p.remoteEmbyTokenMu.Unlock()

	token, ok := p.remoteEmbyTokenFn(ctx, host)
	if !ok {
		token = ""
	}
	p.cacheRemoteEmbyToken(host, token)
	return token
}

func (p *ImageProxy) cacheRemoteEmbyToken(host, token string) {
	if p == nil || strings.TrimSpace(host) == "" {
		return
	}
	p.remoteEmbyTokenMu.Lock()
	if p.remoteEmbyTokenCache == nil {
		p.remoteEmbyTokenCache = make(map[string]remoteEmbyImageToken, 8)
	}
	p.remoteEmbyTokenCache[host] = remoteEmbyImageToken{token: token, at: time.Now()}
	p.remoteEmbyTokenMu.Unlock()
}

// refreshRemoteEmbyTokenForHost 强制重认证一次并刷新缓存。没有配置刷新器、或
// 账号无法自动刷新（如仅填了 api_key）时返回空串，调用方按原错误处理。
func (p *ImageProxy) refreshRemoteEmbyTokenForHost(ctx context.Context, host string) (string, error) {
	if p == nil || p.refreshRemoteEmbyTokenFn == nil || strings.TrimSpace(host) == "" {
		return "", nil
	}
	token, err := p.refreshRemoteEmbyTokenFn(ctx, host)
	if err != nil {
		return "", err
	}
	if token != "" {
		p.cacheRemoteEmbyToken(host, token)
	}
	return token, nil
}

// applyRemoteEmbyAuth 用账号当前 token 覆盖回源请求上的凭据。已配置的远程 Emby
// 以 X-Emby-Token 请求头为准，所以图片 URL 里带的是轮换前的旧 api_key 也不会再
// 被采纳；同时把查询参数里的 api_key 一并改写，避免旧值干扰。
func (p *ImageProxy) applyRemoteEmbyAuth(ctx context.Context, req *http.Request, host string) {
	if p == nil || p.remoteEmbyTokenFn == nil || req == nil || req.URL == nil {
		return
	}
	token := p.remoteEmbyTokenForHost(ctx, host)
	if token == "" {
		return
	}
	req.Header.Set("X-Emby-Token", token)
	q := req.URL.Query()
	q.Set("api_key", token)
	req.URL.RawQuery = q.Encode()
}
