// 115 开放平台 HTTP 客户端（集成全局三级令牌桶限流、熔断保护与防 405 重定向策略）。
package cloud115

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OpenClient 是 115 开放平台客户端。
type OpenClient struct {
	AppID           string
	HTTP            *http.Client
	AccessToken     string
	RefreshTokenStr string
	executor        *QueueExecutor

	// OnTokenRefreshed 在 access_token 刷新成功后回调（参数为新令牌对），
	// 供上层持久化新令牌使用；nil 安全，且在 tokenMu 释放后调用以避免死锁。
	OnTokenRefreshed func(accessToken, refreshToken string)

	// tokenMu 保护 AccessToken / RefreshTokenStr 的并发读写：业务请求中途
	// access_token 失效时自动刷新重试，多 goroutine（同步列表 + 下载队列）
	// 并发下只允许一次刷新进行。
	tokenMu sync.RWMutex
}

// default115HTTPClient 创建带有防 405 重定向保护的 http.Client。
func default115HTTPClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		// 防止 Go 标准库在遇到 301/302/307 重定向时将 POST 降级为 GET 导致 115 报 405 Method Not Allowed
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			// 如果原请求是 POST，重定向时不允许静默转为 GET（直接终止自动重定向，由业务层处理响应）
			if len(via) > 0 && via[len(via)-1].Method == http.MethodPost {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// NewOpenClient 构造客户端。
func NewOpenClient(appID, accessToken, refreshToken string) *OpenClient {
	return &OpenClient{
		AppID:           appID,
		HTTP:            default115HTTPClient(),
		AccessToken:     accessToken,
		RefreshTokenStr: refreshToken,
		executor:        GetGlobalExecutor(),
	}
}

// SetAuthToken 更新认证令牌（并发安全）。
func (c *OpenClient) SetAuthToken(accessToken, refreshToken string) {
	c.tokenMu.Lock()
	c.setAuthTokenLocked(accessToken, refreshToken)
	c.tokenMu.Unlock()
}

// setAuthTokenLocked 无锁更新令牌，调用方必须已持有 tokenMu 写锁
// （tryRefreshTokenLocked 等已持锁流程内部使用，避免重入死锁）。
func (c *OpenClient) setAuthTokenLocked(accessToken, refreshToken string) {
	c.AccessToken = accessToken
	c.RefreshTokenStr = refreshToken
}

// currentAccessToken 返回当前 access_token（并发安全）。
func (c *OpenClient) currentAccessToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.AccessToken
}

// currentRefreshToken 返回当前 refresh_token（并发安全）。
func (c *OpenClient) currentRefreshToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.RefreshTokenStr
}

// CurrentAccessToken 返回当前 access_token 快照（并发安全），
// 供上层在无锁环境下安全读取（如 Ping 时探测令牌是否存在）。
func (c *OpenClient) CurrentAccessToken() string {
	return c.currentAccessToken()
}

// RespState 兼容 115 不同端点返回的 state 类型（proapi 返回布尔、passport 返回数字）。
type RespState bool

func (s *RespState) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case "true", "1":
		*s = true
		return nil
	case "false", "0", "null", "":
		*s = false
		return nil
	}
	var n float64
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("115: 无法解析 state 字段 %s", string(data))
	}
	*s = n != 0
	return nil
}

// RespBase 是 115 开放平台统一响应外壳。
type RespBase struct {
	State   RespState       `json:"state"`
	Code    int             `json:"code"`
	Errno   int             `json:"errno"`
	Message string          `json:"message"`
	Error   string          `json:"error"`
	Count   int64           `json:"count"`
	Data    json.RawMessage `json:"data"`
	Raw     json.RawMessage `json:"-"` // 原始响应体（外层附加字段用）
}

