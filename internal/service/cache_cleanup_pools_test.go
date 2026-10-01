package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePoolFile ?????????????????????????
func writePoolFile(t *testing.T, path string, size int, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if !modTime.IsZero() {
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be deleted, got err=%v", path, err)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to survive, got err=%v", path, err)
	}
}

// currentVariantPath ????????????????.<??x<??q<??>.img??
// ??????????????????????????
func currentVariantPath(dir, keyChar string, width, height, quality int) string {
	key := strings.Repeat(keyChar, sha256HexLength)
	return filepath.Join(dir, fmt.Sprintf("%s.%dx%dq%d.img", key, width, height, quality))
}

// TestPruneImageCachePoolsOriginalTTL ????????????????????
// ?? TTL ??????????????????????
func TestPruneImageCachePoolsOriginalTTL(t *testing.T) {
	imagesDir := t.TempDir()
	now := time.Now()

	stale := filepath.Join(imagesDir, imageOriginalCacheSubdir, "stale-original")
	fresh := filepath.Join(imagesDir, imageOriginalCacheSubdir, "fresh-original")
	thumb := currentVariantPath(filepath.Join(imagesDir, imageResizeCacheSubdir), "b", 480, 600, 80)
	writePoolFile(t, stale, 400, now.Add(-8*24*time.Hour))
	writePoolFile(t, fresh, 400, now.Add(-1*time.Hour))
	// ??????????TTL ????
	writePoolFile(t, thumb, 400, now.Add(-8*24*time.Hour))

	pools := ImageCachePools(imagesDir, 0, 7*24*time.Hour)
	res, err := PruneImageCachePools(pools, 100*1024*1024)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	if res.DeletedFiles != 1 {
		t.Fatalf("deleted files = %d, want 1", res.DeletedFiles)
	}
	mustNotExist(t, stale)
	mustExist(t, fresh)
	mustExist(t, thumb)
}

// TestPruneImageCachePoolsEvictsOriginalsBeforeDerived ????????????
// ????????????????????
func TestPruneImageCachePoolsEvictsOriginalsBeforeDerived(t *testing.T) {
	imagesDir := t.TempDir()
	now := time.Now()

	original := filepath.Join(imagesDir, imageOriginalCacheSubdir, "original")
	rendition := filepath.Join(imagesDir, imageRenditionCacheSubdir, "rendition")
	thumb := currentVariantPath(filepath.Join(imagesDir, imageResizeCacheSubdir), "c", 480, 600, 80)
	// ?????????????????
	writePoolFile(t, original, 1024*1024, now.Add(-72*time.Hour))
	writePoolFile(t, rendition, 1024*1024, now.Add(-2*time.Hour))
	writePoolFile(t, thumb, 1024*1024, now.Add(-1*time.Hour))

	pools := ImageCachePools(imagesDir, 0, 0)
	// ?? 3MB????2.4MB -> ????? 1.92MB??
	res, err := PruneImageCachePools(pools, 2*1024*1024+400*1024)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	if res.DeletedFiles != 2 {
		t.Fatalf("deleted files = %d, want 2 (original + oldest derived)", res.DeletedFiles)
	}
	mustNotExist(t, original)
	mustNotExist(t, rendition)
	mustExist(t, thumb)
	if res.RemainingBytes != 1024*1024 {
		t.Fatalf("remaining bytes = %d, want %d", res.RemainingBytes, 1024*1024)
	}
}

// TestPruneImageCachePoolsOriginalQuotaKeepsDerived ????????????
// ??????????????
func TestPruneImageCachePoolsOriginalQuotaKeepsDerived(t *testing.T) {
	imagesDir := t.TempDir()
	now := time.Now()

	oldOriginal := filepath.Join(imagesDir, imageOriginalCacheSubdir, "old")
	newOriginal := filepath.Join(imagesDir, imageOriginalCacheSubdir, "new")
	thumb := currentVariantPath(filepath.Join(imagesDir, imageResizeCacheSubdir), "c", 480, 600, 80)
	writePoolFile(t, oldOriginal, 800, now.Add(-3*time.Hour))
	writePoolFile(t, newOriginal, 800, now.Add(-1*time.Hour))
	writePoolFile(t, thumb, 800, now.Add(-2*time.Hour))

	// ???? 1000 ??????????1600 -> ????800????? 1 ????
	pools := ImageCachePools(imagesDir, 1000, 0)
	res, err := PruneImageCachePools(pools, 0)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	if res.DeletedFiles != 1 {
		t.Fatalf("deleted files = %d, want 1", res.DeletedFiles)
	}
	mustNotExist(t, oldOriginal)
	mustExist(t, newOriginal)
	mustExist(t, thumb)
}

