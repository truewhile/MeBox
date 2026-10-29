package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPruneImageCache_UnderLimit(t *testing.T) {
	dir := t.TempDir()

	file1 := filepath.Join(dir, "img1")
	file2 := filepath.Join(dir, "img2")
	if err := os.WriteFile(file1, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, make([]byte, 200), 0o600); err != nil {
		t.Fatal(err)
	}

	// Max limit is 500 bytes, total is 300 bytes -> no prune
	res, err := PruneImageCache(dir, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.DeletedFiles != 0 {
		t.Fatalf("expected 0 deleted files, got %d", res.DeletedFiles)
	}
	if res.TotalFilesBefore != 2 || res.TotalBytesBefore != 300 || res.RemainingBytes != 300 {
		t.Fatalf("unexpected stats: %+v", res)
	}
}

func TestPruneImageCache_OverLimitLRU(t *testing.T) {
	dir := t.TempDir()

	now := time.Now()
	// Create 4 files of 100 bytes each, with distinct mtime
	fOldest := filepath.Join(dir, "oldest")
	fMidOld := filepath.Join(dir, "mid_old")
	fMidNew := filepath.Join(dir, "mid_new")
	fNewest := filepath.Join(dir, "newest")

	for _, f := range []string{fOldest, fMidOld, fMidNew, fNewest} {
		if err := os.WriteFile(f, make([]byte, 100), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_ = os.Chtimes(fOldest, now.Add(-4*time.Hour), now.Add(-4*time.Hour))
	_ = os.Chtimes(fMidOld, now.Add(-3*time.Hour), now.Add(-3*time.Hour))
	_ = os.Chtimes(fMidNew, now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	_ = os.Chtimes(fNewest, now.Add(-1*time.Hour), now.Add(-1*time.Hour))

	// Total = 400 bytes. Max limit = 300 bytes.
	// Target = 300 * 80 / 100 = 240 bytes.
	// Deleting oldest (100) brings total to 300 (> 240).
	// Deleting mid_old (100) brings total to 200 (<= 240).
	// Total deleted = 2 files (200 bytes), remaining = 200 bytes.
	res, err := PruneImageCache(dir, 300)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.DeletedFiles != 2 {
		t.Fatalf("expected 2 deleted files, got %d", res.DeletedFiles)
	}
	if res.FreedBytes != 200 {
		t.Fatalf("expected 200 freed bytes, got %d", res.FreedBytes)
	}
	if res.RemainingBytes != 200 {
		t.Fatalf("expected 200 remaining bytes, got %d", res.RemainingBytes)
	}

	// Verify oldest and mid_old were deleted, mid_new and newest still exist
	if _, err := os.Stat(fOldest); !os.IsNotExist(err) {
		t.Fatalf("expected oldest file to be deleted, got err=%v", err)
	}
	if _, err := os.Stat(fMidOld); !os.IsNotExist(err) {
		t.Fatalf("expected mid_old file to be deleted, got err=%v", err)
	}
	if _, err := os.Stat(fMidNew); err != nil {
		t.Fatalf("expected mid_new file to exist, got err=%v", err)
	}
	if _, err := os.Stat(fNewest); err != nil {
		t.Fatalf("expected newest file to exist, got err=%v", err)
	}
}

func TestPruneImageCache_SkipsTmpFiles(t *testing.T) {
	dir := t.TempDir()

	fTmp := filepath.Join(dir, "img-123.tmp")
	fImg := filepath.Join(dir, "cached_img")

	if err := os.WriteFile(fTmp, make([]byte, 500), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fImg, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}

	// Limit is 200 bytes. fTmp (500) is ignored, only fImg (100) is counted <= 200.
	res, err := PruneImageCache(dir, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.DeletedFiles != 0 {
		t.Fatalf("expected 0 deleted files, got %d", res.DeletedFiles)
	}
	if _, err := os.Stat(fTmp); err != nil {
		t.Fatalf("expected tmp file to remain untouched, got %v", err)
	}
}

func TestPruneImageCache_ZeroOrNegativeLimit(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "img")
	if err := os.WriteFile(f, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := PruneImageCache(dir, 0)
	if err != nil || res.DeletedFiles != 0 {
		t.Fatalf("expected no-op for 0 limit, got %+v, err=%v", res, err)
	}

	res, err = PruneImageCache(dir, -10)
	if err != nil || res.DeletedFiles != 0 {
		t.Fatalf("expected no-op for negative limit, got %+v, err=%v", res, err)
	}
}

func TestSchedulerJobCleanCache(t *testing.T) {
	cacheRoot := t.TempDir()
	imagesDir := filepath.Join(cacheRoot, "images")
	if err := os.MkdirAll(imagesDir, 0o750); err != nil {
		t.Fatal(err)
	}

	f := filepath.Join(imagesDir, "old_poster")
	if err := os.WriteFile(f, make([]byte, 2*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}

	scheduler := NewSchedulerService(zap.NewNop(), nil, nil, nil, nil, nil, cacheRoot)
	// Set limit to 1MB; our file is 2MB -> should be pruned
	scheduler.SetCacheCleanupPolicyProvider(func() CacheCleanupPolicy {
		return CacheCleanupPolicy{TotalBytes: 1 * 1024 * 1024}
	})

	if err := scheduler.jobCleanCache(context.Background()); err != nil {
		t.Fatalf("jobCleanCache failed: %v", err)
	}

	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("expected file to be pruned, got err=%v", err)
	}
}

// TestPruneCacheEvictionOrder 验证总量超限时严格按
// hls → images → library-covers 的顺序淘汰，且 reader-audio 不参与。
func TestPruneCacheEvictionOrder(t *testing.T) {
	cacheRoot := t.TempDir()
	now := time.Now()

	audio := filepath.Join(cacheRoot, "reader-audio", "old.mp3")
	hls := filepath.Join(cacheRoot, "hls", "job", "seg.ts")
	image := filepath.Join(cacheRoot, "images", "originals", "poster.jpg")
	cover := filepath.Join(cacheRoot, "library-covers", "cover.jpg")
	for _, p := range []string{audio, hls, image, cover} {
		writePoolFile(t, p, 1024*1024, now.Add(-time.Hour))
	}

	// 计入总量的只有 hls+images+cover = 3MB，上限 2MB -> 回落到 1.6MB：
	// 按顺序删 hls、images 两个文件即可；reader-audio 与封面都应保留。
	res, err := PruneCache(cacheRoot, 2*1024*1024, 0)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	if res.DeletedFiles != 2 {
		t.Fatalf("deleted files = %d, want 2", res.DeletedFiles)
	}
	mustExist(t, audio)
	mustNotExist(t, hls)
	mustNotExist(t, image)
	mustExist(t, cover)
	if _, ok := res.FreedByPool["reader-audio"]; ok {
		t.Fatalf("reader-audio must not be part of the global pool, got %+v", res.FreedByPool)
	}
	if res.FreedByPool["hls"] != 1024*1024 || res.FreedByPool["images"] != 1024*1024 {
		t.Fatalf("unexpected per-pool stats: %+v", res.FreedByPool)
	}
}

// TestPruneCacheLeavesReaderAudioAlone reader-audio 有独立的 1GB 写入时淘汰，
// 不参与总容量统计：即使它单独就超过全局上限，全局任务也不应删它。
func TestPruneCacheLeavesReaderAudioAlone(t *testing.T) {
	cacheRoot := t.TempDir()
	audio := filepath.Join(cacheRoot, "reader-audio", "book", "chapter.mp3")
	writePoolFile(t, audio, 4*1024*1024, time.Now().Add(-time.Hour))

	res, err := PruneCache(cacheRoot, 1*1024*1024, 0)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	if res.DeletedFiles != 0 {
		t.Fatalf("deleted files = %d, want 0", res.DeletedFiles)
	}
	if res.TotalBytesBefore != 0 {
		t.Fatalf("reader-audio should not count toward total, got %d bytes", res.TotalBytesBefore)
	}
	mustExist(t, audio)
}

// TestPruneCacheAppliesOriginalsSubQuota 验证总量没超时，图片原图子配额仍然生效。
func TestPruneCacheAppliesOriginalsSubQuota(t *testing.T) {
	cacheRoot := t.TempDir()
	now := time.Now()

	oldOriginal := filepath.Join(cacheRoot, "images", "originals", "old.jpg")
	newOriginal := filepath.Join(cacheRoot, "images", "originals", "new.jpg")
	derived := currentVariantPath(filepath.Join(cacheRoot, "images", "resize"), "d", 480, 600, 80)
	writePoolFile(t, oldOriginal, 800, now.Add(-2*time.Hour))
	writePoolFile(t, newOriginal, 800, now.Add(-time.Hour))
	writePoolFile(t, derived, 800, now.Add(-time.Hour))

	// 总量上限很大（不触发全局淘汰），原图子配额 1000B -> 删到 800B。
	res, err := PruneCache(cacheRoot, 100*1024*1024, 1000)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	if res.DeletedFiles != 1 {
		t.Fatalf("deleted files = %d, want 1", res.DeletedFiles)
	}
	mustNotExist(t, oldOriginal)
	mustExist(t, newOriginal)
	mustExist(t, derived)
}

// TestPruneCacheOldestFirstWithinPool 验证同一池内先删最旧的文件。
func TestPruneCacheOldestFirstWithinPool(t *testing.T) {
	cacheRoot := t.TempDir()
	now := time.Now()

	oldHLS := filepath.Join(cacheRoot, "hls", "job-a", "seg1.ts")
	newHLS := filepath.Join(cacheRoot, "hls", "job-b", "seg1.ts")
	writePoolFile(t, oldHLS, 1024*1024, now.Add(-2*time.Hour))
	writePoolFile(t, newHLS, 1024*1024, now.Add(-time.Minute))

	// 上限 1.25MB -> 回落水位 1MB：删掉最旧的 1MB 后正好达标。
	res, err := PruneCache(cacheRoot, 1280*1024, 0)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	if res.DeletedFiles != 1 {
		t.Fatalf("deleted files = %d, want 1", res.DeletedFiles)
	}
	mustNotExist(t, oldHLS)
	mustExist(t, newHLS)
}
