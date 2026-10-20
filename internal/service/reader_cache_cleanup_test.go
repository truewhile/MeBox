package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 长期运行时各类缓存必须有上限或清理，否则会慢慢吃满磁盘。
// 这里覆盖新增的「书源文件缓存 + 孤儿临时文件」清理任务。

func writeAgedFile(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func TestJobCleanReaderFilesPrunesByRetention(t *testing.T) {
	cacheDir := t.TempDir()
	s := &SchedulerService{cacheDir: cacheDir, readerFilesTTLHours: 24}

	// 超龄的书源文件（字体/静态 JS）应被删掉；新文件保留。
	oldFile := filepath.Join(cacheDir, "reader", "files", "abc.woff")
	newFile := filepath.Join(cacheDir, "reader", "files", "nested", "fresh.js")
	writeAgedFile(t, oldFile, 48*time.Hour)
	writeAgedFile(t, newFile, time.Hour)

	if err := s.jobCleanReaderFiles(context.Background()); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("超龄书源文件应被删除，err=%v", err)
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("未超龄文件不应被删除: %v", err)
	}
}

func TestJobCleanReaderFilesRemovesOrphanTempFiles(t *testing.T) {
	cacheDir := t.TempDir()
	s := &SchedulerService{cacheDir: cacheDir, readerFilesTTLHours: 24}
	ctx := context.Background()

	// 中断的转码产物 .mp3.part 与正文缓存的 .content-* 临时文件。
	oldPart := filepath.Join(cacheDir, "reader-audio", "deadbeef.mp3.part")
	freshPart := filepath.Join(cacheDir, "reader-audio", "running.mp3.part")
	oldContentTmp := filepath.Join(cacheDir, "reader-content", "aa", "bb", ".content-123")
	// 成品与正常缓存文件不能被误删。
	finished := filepath.Join(cacheDir, "reader-audio", "done.mp3")
	contentJSON := filepath.Join(cacheDir, "reader-content", "aa", "bb", "chapter.json")

	writeAgedFile(t, oldPart, 48*time.Hour)
	writeAgedFile(t, freshPart, time.Minute)
	writeAgedFile(t, oldContentTmp, 48*time.Hour)
	writeAgedFile(t, finished, 48*time.Hour)
	writeAgedFile(t, contentJSON, 48*time.Hour)

	if err := s.jobCleanReaderFiles(ctx); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if _, err := os.Stat(oldPart); !os.IsNotExist(err) {
		t.Fatalf("孤儿 .part 应被删除，err=%v", err)
	}
	if _, err := os.Stat(oldContentTmp); !os.IsNotExist(err) {
		t.Fatalf("孤儿 .content-* 应被删除，err=%v", err)
	}
	if _, err := os.Stat(freshPart); err != nil {
		t.Fatalf("正在写入的 .part 不应被删除: %v", err)
	}
	if _, err := os.Stat(finished); err != nil {
		t.Fatalf("转码成品不应被删除: %v", err)
	}
	if _, err := os.Stat(contentJSON); err != nil {
		t.Fatalf("正文缓存文件不应被删除: %v", err)
	}
}

// 未配置 TTL 时用兜底保留时长，而不是「不清理」。
func TestJobCleanReaderFilesFallsBackToDefaultRetention(t *testing.T) {
	cacheDir := t.TempDir()
	s := &SchedulerService{cacheDir: cacheDir} // readerFilesTTLHours = 0

	// 40 天前的文件应被兜底规则（30 天）清掉。
	veryOld := filepath.Join(cacheDir, "reader", "files", "ancient.js")
	writeAgedFile(t, veryOld, 40*24*time.Hour)

	if err := s.jobCleanReaderFiles(context.Background()); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if _, err := os.Stat(veryOld); !os.IsNotExist(err) {
		t.Fatalf("未配置 TTL 也应有兜底清理，err=%v", err)
	}
}

// 目录不存在时不应报错（缓存未启用或尚未产生文件）。
func TestJobCleanReaderFilesMissingDirs(t *testing.T) {
	s := &SchedulerService{cacheDir: filepath.Join(t.TempDir(), "nope")}
	if err := s.jobCleanReaderFiles(context.Background()); err != nil {
		t.Fatalf("目录不存在应静默通过: %v", err)
	}
	// cacheDir 为空（未配置）同样直接返回。
	empty := &SchedulerService{}
	if err := empty.jobCleanReaderFiles(context.Background()); err != nil {
		t.Fatalf("未配置缓存目录应静默通过: %v", err)
	}
}
