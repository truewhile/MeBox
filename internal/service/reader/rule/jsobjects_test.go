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

// cache 必须按书源隔离：旧实现是包级全局 map，任一源的 put/get 对所有源可见，
// 多个源用同一个 key 会互相串值，一个源写满还会清掉别的源的缓存。
func TestCacheIsolatedPerSource(t *testing.T) {
	srcA := map[string]any{"bookSourceUrl": "https://a.example.com"}
	srcB := map[string]any{"bookSourceUrl": "https://b.example.com"}
	ar := NewAnalyzeRule()

	runnerA := NewJSRunner(JSConfig{SourceProps: srcA})
	if _, err := runnerA.Run(ar, `cache.put('shared-key', 'from-a')`, nil, ""); err != nil {
		t.Fatalf("源 A 写入失败: %v", err)
	}
	runnerB := NewJSRunner(JSConfig{SourceProps: srcB})
	v, err := runnerB.Run(ar, `String(cache.get('shared-key'))`, nil, "")
	if err != nil {
		t.Fatalf("源 B 读取失败: %v", err)
	}
	if got := anyToString(v); got != "null" {
		t.Fatalf("源 B 读到了源 A 的缓存: %q", got)
	}
	// 源 A 自己重开运行时仍应读到（落到 CacheDir 之外的进程内持久层）。
	runnerA2 := NewJSRunner(JSConfig{SourceProps: srcA})
	v, err = runnerA2.Run(ar, `String(cache.get('shared-key'))`, nil, "")
	if err != nil {
		t.Fatalf("源 A 二次读取失败: %v", err)
	}
	if got := anyToString(v); got != "from-a" {
		t.Fatalf("源 A 应读到自己的缓存: %q", got)
	}
	// 内存缓存同样按源隔离。
	if _, err := runnerA.Run(ar, `cache.putMemory('mem-key', 'a')`, nil, ""); err != nil {
		t.Fatal(err)
	}
	v, err = runnerB.Run(ar, `String(cache.getFromMemory('mem-key'))`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := anyToString(v); got != "null" {
		t.Fatalf("内存缓存跨源串值: %q", got)
	}
}
