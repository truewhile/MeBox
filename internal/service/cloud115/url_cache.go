// 下载直链进程级缓存。
//
// 115 的 /open/ufile/downurl 换取结果带 `t=` 过期时间戳（实测为「换取时刻 +
// 1800 秒」，即 30 分钟），且换取请求是 WAF 风控重点关注的密集接口。缓存按
// pickcode 存直链、避免对同一文件反复换取；下载失败（http 403/404 等）时由
// 调用方主动清除对应条目，下一轮重试会重新换取链接。
package cloud115

import (
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// downloadURLCacheMaxTTL 直链缓存上限：仅在解析不出直链自带过期时间（`t=`）
// 时使用。115 直链实际有效 30 分钟，这里取 45 分钟只是兜底上限，正常情况下
// 由 linkExpirySafety 提前失效，避免把已经过期的直链发给客户端。
const downloadURLCacheMaxTTL = 45 * time.Minute

// linkExpirySafety 提前量：直链自身的过期时间戳到达前就停止使用，留出客户端
// 建连与传输的余量，防止出现「服务端认为还有效、CDN 已经 403」的窗口。
const linkExpirySafety = 2 * time.Minute

// maxCachedURLs 超过该数量时顺带清理过期条目，防止 map 无限膨胀。
const maxCachedURLs = 10000

type urlCacheEntry struct {
	url       string
	createdAt time.Time
	expiresAt time.Time
}

var (
	urlCacheMu sync.Mutex
	urlCache   = map[string]urlCacheEntry{}
)

// urlCacheKey 构造缓存键名（pickCode + UA，实现直链防盗链按客户端 UA 独立缓存）。
func urlCacheKey(pickCode, ua string) string {
	if ua == "" {
		return pickCode
	}
	return pickCode + "@" + ua
}

// LinkExpiry 解析 115 直链自带的过期时间戳（`t=` 参数，单位秒）。
// 解析不出时返回零值，调用方按兜底 TTL 处理。
func LinkExpiry(rawURL string) time.Time {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil {
		return time.Time{}
	}
	raw := strings.TrimSpace(u.Query().Get("t"))
	if raw == "" {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// cacheExpiry 计算写入缓存时应使用的过期时刻：跟随直链自身的 `t=` 并提前
// linkExpirySafety，解析不出时退回 downloadURLCacheMaxTTL。返回零值表示这条
// 直链已经过期，不应写入缓存。
func cacheExpiry(rawURL string, now time.Time) time.Time {
	maxExpiry := now.Add(downloadURLCacheMaxTTL)
	linkExpiry := LinkExpiry(rawURL)
	if linkExpiry.IsZero() {
		return maxExpiry
	}
	expiry := linkExpiry.Add(-linkExpirySafety)
	if !expiry.After(now) {
		return time.Time{}
	}
	if expiry.After(maxExpiry) {
		return maxExpiry
	}
	return expiry
}

// GetDownloadURLCache 返回未过期的缓存直链；不存在或已过期返回空串。
func GetDownloadURLCache(pickCode string, uas ...string) string {
	if pickCode == "" {
		return ""
	}
	ua := ""
	if len(uas) > 0 {
		ua = uas[0]
	}
	key := urlCacheKey(pickCode, ua)
	urlCacheMu.Lock()
	defer urlCacheMu.Unlock()
	entry, ok := urlCache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		if ok {
			delete(urlCache, key)
		}
		return ""
	}
	return entry.url
}

// SetDownloadURLCache 写入直链缓存。过期时刻跟随直链自身的 `t=`，已经过期的
// 直链直接丢弃，避免把死链缓存下来。
func SetDownloadURLCache(pickCode string, url string, uas ...string) {
	if pickCode == "" || url == "" {
		return
	}
	now := time.Now()
	expiresAt := cacheExpiry(url, now)
	if expiresAt.IsZero() {
		return
	}
	ua := ""
	if len(uas) > 0 {
		ua = uas[0]
	}
	urlCacheMu.Lock()
	defer urlCacheMu.Unlock()
	if len(urlCache) >= maxCachedURLs {
		for k, e := range urlCache {
			if now.After(e.expiresAt) {
				delete(urlCache, k)
			}
		}
	}
	key := urlCacheKey(pickCode, ua)
	urlCache[key] = urlCacheEntry{url: url, createdAt: now, expiresAt: expiresAt}
}

// ClearDownloadURLCache 删除指定 pickcode 的所有缓存（下载得到非 2xx 时调用）。
func ClearDownloadURLCache(pickCode string) {
	if pickCode == "" {
		return
	}
	urlCacheMu.Lock()
	defer urlCacheMu.Unlock()
	for k := range urlCache {
		if k == pickCode || strings.HasPrefix(k, pickCode+"@") {
			delete(urlCache, k)
		}
	}
}

// ClearDownloadURLCacheIfOlderThan 删除指定 pickcode 下「已经存在超过 minAge」
// 的缓存条目，返回是否删除了至少一条。
//
// 客户端上报「直链被 CDN 拒绝」时用它强制换链：既让下一次解析拿到新地址，
// 又不会让同一秒内的重试风暴把 115 换链接口打出突发。
func ClearDownloadURLCacheIfOlderThan(pickCode string, minAge time.Duration) bool {
	if pickCode == "" {
		return false
	}
	if minAge < 0 {
		minAge = 0
	}
	now := time.Now()
	removed := false
	urlCacheMu.Lock()
	defer urlCacheMu.Unlock()
	for k, e := range urlCache {
		if k != pickCode && !strings.HasPrefix(k, pickCode+"@") {
			continue
		}
		if now.Sub(e.createdAt) < minAge {
			continue
		}
		delete(urlCache, k)
		removed = true
	}
	return removed
}