// doJSON 执行 HTTP 请求并解析为统一响应；带 AccessToken（access=true 时）。
func (c *OpenClient) doJSON(ctx context.Context, method, rawURL string, form map[string]string, access bool, retries int, uas ...string) (*RespBase, error) {
	executor := c.executor
	if executor == nil {
		executor = GetGlobalExecutor()
	}
	ua := ""
	if len(uas) > 0 {
		ua = uas[0]
	}

	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		// 1. 获取全局三级令牌桶（QPS/QPM/QPH）令牌，若在熔断状态则阻塞等待冷却
		if err := executor.Acquire(ctx); err != nil {
			return nil, err
		}

		req, err := c.buildRequestWithUA(ctx, method, rawURL, form, access, ua)
		if err != nil {
			return nil, err
		}
		attemptedAccess := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			if attempt < retries {
				time.Sleep(time.Duration(attempt+1) * 1 * time.Second)
				continue
			}
			return nil, err
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		// 检查 HTTP 状态码：特别处理 405/406/429 等限流和异常阻断
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotAcceptable || resp.StatusCode == http.StatusTooManyRequests {
				// 触发全局熔断冷却，防止短时间连续重试导致 IP 被完全拉黑
				executor.MarkThrottled()
				lastErr = fmt.Errorf("115 接口触发频控/安全拦截（HTTP %d）：%s", resp.StatusCode, strings.TrimSpace(string(body)))
				if attempt < retries {
					// 阻塞等待 115 限流冷却解封后自动重试当前请求
					if waitErr := executor.throttleManager.WaitThrottleRecovery(ctx); waitErr != nil {
						return nil, waitErr
					}
					continue
				}
				return nil, lastErr
			}
			lastErr = fmt.Errorf("115 接口返回 HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(body)))
			if attempt < retries {
				time.Sleep(time.Duration(attempt+1) * 1 * time.Second)
				continue
			}
			return nil, lastErr
		}

		var base RespBase
		if err := json.Unmarshal(body, &base); err != nil {
			return nil, fmt.Errorf("115 接口响应解析失败：%w", err)
		}
		base.Raw = body
		if base.State {
			return &base, nil
		}

		// 业务返回限流错误码（406 / 770004）：标记全局熔断，等待冷却后重试
		if IsThrottleCode(base.Code) {
			executor.MarkThrottled()
			lastErr = NewOpenAPIResponseError(base.Code, base.Errno, base.Message, base.Error, "115 访问频率达到上限，已进入冷却")
			if attempt < retries {
				// 阻塞等待限流冷却解封后自动重试当前请求
				if waitErr := executor.throttleManager.WaitThrottleRecovery(ctx); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return &base, lastErr
		}

		// Token 失效（access_token 过期，如长时间同步中途过期）：自动用
		// refresh_token 刷新后重试一次。刷新失败或重试后仍失败才返回，
		// 避免长时间同步因 token 过期而整体失败。
		if isTokenCode(base.Code) {
			if access && c.tryRefreshTokenLocked(ctx, attemptedAccess) {
				continue
			}
			if access {
				// 刷新失败（或已刷新仍失败）时返回明确错误
				lastErr = NewOpenAPIResponseError(base.Code, base.Errno, base.Message, base.Error, "115: access_token 校验失败且刷新未成功")
			} else {
				// 未携带令牌的请求（登录/刷新流程）命中 token 类错误码：
				// 必须返回显式 error，避免调用方把 (resp, nil) 当作成功处理
				msg := base.Message
				if msg == "" {
					msg = base.Error
				}
				lastErr = fmt.Errorf("115: 认证失败(code=%d): %s", base.Code, msg)
			}
			return &base, lastErr
		}

		lastErr = NewOpenAPIResponseError(base.Code, base.Errno, base.Message, base.Error, "115 接口调用失败")
		if attempt < retries {
			time.Sleep(time.Duration(attempt+1) * 1 * time.Second)
			continue
		}
		return &base, lastErr
	}
	return nil, lastErr
}

func (c *OpenClient) buildRequest(ctx context.Context, method, rawURL string, form map[string]string, access bool) (*http.Request, error) {
	return c.buildRequestWithUA(ctx, method, rawURL, form, access, "")
}

