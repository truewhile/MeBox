package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"go.uber.org/zap"
)

type remoteImageFetchClient struct {
	name   string
	client *http.Client
}

func (p *ImageProxy) remoteImageFetchClients(host string) []remoteImageFetchClient {
	client := p.client
	if client == nil {
		client = NewExternalHTTPClient(30 * time.Second)
	}
	if _, ok := client.Transport.(*http.Transport); !ok {
		return []remoteImageFetchClient{{name: "default", client: client}}
	}
	timeout := client.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	direct := p.directClient
	if direct == nil {
		direct = &http.Client{Timeout: timeout, Transport: NewInternalTransport()}
	}
	directCandidate := remoteImageFetchClient{name: "direct", client: direct}
	defaultCandidate := remoteImageFetchClient{name: "default", client: client}
	// Hosts the user configured themselves — remote Emby mounts and their image
	// endpoints — are reached over the LAN or a dedicated line. Routing those
	// through the OS/env proxy first costs a failed attempt before every single
	// image, so try the direct client first for them.
	if p.isAllowedRemoteHost(host) {
		return []remoteImageFetchClient{directCandidate, defaultCandidate}
	}
	return []remoteImageFetchClient{defaultCandidate, directCandidate}
}

func (p *ImageProxy) canUseExternalImageFallback() bool {
	if p == nil || p.client == nil {
		return false
	}
	_, ok := p.client.Transport.(*http.Transport)
	return ok
}

// remoteImageFetchResult 是一次成功拉取的结果。正常路径图片已经流式落盘
// （data 为空，调用方从缓存文件下发/缩放）；只有缓存目录不可写、退回内存
// 缓冲时才带 data，保证图片仍能发给客户端。
type remoteImageFetchResult struct {
	data        []byte
	contentType string
}

// fetchRemoteImageOnce 拉取一次上游图片并写入 cachePath。响应体直接流式落盘，
// 不再整张读进内存。
func (p *ImageProxy) fetchRemoteImageOnce(ctx context.Context, raw, host string, candidate remoteImageFetchClient, cachePath, failPath string) (remoteImageFetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		p.log.Warn("imageproxy: build request failed", zap.String("url", redactSensitiveURL(raw)), zap.Error(redactSensitiveError(err)))
		return remoteImageFetchResult{}, errImageProxyRequestSetup
	}
	applyRemoteImageHeaders(req, host, raw)

	resp, err := candidate.client.Do(req)
	if err != nil {
		logImageFetchError(p.log, "imageproxy: upstream fetch failed", host, candidate.name, err)
		return remoteImageFetchResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		p.log.Warn("imageproxy: upstream returned non-OK", zap.String("host", host), zap.String("client", candidate.name), zap.String("status", resp.Status))
		return remoteImageFetchResult{}, errors.New("upstream returned " + resp.Status)
	}
	if err := p.streamImageToCache(cachePath, failPath, resp.Body); err != nil {
		if errors.Is(err, errImageCacheUnavailable) {
			// 缓存目录不可写：响应体还没读，退回内存缓冲。
			return p.bufferRemoteImage(resp, host, candidate.name, cachePath, failPath)
		}
		logImageFetchError(p.log, "imageproxy: stream image failed", host, candidate.name, err)
		return remoteImageFetchResult{}, err
	}
	return remoteImageFetchResult{}, nil
}

// bufferRemoteImage 在缓存不可用时把响应体读进内存，校验为图片后尽力写入
// 缓存（失败也不影响本次下发）。
func (p *ImageProxy) bufferRemoteImage(resp *http.Response, host, client, cachePath, failPath string) (remoteImageFetchResult, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, imageProxyMaxDownloadBytes))
	if err != nil || len(data) == 0 {
		p.log.Warn("imageproxy: read upstream body failed", zap.String("host", host), zap.String("client", client), zap.Error(redactSensitiveError(err)))
		if err == nil {
			err = errors.New("upstream image body is empty")
		}
		return remoteImageFetchResult{}, err
	}
	ctype, ok := validImageContentType(data)
	if !ok {
		p.log.Warn("imageproxy: upstream returned non-image content", zap.String("host", host), zap.String("client", client), zap.String("content_type", resp.Header.Get("Content-Type")))
		return remoteImageFetchResult{}, errImageProxyNonImageContent
	}
	p.writeImageCache(cachePath, failPath, "img-*.tmp", data)
	return remoteImageFetchResult{data: data, contentType: ctype}, nil
}

