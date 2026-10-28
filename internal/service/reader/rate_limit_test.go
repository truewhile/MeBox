package reader

import (
	"testing"
	"time"
)

// TestParseConcurrentRate 书源 concurrentRate 的两种写法。
func TestParseConcurrentRate(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"abc", 0},
		{"1000", 1000 * time.Millisecond},
		{" 500 ", 500 * time.Millisecond},
		{"3/1000", 333 * time.Millisecond},
		{"1/2000", 2000 * time.Millisecond},
		{"3/0", 0},
		{"0/1000", 0},
		{"999999", time.Duration(maxRateGapMs) * time.Millisecond}, // 上限夹紧
	}
	for _, c := range cases {
		if got := parseConcurrentRate(c.in); got != c.want {
			t.Errorf("parseConcurrentRate(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

// TestRateLimiterSpacesRequests 同一个源连续两次请求之间至少间隔一个周期。
func TestRateLimiterSpacesRequests(t *testing.T) {
	l := newSourceRateLimiter()
	start := time.Now()
	l.wait(t.Context(), "src", "120") // 120ms
	l.wait(t.Context(), "src", "120")
	if elapsed := time.Since(start); elapsed < 110*time.Millisecond {
		t.Fatalf("两次请求只隔了 %v，没有按 concurrentRate 限速", elapsed)
	}
	// 不同源互不影响
	start = time.Now()
	l.wait(t.Context(), "other", "500")
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("换源后不该等待，实际 %v", elapsed)
	}
}

// TestRateLimiterIgnoresEmptyRate 未声明 concurrentRate 时不引入任何等待。
func TestRateLimiterIgnoresEmptyRate(t *testing.T) {
	l := newSourceRateLimiter()
	start := time.Now()
	for i := 0; i < 5; i++ {
		l.wait(t.Context(), "src", "")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("未声明限速却等待了 %v", elapsed)
	}
}

// TestRetryableStatus 只有可恢复的失败才重试。
func TestRetryableStatus(t *testing.T) {
	for _, code := range []int{403, 429, 500, 502, 503, 504} {
		if !retryableStatus(code) {
			t.Errorf("状态码 %d 应当可重试", code)
		}
	}
	for _, code := range []int{200, 301, 400, 401, 404, 410} {
		if retryableStatus(code) {
			t.Errorf("状态码 %d 不该重试", code)
		}
	}
}

// TestMediaRetryableStatus 媒体直链的 410（对象已下线）与 403/429 一样值得原地重试，
// 但 401「签名票被拒」原地重试一定是同样的结果，只能靠回源换票。
func TestMediaRetryableStatus(t *testing.T) {
	for _, code := range []int{403, 410, 429, 500, 503} {
		if !mediaRetryableStatus(code) {
			t.Errorf("媒体状态码 %d 应当可重试", code)
		}
	}
	for _, code := range []int{200, 206, 301, 400, 401, 404} {
		if mediaRetryableStatus(code) {
			t.Errorf("媒体状态码 %d 不该原地重试", code)
		}
	}
	// 规则请求的判定不能被放宽：登录态失效的 401 反复重试只会白等。
	if retryableStatus(401) || retryableStatus(410) {
		t.Error("retryableStatus 不应包含 401/410")
	}
}

// TestMediaNeedsRefresh 只有「票被拒」这一类状态才值得回源换票。
func TestMediaNeedsRefresh(t *testing.T) {
	for _, code := range []int{401, 403, 410} {
		if !MediaNeedsRefresh(code) {
			t.Errorf("状态码 %d 应当换票重试", code)
		}
	}
	for _, code := range []int{200, 206, 304, 400, 404, 429, 500} {
		if MediaNeedsRefresh(code) {
			t.Errorf("状态码 %d 不该换票", code)
		}
	}
}
