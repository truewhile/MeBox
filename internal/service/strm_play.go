// STRM 播放端点解析：strm 文件内容指向 /api/strm/play/{provider}，服务端
// 依据账号凭据解析出直链：能 302 的走 302（115/OpenList API），需要携带请求
// 头（CloudDrive2 WebDAV）的走反向代理；本地源直接以静态文件方式提供。
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/cloud"
)

// ErrStrmPlayNotFound 表示 strm 播放目标不存在（handler 返回 404）。
var ErrStrmPlayNotFound = errors.New("strm play target not found")

// StrmPlayResult 是播放解析结果。
type StrmPlayResult struct {
	// RedirectURL 非空时 handler 直接 302 到该地址。
	RedirectURL string
	// LocalPath 非空时 handler 以静态文件方式提供（本地源）。
	LocalPath string
	// Link 非空且 Proxy 为 true 时 handler 反向代理该直链。
	Link  *cloud.DirectLink
	Proxy bool
}

// ResolvePlay 解析 strm 播放请求。
func (s *StrmService) ResolvePlay(ctx context.Context, provider string, q url.Values) (*StrmPlayResult, error) {
	switch provider {
	case model.StrmProviderLocal:
		return s.resolveLocalPlay(ctx, q.Get("path"))
	case model.StrmProvider115:
		return s.resolveCloudPlay(ctx, provider, q, "pickcode")
	case model.StrmProviderCloudDrive, model.StrmProviderOpenList:
		return s.resolveCloudPlay(ctx, provider, q, "ref")
	default:
		return nil, errors.New("未知的 STRM 提供方")
	}
}

func (s *StrmService) resolveCloudPlay(ctx context.Context, provider string, q url.Values, refKey string) (*StrmPlayResult, error) {
	acctID := q.Get("acct")
	ref := q.Get(refKey)
	if acctID == "" || ref == "" {
		return nil, fmt.Errorf("缺少 %s 参数", refKey)
	}
	acct, err := s.repo.StrmAccount.FindByID(ctx, acctID)
	if err != nil || acct == nil {
		return nil, errors.New("网盘账号不存在")
	}
	if !acct.Enabled {
		return nil, errors.New("网盘账号已禁用")
	}
	if acct.Provider != provider {
		return nil, errors.New("网盘账号类型不匹配")
	}
	p, err := s.providerFor(ctx, acct)
	if err != nil {
		return nil, err
	}
	var link *cloud.DirectLink
	ua := q.Get("__ua")
	if uaProvider, ok := p.(interface {
		ResolveWithUA(ctx context.Context, fileRef, ua string) (*cloud.DirectLink, error)
	}); ok && ua != "" {
		link, err = uaProvider.ResolveWithUA(ctx, ref, ua)
	} else {
		link, err = p.Resolve(ctx, ref)
	}
	if err != nil {
		return nil, err
	}
	if link == nil || link.URL == "" {
		return nil, errors.New("解析播放地址失败")
	}
	if link.Proxy {
		return &StrmPlayResult{Link: link, Proxy: true}, nil
	}
	// Link 一并保留：302 处理器只认 RedirectURL，但服务端直连（弹幕 hash
	// 拉取）需要 link.Headers 才能通过直链防盗链校验。
	return &StrmPlayResult{RedirectURL: link.URL, Link: link}, nil
}

// resolveLocalPlay 本地源：校验路径位于某个本地同步目录的源目录内。
func (s *StrmService) resolveLocalPlay(ctx context.Context, rawPath string) (*StrmPlayResult, error) {
	if rawPath == "" {
		return nil, errors.New("缺少 path 参数")
	}
	target := filepath.Clean(rawPath)
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		return nil, ErrStrmPlayNotFound
	}
	paths, err := s.repo.StrmSyncPath.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range paths {
		p := &paths[i]
		if p.Provider != model.StrmProviderLocal || !p.Enabled || strings.TrimSpace(p.RemotePath) == "" {
			continue
		}
		root := filepath.Clean(p.RemotePath)
		if target == root || strings.HasPrefix(target, root+string(filepath.Separator)) {
			return &StrmPlayResult{LocalPath: target}, nil
		}
	}
	return nil, errors.New("文件不在任何本地同步目录内")
}