func logImageFetchError(log *zap.Logger, message, host, client string, err error) {
	if log == nil || err == nil {
		return
	}
	fields := []zap.Field{
		zap.String("host", host),
		zap.String("client", client),
		zap.Error(redactSensitiveError(err)),
	}
	if errors.Is(err, context.Canceled) {
		log.Debug(message, fields...)
		return
	}
	log.Warn(message, fields...)
}

func applyRemoteImageHeaders(req *http.Request, host, raw string) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,ja;q=0.8,en;q=0.7")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	if cookie := remoteImageCookie(host); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if referer := remoteImageReferer(host, raw); referer != "" {
		req.Header.Set("Referer", referer)
	}
}

func remoteImageCookie(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	switch {
	case strings.Contains(h, "javbus") || strings.Contains(h, "busjav") || strings.Contains(h, "dmmbus") || strings.Contains(h, "javsee") || strings.Contains(h, "cdnbus"):
		return "age=verified; existmag=all"
	case strings.Contains(h, "javdb") || strings.Contains(h, "jdbstatic"):
		return "over18=1"
	default:
		return ""
	}
}

func remoteImageReferer(host, raw string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	switch {
	case strings.Contains(h, "doubanio.com"):
		return "https://movie.douban.com/"
	case strings.Contains(h, "bgm.tv"):
		return "https://bgm.tv/"
	case strings.Contains(h, "javbus") || strings.Contains(h, "busjav") || strings.Contains(h, "dmmbus") || strings.Contains(h, "javsee"):
		return "https://www.javbus.com/"
	case strings.Contains(h, "javdb") || strings.Contains(h, "jdbstatic"):
		return "https://javdb.com/"
	case strings.Contains(h, "dmm.co.jp") || strings.Contains(h, "dmm.com"):
		return "https://www.dmm.co.jp/"
	case strings.Contains(h, "mgstage.com"):
		return "https://www.mgstage.com/"
	case strings.Contains(h, "faleno.jp"):
		return "https://faleno.jp/"
	case strings.Contains(h, "fc2.com"):
		return "https://adult.contents.fc2.com/"
	case h != "":
		scheme := "https"
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "http://") {
			scheme = "http"
		}
		return scheme + "://" + h + "/"
	default:
		return ""
	}
}

func isDoubanImageHost(host string) bool {
	return strings.Contains(strings.ToLower(host), "doubanio.com")
}

func fetchRemoteImageWithCurl(ctx context.Context, raw, host string) ([]byte, string, string, error) {
	bin, err := exec.LookPath("curl")
	if err != nil {
		return nil, "", "", err
	}
	curlCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	args := []string{
		"--fail",
		"--location",
		"--silent",
		"--show-error",
		"--http1.1",
		"--max-time", "20",
		"--proto", "=http,https",
		"--proto-redir", "=http,https",
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36",
		"--header", "Accept: image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8",
		"--header", "Accept-Language: zh-CN,zh;q=0.9,en;q=0.8",
		"--header", "Cache-Control: no-cache",
		"--header", "Pragma: no-cache",
	}
	if referer := remoteImageReferer(host, raw); referer != "" {
		args = append(args, "--referer", referer)
	}
	if cookie := remoteImageCookie(host); cookie != "" {
		args = append(args, "--cookie", cookie)
	}
	args = append(args, "--", raw)

	cmd := exec.CommandContext(curlCtx, bin, args...) // #nosec G204 -- bin is resolved by LookPath and args are not shell-expanded.
	stderr := bytes.Buffer{}
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", "", err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, 32<<20))
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, "", "", readErr
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return nil, "", "", redactSensitiveError(errors.New(message))
		}
		return nil, "", "", redactSensitiveError(waitErr)
	}
	if len(data) == 0 {
		return nil, "", "", errors.New("curl image body is empty")
	}
	ctype := detectContentType(data)
	if !isImageContentType(ctype) || isTransparentPlaceholderData(data) {
		return nil, "", "", errors.New("curl returned non-image content")
	}
	return data, ctype, "", nil
}
