package service

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Eviction must be deterministic when entries share a lastUsed timestamp.
//
// Regression: recency was compared by lastUsed alone. Entries written within the
// same clock tick carry identical timestamps and Go map iteration order is
// randomized, so eviction could drop a just-written entry instead of the oldest
// one — which made TestRuntimeCacheSetMaxSizeEvictsImmediately fail intermittently
// in a full-package run.
func TestRuntimeCacheEvictionBreaksTimestampTiesDeterministically(t *testing.T) {
	// Repeat because the bug only showed up for some map iteration orders.
	for attempt := 0; attempt < 50; attempt++ {
		cache := newRuntimeCacheForTest(t, 128)
		const perEntry = 700 << 10

		// Two entries large enough that a 1MiB budget can hold only one. Writing
		// them back to back puts them in the same clock tick often enough to hit
		// the tie almost immediately.
		cache.SetObject("old", strings.Repeat("a", perEntry), time.Minute)
		cache.SetObject("new", strings.Repeat("b", perEntry), time.Minute)

		cache.SetMaxSizeMB(1)

		if _, ok := cache.GetObject("old"); ok {
			t.Fatalf("attempt %d: oldest entry survived the budget drop", attempt)
		}
		if _, ok := cache.GetObject("new"); !ok {
			t.Fatalf("attempt %d: newest entry was evicted instead of the oldest", attempt)
		}
	}
}

// Accessing an entry refreshes its recency even when it lands in the same tick as
// the write of a competing entry.
func TestRuntimeCacheAccessRefreshesEvictionOrder(t *testing.T) {
	cache := newRuntimeCacheForTest(t, 128)
	const perEntry = 700 << 10

	cache.SetObject("first", strings.Repeat("a", perEntry), time.Minute)
	cache.SetObject("second", strings.Repeat("b", perEntry), time.Minute)
	// Touch the older entry so it becomes the more recent one.
	if _, ok := cache.GetObject("first"); !ok {
		t.Fatal("first entry should be present before the budget drop")
	}

	cache.SetMaxSizeMB(1)

	if _, ok := cache.GetObject("second"); ok {
		t.Fatal("the untouched entry should have been evicted first")
	}
	if _, ok := cache.GetObject("first"); !ok {
		t.Fatal("the just-accessed entry should have been kept")
	}
}

// Eviction picks the oldest across the byte cache and the object cache together.
func TestRuntimeCacheEvictionComparesBothCaches(t *testing.T) {
	cache := newRuntimeCacheForTest(t, 128)
	const perEntry = 700 << 10

	// Byte-cache entry written first, object-cache entry second.
	payload := strings.Repeat("c", perEntry)
	cache.SetJSON(context.Background(), "bytes", payload, time.Minute)
	cache.SetObject("object", strings.Repeat("d", perEntry), time.Minute)

	cache.SetMaxSizeMB(1)

	if _, ok := cache.GetObject("object"); !ok {
		t.Fatal("the newer object entry must be kept")
	}
	var decoded string
	if cache.GetJSON(context.Background(), "bytes", &decoded) {
		t.Fatal("the older byte-cache entry should have been evicted")
	}
}

// The sequence counter must hand out strictly increasing values, otherwise ties
// would resolve randomly again.
func TestRuntimeCacheSequenceIsMonotonic(t *testing.T) {
	cache := newRuntimeCacheForTest(t, 16)
	seen := map[uint64]bool{}
	cache.mu.Lock()
	var previous uint64
	for i := 0; i < 100; i++ {
		seq := cache.nextSeqLocked()
		if seen[seq] {
			cache.mu.Unlock()
			t.Fatalf("sequence %d handed out twice", seq)
		}
		if seq <= previous && previous != 0 {
			cache.mu.Unlock()
			t.Fatalf("sequence went backwards: %d after %d", seq, previous)
		}
		seen[seq] = true
		previous = seq
	}
	cache.mu.Unlock()
	if len(seen) != 100 {
		t.Fatalf("expected 100 distinct sequence numbers, got %d", len(seen))
	}
}
