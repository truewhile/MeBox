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

func TestSchedulerJobCleanImageCache(t *testing.T) {
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
	scheduler.SetImageCachePolicyProvider(func() ImageCachePolicy {
		return ImageCachePolicy{TotalBytes: 1 * 1024 * 1024}
	})

	if err := scheduler.jobCleanImageCache(context.Background()); err != nil {
		t.Fatalf("jobCleanImageCache failed: %v", err)
	}

	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("expected file to be pruned, got err=%v", err)
	}
}
