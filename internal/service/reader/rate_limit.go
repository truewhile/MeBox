package reader

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sourceRateLimiter 单源限速。
//
// 书源用 concurrentRate 声明「请求该源的最小间隔」，风控严格的站点靠它避免被封
// （不实现的话，搜索/目录/正文连着打过去很容易被拦，表现为「有时能用有时不能用」）。
// 对应 legado 的 ConcurrentRateLimiter，支持两种写法：
//
//	"1000"   每次请求至少间隔 1000ms
//	"3/1000" 每 1000ms 最多 3 次（折算成 333ms 间隔）
//
// 实现为「按源排槽位」：同一书源的请求被排成固定间隔，天然串行。
type sourceRateLimiter struct {
	mu    sync.Mutex
	slots map[string]time.Time
}

// maxRateGapMs 允许的最大间隔，避免书源写出离谱的值把请求卡死。
const maxRateGapMs = 30000

func newSourceRateLimiter() *sourceRateLimiter {
	return &sourceRateLimiter{slots: map[string]time.Time{}}
}

// wait 按源限速等待；key 为空或 rate 无法解析时不等待。
func (l *sourceRateLimiter) wait(ctx context.Context, key, rate string) {
	gap := parseConcurrentRate(rate)
	if gap <= 0 || key == "" {
		return
	}
	now := time.Now()
	l.mu.Lock()
	slot := now
	if next, ok := l.slots[key]; ok && next.After(now) {
		slot = next
	}
	l.slots[key] = slot.Add(gap)
	l.mu.Unlock()
	sleepWithContext(ctx, slot.Sub(now))
}

// parseConcurrentRate 解析 concurrentRate，返回两次请求的最小间隔；无法解析返回 0。
func parseConcurrentRate(rate string) time.Duration {
	rate = strings.TrimSpace(rate)
	if rate == "" {
		return 0
	}
	gapMs := 0
	if i := strings.IndexByte(rate, '/'); i > 0 {
		count, errCount := strconv.Atoi(strings.TrimSpace(rate[:i]))
		interval, errInterval := strconv.Atoi(strings.TrimSpace(rate[i+1:]))
		if errCount != nil || errInterval != nil || count <= 0 || interval <= 0 {
			return 0
		}
		gapMs = interval / count
	} else {
		n, err := strconv.Atoi(rate)
		if err != nil || n <= 0 {
			return 0
		}
		gapMs = n
	}
	if gapMs <= 0 {
		return 0
	}
	if gapMs > maxRateGapMs {
		gapMs = maxRateGapMs
	}
	return time.Duration(gapMs) * time.Millisecond
}

const (
	// maxRequestAttempts 单次请求最大尝试次数（含首次），兜住书源里写很大的 retry。
	maxRequestAttempts = 5
	// requestRetryDelay 请求重试退避基数，实际等待为 attempt 倍。
	requestRetryDelay = 300 * time.Millisecond
)

// retryableStatus 该状态码是否值得重试：限流（403/429）与上游抖动（5xx）可重试，
// 404/400 这类确定性错误不重试。规则请求与媒体代理共用。
func retryableStatus(code int) bool {
	switch code {
	case 403, 408, 425, 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

// sleepWithContext 可被 ctx 取消的等待；返回 false 表示上下文已结束。
func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
