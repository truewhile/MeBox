package service

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

func TestFFmpegTargetForPlatform(t *testing.T) {
	target, err := ffmpegTargetForPlatform()
	switch runtime.GOOS {
	case "windows", "linux", "darwin":
		if err != nil {
			t.Fatalf("supported platform %s/%s should resolve a target: %v", runtime.GOOS, runtime.GOARCH, err)
		}
		if target == nil || target.Label == "" || len(target.Archives) == 0 {
			t.Fatalf("target incomplete: %#v", target)
		}
		if target.Kind != "zip" && target.Kind != "tar.xz" {
			t.Fatalf("unexpected archive kind: %s", target.Kind)
		}
		for _, u := range target.Archives {
			if !strings.HasPrefix(u, "https://") {
				t.Fatalf("archive url not https: %s", u)
			}
		}
	default:
		if err == nil {
			t.Fatalf("unsupported platform %s/%s should fail", runtime.GOOS, runtime.GOARCH)
		}
	}
}

func TestSafeZipTargetRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../evil", "..\\evil", "/etc/passwd", "a/../../evil"} {
		if _, err := safeZipTarget(root, name); err == nil {
			t.Fatalf("expected traversal rejection for %q", name)
		}
	}
	if _, err := safeZipTarget(root, "bin/ffmpeg.exe"); err != nil {
		t.Fatalf("valid relative path should pass: %v", err)
	}
}

func TestFFmpegToolsStatusNoPanic(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())
	svc := NewFFmpegToolsService(&config.Config{}, zap.NewNop(), nil)
	st := svc.Status(context.Background())
	for _, key := range []string{"installing", "message", "error", "install_dir", "target", "ffmpeg", "ffprobe"} {
		if _, ok := st[key]; !ok {
			t.Fatalf("status missing key %q: %#v", key, st)
		}
	}
	ffmpeg, ok := st["ffmpeg"].(ffToolInfo)
	if !ok {
		t.Fatalf("ffmpeg field not ffToolInfo: %T", st["ffmpeg"])
	}
	if ffmpeg.Installed {
		t.Fatalf("empty config should not report installed ffmpeg: %#v", st)
	}
}

func TestStartInstallRejectsConcurrent(t *testing.T) {
	svc := NewFFmpegToolsService(&config.Config{App: config.AppConfig{DataDir: t.TempDir()}}, zap.NewNop(), nil)
	// 不真实运行：直接占用 running 标记模拟进行中的安装。
	svc.mu.Lock()
	svc.running = true
	svc.mu.Unlock()
	if err := svc.StartInstall(context.Background()); err == nil {
		t.Fatalf("second install while running should be rejected")
	}
	svc.mu.Lock()
	svc.running = false
	svc.mu.Unlock()
}
