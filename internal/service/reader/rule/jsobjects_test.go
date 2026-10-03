package rule

import "testing"

// TestCacheMemoryRoundTrip 内存缓存（cache.putMemory / cache.getFromMemory）要能读写。
//
// 回归：「cache 对象只有 put/get/delete」曾让光遇聚合的正文规则整条失败——
// paraForAndroid 每一段带段评的文字都会调 cache.putMemory(url, 0)，缺了它就抛
// TypeError: Object has no member 'putMemory'，正文接口直接 400。
func TestCacheMemoryRoundTrip(t *testing.T) {
	r := NewJSRunner(JSConfig{})
	ar := NewAnalyzeRule()

	if _, err := r.Run(ar, `cache.putMemory('__test_mem', 3)`, nil, ""); err != nil {
		t.Fatalf("cache.putMemory 失败: %v", err)
	}
	v, err := r.Run(ar, `String(cache.getFromMemory('__test_mem'))`, nil, "")
	if err != nil {
		t.Fatalf("cache.getFromMemory 失败: %v", err)
	}
	if got := anyToString(v); got != "3" {
		t.Fatalf("getFromMemory = %q，期望 3", got)
	}

	// 缺省返回 null（书源会写 `cache.getFromMemory(k) || ''` 这类兜底）。
	v, err = r.Run(ar, `String(cache.getFromMemory('__test_mem_missing'))`, nil, "")
	if err != nil {
		t.Fatalf("cache.getFromMemory 失败: %v", err)
	}
	if got := anyToString(v); got != "null" {
		t.Fatalf("缺失键 = %q，期望 null", got)
	}

	// 持久缓存与内存缓存是两套存储（legado 的 put/get 走 ACache，putMemory 走内存），
	// 互不串门，否则书源会读到本该过期的值。
	if _, err := r.Run(ar, `cache.put('__test_persist', 'p')`, nil, ""); err != nil {
		t.Fatalf("cache.put 失败: %v", err)
	}
	v, err = r.Run(ar, `String(cache.getFromMemory('__test_persist'))`, nil, "")
	if err != nil {
		t.Fatalf("cache.getFromMemory 失败: %v", err)
	}
	if got := anyToString(v); got != "null" {
		t.Fatalf("getFromMemory 读到了持久缓存的值: %q", got)
	}
	v, err = r.Run(ar, `String(cache.get('__test_mem'))`, nil, "")
	if err != nil {
		t.Fatalf("cache.get 失败: %v", err)
	}
	if got := anyToString(v); got != "null" {
		t.Fatalf("cache.get 读到了内存缓存的值: %q", got)
	}
}