func (c *OpenClient) buildRequestWithUA(ctx context.Context, method, rawURL string, form map[string]string, access bool, ua string) (*http.Request, error) {
	method = strings.ToUpper(method)
	var body io.Reader
	if method == http.MethodPost && len(form) > 0 {
		values := url.Values{}
		for k, v := range form {
			values.Set(k, v)
		}
		body = bytes.NewBufferString(values.Encode())
	} else if method == http.MethodGet && len(form) > 0 {
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		query := u.Query()
		for k, v := range form {
			query.Set(k, v)
		}
		u.RawQuery = query.Encode()
		rawURL = u.String()
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	targetUA := DefaultUA
	if strings.TrimSpace(ua) != "" {
		targetUA = strings.TrimSpace(ua)
	}
	req.Header.Set("User-Agent", targetUA)
	if method == http.MethodPost && len(form) > 0 {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if access {
		// RLock 读取令牌，避免与刷新流程的写入产生数据竞争
		if accessToken := c.currentAccessToken(); accessToken != "" {
			req.Header.Set("Authorization", "Bearer "+accessToken)
		}
	}
	return req, nil
}

// doAuthJSON 带 AccessToken 的业务请求。
func (c *OpenClient) doAuthJSON(ctx context.Context, method, rawURL string, form map[string]string, retries int) (*RespBase, error) {
	return c.doJSON(ctx, method, rawURL, form, true, retries)
}

// doAuthJSONWithUA 带自定义 User-Agent 的业务请求（换取直链等防盗链接口使用）。
func (c *OpenClient) doAuthJSONWithUA(ctx context.Context, method, rawURL string, form map[string]string, retries int, ua string) (*RespBase, error) {
	return c.doJSON(ctx, method, rawURL, form, true, retries, ua)
}

// tryRefreshTokenLocked 并发安全地刷新 access_token；成功返回 true（调用方
// 应使用内存中的新 token 重试原请求）。
//
// failedAccess 是失败请求实际携带的 token。拿到写锁后与当前 token 对比：
// 若已被其他 goroutine 刷新过则直接复用，避免并发请求连环轮转消耗 115
// 的一次性 refresh_token。全程持写锁读写 token 字段，无 TOCTOU 窗口。
//
// 对"refresh_token 本身已失效/被吊销"（IsRefreshTokenDead，如 40140114/116/119/120）
// 这类不可恢复的错误直接放弃并清空内存 token（提示需重新授权）。
// 对其它失败（网络瞬时抖动、刷新接口可重试错误码等）做指数退避重试几次再放弃，
// 避免同步长任务中途 token 到期时恰好撞上一个短暂的刷新失败就整体失败。
func (c *OpenClient) tryRefreshTokenLocked(ctx context.Context, failedAccess string) bool {
	c.tokenMu.Lock()
	// 请求发出后若其他 goroutine 已经刷新完成，直接复用新 token 重试；
	// 不能再次轮换一次性的 refresh_token。
	if failedAccess != "" && c.AccessToken != failedAccess {
		c.tokenMu.Unlock()
		return true
	}
	newToken, ok := c.refreshTokenWhileLocked(ctx, failedAccess)
	c.tokenMu.Unlock()
	// 回调必须在 tokenMu 释放后调用，避免上层在回调内访问客户端时死锁
	if ok && newToken != nil && c.OnTokenRefreshed != nil {
		c.OnTokenRefreshed(newToken.AccessToken, newToken.RefreshToken)
	}
	return ok
}

// refreshTokenWhileLocked 在已持有 tokenMu 写锁的前提下执行刷新。
// 返回 (新令牌, 是否成功)；命中"他人已刷新"捷径时新令牌为 nil。
func (c *OpenClient) refreshTokenWhileLocked(ctx context.Context, oldAccess string) (*TokenData, bool) {
	if c.AccessToken != oldAccess {
		// 其他 goroutine 刚刷新过：直接复用内存中的新 token 重试原请求
		return nil, true
	}
	refreshToken := c.RefreshTokenStr
	for attempt := 0; attempt < refreshAttempts; attempt++ {
		token, err := c.doRefreshToken(refreshToken)
		if err == nil {
			c.setAuthTokenLocked(token.AccessToken, token.RefreshToken)
			return token, true
		}
		if IsRefreshTokenDead(err) {
			c.setAuthTokenLocked("", "")
			return nil, false
		}
		// 可恢复失败：退避后重试。ctx 取消时立即放弃。
		if attempt < refreshAttempts-1 {
			select {
			case <-ctx.Done():
				return nil, false
			case <-time.After(refreshBackoff(attempt)):
			}
		}
	}
	return nil, false
}

// refreshAttempts 是刷新 access_token 失败时的最大尝试次数（含首次）。
const refreshAttempts = 3

// refreshBackoff 返回第 attempt 次（从 0 计）刷新失败后的退避时长（指数退避）。
func refreshBackoff(attempt int) time.Duration {
	return time.Duration(200*(1<<attempt)) * time.Millisecond // 200ms, 400ms
}

// IsThrottleCode 判断是否为限流错误码。
func IsThrottleCode(code int) bool {
	return code == RequestMaxLimitCode || code == RequestRateLimitCode
}

func isTokenCode(code int) bool {
	switch code {
	case AccessTokenAuthFail, AccessAuthInvalid, AccessTokenExpiryCode, AccessTokenFormatInvalid, RefreshTokenInvalid:
		return true
	}
	return false
}

// openList 解析 data 为对象或数组（StructOrArray 语义）。
// 115 部分接口在鉴权/业务异常时会返回 data:null 或 data:{}，此时若直接
// 反序列化会得到零值元素 + nil error，调用方会把空数据当成功处理；
// 这里对 null/空对象显式报错。
func openList[T any](raw json.RawMessage) ([]T, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) {
		return nil, fmt.Errorf("115: data 为空（%s）", string(trimmed))
	}
	var single T
	if err := json.Unmarshal(raw, &single); err == nil {
		return []T{single}, nil
	}
	var arr []T
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	return nil, fmt.Errorf("115: data 既不是对象也不是数组")
}

// openFirstList 取 data 的第一个元素；data 为空（null/空数组）时返回显式错误，
// 避免调用方拿到 (nil, nil) 后解引用空指针。
func openFirstList[T any](raw json.RawMessage) (*T, error) {
	items, err := openList[T](raw)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("115: data 为空数组")
	}
	return &items[0], nil
}

func firstOrEmpty(m map[string]downloadURLData) downloadURLData {
	for _, v := range m {
		return v
	}
	return downloadURLData{}
}
