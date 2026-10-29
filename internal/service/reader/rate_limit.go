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
	// implicitRetryAttempts 书源没声明 retry 时，GET 请求在可重试状态码上的默认尝试次数。
	// 实测这类聚合源的单次 429 概率接近一半，两次尝试仍有约两成失败，三次降到一成左右；
	// 再往上加只会让被限流的接口更难恢复。退避总共不到 1 秒，详情/目录请求扛得住。
	implicitRetryAttempts = 3
	// requestRetryDelay 请求重试退避基数，实际等待为 attempt 倍。
	requestRetryDelay = 300 * time.Millisecond
)

// retryableStatus 该状态码是否值得重试：限流（403/429）与上游抖动（5xx）可重试，
// 404/400 这类确定性错误不重试。规则请求与媒体代理共用。
//
// 注意 401 不在这里：书源的规则请求返回 401 通常是「登录态失效」，反复重试
// 只会白等退避时间。媒体直链另有语义，见 mediaRetryableStatus。
func retryableStatus(code int) bool {
	switch code {
	case 403, 408, 425, 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

// implicitRetryableStatus 书源没声明 retry 时，值得自动补一次的失败。
//
// 只覆盖「上游临时顶不住」的情形：429 是限流/并发上限，502/503/504 是网关或
// 实例暂时不可用，歇一轮基本会恢复。403（风控）重试同一份请求多半还是 403，
// 500 可能是源站自身 bug（确定性失败），都不在这里浪费一次往返。
func implicitRetryableStatus(code int) bool {
	switch code {
	case 429, 502, 503, 504:
		return true
	default:
		return false
	}
}

// mediaRetryableStatus 媒体拉取专用的可重试判定。
//
// 与规则请求不同，CDN 直链的 410 表示「对象已下线」，与 403/429 一样属于
// 上游抖动，同一张票稍后重试可能就恢复；而 401 是「签名票被拒」，重试同一张票
// 一定还是 401，必须由调用方回源换票（见 MediaNeedsRefresh）。
// 所以这里只补 410，不把 401 加进来。
func mediaRetryableStatus(code int) bool {
	if retryableStatus(code) {
		return true
	}
	return code == 410
}

// MediaNeedsRefresh 判断媒体上游状态是否值得「换一张签名票再试」。
//
// 401 是 CDN 对无效 vkey/uid 的固定回答（空 body、无 Content-Type），410 是
// 对象已下线，403 常见于票与资源不匹配。这三类都值得回到书源重新签票。
func MediaNeedsRefresh(code int) bool {
	switch code {
	case 401, 403, 410:
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
