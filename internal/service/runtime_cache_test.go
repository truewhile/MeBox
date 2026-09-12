package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
)

func newRuntimeCacheForTest(t *testing.T, maxMB int) *RuntimeCacheService {
	t.Helper()
	cfg := &config.Config{}
	cfg.Cache.MemoryMaxSizeMB = maxMB
	return NewRuntimeCacheService(cfg, zap.NewNop())
}

func TestRuntimeCacheEvictsByByteBudget(t *testing.T) {
	cache := newRuntimeCacheForTest(t, 1)
	cache.SetObject("large", strings.Repeat("a", 800<<10), time.Minute)
	cache.SetObject("small", strings.Repeat("b", 300<<10), time.Minute)

	if cache.bytesUsed > cache.maxBytes {
		t.Fatalf("cache bytes=%d exceeds max=%d", cache.bytesUsed, cache.maxBytes)
	}
	if _, ok := cache.GetObject("large"); ok {
		t.Fatal("expected oldest large entry to be evicted")
	}
	if _, ok := cache.GetObject("small"); !ok {
		t.Fatal("expected newest small entry to remain cached")
	}
}

func TestRuntimeCacheSkipsOversizedObject(t *testing.T) {
	cache := newRuntimeCacheForTest(t, 1)
	cache.SetObject("huge", strings.Repeat("x", 2<<20), time.Minute)

	if _, ok := cache.GetObject("huge"); ok {
		t.Fatal("oversized object must not be cached")
	}
	if cache.bytesUsed != 0 {
		t.Fatalf("bytesUsed=%d, want 0", cache.bytesUsed)
	}
}

func TestRuntimeCachePrefixDeleteReleasesByteBudget(t *testing.T) {
	cache := newRuntimeCacheForTest(t, 2)
	for i := 0; i < 4; i++ {
		cache.SetJSON(context.Background(), fmt.Sprintf("media:%d", i), strings.Repeat("x", 64<<10), time.Minute)
	}
	if cache.bytesUsed == 0 {
		t.Fatal("expected cached bytes")
	}
	cache.DeletePrefix(context.Background(), "media:")
	if cache.bytesUsed != 0 {
		t.Fatalf("bytesUsed=%d after prefix delete, want 0", cache.bytesUsed)
	}
	for i := 0; i < 4; i++ {
		var out string
		if cache.GetJSON(context.Background(), fmt.Sprintf("media:%d", i), &out) {
			t.Fatalf("entry media:%d should have been deleted", i)
		}
	}
}

func TestRuntimeCacheStillEnforcesEntryLimit(t *testing.T) {
	cache := newRuntimeCacheForTest(t, 1)
	cache.limit = 3
	for i := 0; i < 5; i++ {
		cache.SetObject(fmt.Sprintf("item-%d", i), i, time.Minute)
	}
	if got := cache.entryCountLocked(); got != 3 {
		t.Fatalf("entry count=%d, want 3", got)
	}
}

func TestRuntimeCacheEstimatesMediaRows(t *testing.T) {
	rows := []model.Media{{
		Title:       strings.Repeat("t", 1024),
		Path:        "/media/movies/example.mkv",
		PosterURL:   "https://image.example/poster.jpg",
		BackdropURL: "https://image.example/backdrop.jpg",
		Overview:    strings.Repeat("o", 2048),
		Genres:      "Action,Adventure",
	}}
	size := estimateRuntimeCacheObjectSize("media:test", rows)
	if size < int64(len(rows[0].Title)+len(rows[0].Overview)) {
		t.Fatalf("estimated size %d is smaller than payload", size)
	}
}

func TestRuntimeCacheSetMaxSizeEvictsImmediately(t *testing.T) {
	cache := newRuntimeCacheForTest(t, 2)
	cache.SetObject("old", strings.Repeat("a", 700<<10), time.Minute)
	cache.SetObject("new", strings.Repeat("b", 700<<10), time.Minute)
	if cache.bytesUsed <= 1<<20 {
		t.Fatalf("test setup bytesUsed=%d, want >1MiB", cache.bytesUsed)
	}

	cache.SetMaxSizeMB(1)
	if cache.maxBytes != 1<<20 {
		t.Fatalf("maxBytes=%d, want 1MiB", cache.maxBytes)
	}
	if cache.bytesUsed > cache.maxBytes {
		t.Fatalf("bytesUsed=%d exceeds maxBytes=%d", cache.bytesUsed, cache.maxBytes)
	}
	if _, ok := cache.GetObject("old"); ok {
		t.Fatal("oldest entry should be evicted after lowering cache limit")
	}
	if _, ok := cache.GetObject("new"); !ok {
		t.Fatal("newest entry should remain after lowering cache limit")
	}
}