// TestPruneImageCachePoolsCleansFailMarkers ????????????????
// ????????????????
func TestPruneImageCachePoolsCleansFailMarkers(t *testing.T) {
	imagesDir := t.TempDir()
	now := time.Now()

	entry := filepath.Join(imagesDir, imageOriginalCacheSubdir, "entry")
	entryMarker := entry + ".fail"
	orphanMarker := filepath.Join(imagesDir, imageOriginalCacheSubdir, "orphan.fail")
	freshMarker := filepath.Join(imagesDir, imageOriginalCacheSubdir, "fresh.fail")

	writePoolFile(t, entry, 900, now.Add(-10*time.Hour))
	writePoolFile(t, entryMarker, 35, now.Add(-10*time.Hour))
	writePoolFile(t, orphanMarker, 35, now.Add(-48*time.Hour))
	writePoolFile(t, freshMarker, 35, now.Add(-1*time.Minute))

	// ???? 500????900 ?????????????????
	pools := ImageCachePools(imagesDir, 500, 0)
	res, err := PruneImageCachePools(pools, 0)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	if res.DeletedFiles != 1 {
		t.Fatalf("deleted files = %d, want 1", res.DeletedFiles)
	}
	mustNotExist(t, entry)
	mustNotExist(t, entryMarker)
	mustNotExist(t, orphanMarker)
	mustExist(t, freshMarker)
}

// TestPruneImageCachePoolsSkipsTemporaryFiles ????????????????
// ?????????????????
func TestPruneImageCachePoolsSkipsTemporaryFiles(t *testing.T) {
	imagesDir := t.TempDir()
	tmp := filepath.Join(imagesDir, imageOriginalCacheSubdir, "img-1234.tmp")
	writePoolFile(t, tmp, 4096, time.Now().Add(-48*time.Hour))

	pools := ImageCachePools(imagesDir, 1, 0)
	if _, err := PruneImageCachePools(pools, 1); err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	mustExist(t, tmp)
}

// TestPruneImageCachePoolsDropsLegacyResizeVariants ????????.img????
// ?????????????????????????????????????
func TestPruneImageCachePoolsDropsLegacyResizeVariants(t *testing.T) {
	imagesDir := t.TempDir()
	resized := filepath.Join(imagesDir, imageResizeCacheSubdir)
	legacy := filepath.Join(resized, "4f2a1b"+strings.Repeat("0", 58)+".img")
	current := filepath.Join(resized, strings.Repeat("a", 64)+".480x600q80.img")
	other := filepath.Join(resized, "not-a-hash.480x600q80.img")
	writePoolFile(t, legacy, 400, time.Now())
	writePoolFile(t, current, 400, time.Now())
	writePoolFile(t, other, 400, time.Now())

	pools := ImageCachePools(imagesDir, 0, 0)
	res, err := PruneImageCachePools(pools, 0)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	mustNotExist(t, legacy)
	mustNotExist(t, other)
	mustExist(t, current)
	if res.TotalFilesBefore != 1 {
		t.Fatalf("counted files = %d, want only the current-format variant", res.TotalFilesBefore)
	}
}

// TestPruneImageCachePoolsCountsLegacyFlatLayout ??????images/ ????
// ?????????????????
func TestPruneImageCachePoolsCountsLegacyFlatLayout(t *testing.T) {
	imagesDir := t.TempDir()
	legacy := filepath.Join(imagesDir, "legacy-flat-cache")
	thumb := currentVariantPath(filepath.Join(imagesDir, imageResizeCacheSubdir), "c", 480, 600, 80)
	writePoolFile(t, legacy, 900, time.Now().Add(-30*24*time.Hour))
	writePoolFile(t, thumb, 900, time.Now().Add(-30*24*time.Hour))

	pools := ImageCachePools(imagesDir, 0, 24*time.Hour)
	res, err := PruneImageCachePools(pools, 0)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	if res.DeletedFiles != 1 {
		t.Fatalf("deleted files = %d, want 1", res.DeletedFiles)
	}
	mustNotExist(t, legacy)
	mustExist(t, thumb)
}
