package cloud115

import (
	"fmt"
	"testing"
	"time"
)

func TestDownloadURLCache(t *testing.T) {
	urlCacheMu.Lock()
	urlCache = map[string]urlCacheEntry{}
	urlCacheMu.Unlock()

	if got := GetDownloadURLCache("pick1"); got != "" {
		t.Fatalf("empty cache should return empty, got %q", got)
	}
	SetDownloadURLCache("pick1", "http://cdn/1")
	if got := GetDownloadURLCache("pick1"); got != "http://cdn/1" {
		t.Fatalf("cache miss, got %q", got)
	}
	// 过期条目按未命中处理
	urlCacheMu.Lock()
	urlCache["pick1"] = urlCacheEntry{url: "http://cdn/old", expiresAt: time.Now().Add(-time.Second)}
	urlCacheMu.Unlock()
	if got := GetDownloadURLCache("pick1"); got != "" {
		t.Fatalf("expired entry should be a miss, got %q", got)
	}
	ClearDownloadURLCache("pick1")
	if got := GetDownloadURLCache("pick1"); got != "" {
		t.Fatalf("cleared entry should be a miss, got %q", got)
	}
}

// 115 直链自带 `t=` 过期时间戳（实测为换取时刻 + 1800 秒）。缓存必须跟随它，
// 否则会出现「服务端认为还有效、CDN 已经 403」的窗口：直链被缓存到 45 分钟，
// 但第 30 分钟起客户端就必然拿到 403。
func TestDownloadURLCacheExpiryFollowsLinkTimestamp(t *testing.T) {
	now := time.Now()
	link := fmt.Sprintf("https://cdn/1/a.mkv?t=%d&u=1&k=abc", now.Add(30*time.Minute).Unix())

	got := cacheExpiry(link, now)
	want := now.Add(30*time.Minute - linkExpirySafety)
	if diff := got.Sub(want); diff > time.Second || diff < -time.Second {
		t.Fatalf("cacheExpiry = %v, want %v", got, want)
	}

	// 解析不出 t= 时退回兜底 TTL，保持旧行为。
	fallback := cacheExpiry("https://cdn/1/a.mkv", now)
	if diff := fallback.Sub(now.Add(downloadURLCacheMaxTTL)); diff > time.Second || diff < -time.Second {
		t.Fatalf("fallback cacheExpiry = %v, want %v", fallback, now.Add(downloadURLCacheMaxTTL))
	}

	// 直链本身已经过期（或只剩不足提前量的寿命）时不进缓存。
	if got := cacheExpiry(fmt.Sprintf("https://cdn/1/a.mkv?t=%d", now.Add(-time.Minute).Unix()), now); !got.IsZero() {
		t.Fatalf("expired link must not be cached, got expiry %v", got)
	}
	if got := cacheExpiry(fmt.Sprintf("https://cdn/1/a.mkv?t=%d", now.Add(time.Minute).Unix()), now); !got.IsZero() {
		t.Fatalf("link expiring inside the safety margin must not be cached, got %v", got)
	}
}

// 客户端上报「直链被 CDN 拒绝」时按 pickcode 清缓存，但要挡住刚写入的条目，
// 避免重试风暴把 115 换链接口打出突发。
func TestClearDownloadURLCacheIfOlderThan(t *testing.T) {
	urlCacheMu.Lock()
	urlCache = map[string]urlCacheEntry{}
	urlCacheMu.Unlock()
	t.Cleanup(func() {
		urlCacheMu.Lock()
		urlCache = map[string]urlCacheEntry{}
		urlCacheMu.Unlock()
	})

	future := time.Now().Add(30 * time.Minute)
	link := fmt.Sprintf("https://cdn/1/a.mkv?t=%d", future.Unix())
	SetDownloadURLCache("pick-fresh", link, "ua-a")
	if ClearDownloadURLCacheIfOlderThan("pick-fresh", time.Minute) {
		t.Fatal("fresh entry must survive the min-age guard")
	}
	if got := GetDownloadURLCache("pick-fresh", "ua-a"); got != link {
		t.Fatalf("fresh entry lost, got %q", got)
	}

	SetDownloadURLCache("pick-old", link, "ua-a")
	urlCacheMu.Lock()
	entry := urlCache[urlCacheKey("pick-old", "ua-a")]
	entry.createdAt = time.Now().Add(-10 * time.Minute)
	urlCache[urlCacheKey("pick-old", "ua-a")] = entry
	urlCacheMu.Unlock()

	if !ClearDownloadURLCacheIfOlderThan("pick-old", time.Minute) {
		t.Fatal("stale entry should be cleared")
	}
	if got := GetDownloadURLCache("pick-old", "ua-a"); got != "" {
		t.Fatalf("stale entry should be gone, got %q", got)
	}
	// 无关 pickcode 不受影响。
	if got := GetDownloadURLCache("pick-fresh", "ua-a"); got != link {
		t.Fatalf("unrelated pickcode lost, got %q", got)
	}
}