// ResolvePlayTarget 解析媒体行固化的播放目标（STRMURL 或 .strm 文件内容）为
// 可播放结果，供弹幕 hash、内嵌字幕提取等「先解析直链再读取远端」的场景复用。
// 支持：
//   - /api/strm/play/{provider}/video{ext}?acct=..&pickcode=.. （常规格式，含账号）
//   - /api/cloud/play/{type}?ref=.. （旧格式，无账号 → 取该类型第一个启用账号）
//   - 绝对 http(s) 链接（直接透传）
//   - 其余协议（webdav:// 等）返回错误，由调用方决定是否静默跳过
func (s *StrmService) ResolvePlayTarget(ctx context.Context, raw string) (*StrmPlayResult, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("空播放目标")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("解析播放目标失败: %w", err)
	}
	lowerPath := strings.ToLower(u.Path)
	switch {
	case strings.HasPrefix(lowerPath, "/api/strm/play/"):
		segs := strings.Split(strings.TrimPrefix(u.Path, "/api/strm/play/"), "/")
		if len(segs) < 1 || strings.TrimSpace(segs[0]) == "" {
			return nil, errors.New("无效的 strm 播放地址")
		}
		return s.ResolvePlay(ctx, segs[0], u.Query())
	case strings.HasPrefix(lowerPath, "/api/cloud/play/"):
		typ := strings.TrimSpace(strings.TrimPrefix(u.Path, "/api/cloud/play/"))
		acct, err := s.firstEnabledAccountOf(ctx, typ)
		if err != nil || acct == nil {
			return nil, errors.New("没有可用的网盘账号，无法解析直链")
		}
		q := u.Query()
		q.Set("acct", acct.ID)
		return s.ResolvePlay(ctx, typ, q)
	case u.Scheme == "http" || u.Scheme == "https":
		return &StrmPlayResult{RedirectURL: raw}, nil
	default:
		return nil, fmt.Errorf("不支持的播放目标协议: %s", u.Scheme)
	}
}

// firstEnabledAccountOf 返回指定提供方第一个凭据可用的启用账号。
func (s *StrmService) firstEnabledAccountOf(ctx context.Context, provider string) (*model.StrmAccount, error) {
	accounts, err := s.repo.StrmAccount.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		a := &accounts[i]
		if !a.Enabled || a.Provider != provider {
			continue
		}
		if _, err := s.providerFor(ctx, a); err == nil {
			return a, nil
		}
	}
	return nil, nil
}

// ErrStrmProxyNotApplicable 表示该媒体没有需要服务端转发的直链（本地文件本身同源）。
var ErrStrmProxyNotApplicable = errors.New("strm proxy not applicable")

// ProxyMediaDirect 把媒体行的网盘/STRM 直链解析为真实地址后由服务端反向代理给
// 客户端，让浏览器拿到「同源」数据。画质与原文件完全一致，不触发任何转码。
//
// 用途：VR 全景播放要把视频帧读进 WebGL 纹理，而跨域直链（网盘 302 跳到 CDN）
// 在浏览器里属于被污染的资源，WebGL 读取会抛 SecurityError；把流量经服务端转发
// 是「原画 + VR」唯一可行的办法。
func (s *StrmService) ProxyMediaDirect(ctx context.Context, w http.ResponseWriter, r *http.Request, m *model.Media) error {
	if s == nil || m == nil {
		return ErrStrmProxyNotApplicable
	}
	raw := strings.TrimSpace(m.STRMURL)
	if raw == "" {
		path := strings.TrimSpace(m.Path)
		if !strings.HasSuffix(strings.ToLower(path), ".strm") {
			return ErrStrmProxyNotApplicable
		}
		target, err := readLocalSTRMTarget(path)
		if err != nil {
			return err
		}
		raw = strings.TrimSpace(target)
	}
	if raw == "" {
		return ErrStrmProxyNotApplicable
	}
	result, err := s.ResolvePlayTarget(ctx, raw)
	if err != nil {
		return err
	}
	switch {
	case result.Link != nil && result.Link.URL != "":
		return s.ProxyDirect(ctx, w, r, result.Link)
	case result.RedirectURL != "":
		return s.ProxyDirect(ctx, w, r, &cloud.DirectLink{URL: result.RedirectURL})
	default:
		// 本地文件（LocalPath）由静态文件处理器提供，本身就是同源。
		return ErrStrmProxyNotApplicable
	}
}

// ProxyDirect 反向代理渲染直链内容（保留 Range 请求头以支持拖动播放）。
func (s *StrmService) ProxyDirect(ctx context.Context, w http.ResponseWriter, r *http.Request, link *cloud.DirectLink) error {
	if link == nil || link.URL == "" {
		return errors.New("空直链")
	}
	method := http.MethodGet
	if r != nil && r.Method == http.MethodHead {
		method = http.MethodHead
	}
	req, err := http.NewRequestWithContext(ctx, method, link.URL, nil)
	if err != nil {
		return err
	}
	for k, v := range link.Headers {
		req.Header.Set(k, v)
	}
	if r != nil {
		if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		// 部分网盘直链按 UA 防盗链；解析时未绑定 UA 的直链沿用浏览器 UA 更稳。
		if ua := strings.TrimSpace(r.Header.Get("User-Agent")); ua != "" && req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", ua)
		}
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	for _, header := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if value := resp.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	// 原样透传上游状态码：Range 请求必须回 206，改写成 200 会让浏览器误判
	// 响应长度，拖动进度条时反复重新拉流。
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil
	}
	if method == http.MethodHead {
		return nil
	}
	// 边转发边 flush，避免大体积视频被 net/http 的写缓冲切成一段段卡顿。
	writer := io.Writer(w)
	if flusher, ok := w.(http.Flusher); ok {
		writer = &flushWriter{writer: w, flusher: flusher}
	}
	_, err = io.Copy(writer, resp.Body)
	return err
}

type flushWriter struct {
	writer  io.Writer
	flusher http.Flusher
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.writer.Write(p)
	if f.flusher != nil {
		f.flusher.Flush()
	}
	return n, err
}
