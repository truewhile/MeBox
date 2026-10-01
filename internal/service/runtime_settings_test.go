package service

import (
	"testing"

	"github.com/truewhile/MeBox/internal/config"
)

func TestApplyRuntimeSettingTranscodeSwitches(t *testing.T) {
	cfg := &config.Config{}
	cfg.Transcoder.Enabled = true
	cfg.Transcoder.HardwareAccel = false

	ApplyRuntimeSetting(cfg, "transcode.enabled", "false")
	if cfg.Transcoder.Enabled {
		t.Fatal("transcode.enabled=false should disable transcoding")
	}

	ApplyRuntimeSetting(cfg, "transcode.hw_enabled", "true")
	if !cfg.Transcoder.HardwareAccel {
		t.Fatal("transcode.hw_enabled=true should enable hardware accel")
	}

	ApplyRuntimeSetting(cfg, "transcode.hw_accel", "nvenc")
	if cfg.Transcoder.Encoder != "nvenc" {
		t.Fatalf("encoder = %q, want nvenc", cfg.Transcoder.Encoder)
	}

	ApplyRuntimeSetting(cfg, "transcode.max_jobs", "1")
	if cfg.Transcoder.MaxConcurrent != 1 {
		t.Fatalf("max concurrent = %d, want 1", cfg.Transcoder.MaxConcurrent)
	}

	ApplyRuntimeSetting(cfg, "app.max_cpu_threads", "99")
	if cfg.App.MaxCPUThreads != 8 {
		t.Fatalf("max cpu threads = %d, want clamp 8", cfg.App.MaxCPUThreads)
	}

	ApplyRuntimeSetting(cfg, "app.max_cpu_threads", "0")
	if cfg.App.MaxCPUThreads != 1 {
		t.Fatalf("max cpu threads = %d, want clamp 1", cfg.App.MaxCPUThreads)
	}
}

func TestApplyRuntimeSettingMemoryCacheLimit(t *testing.T) {
	cfg := &config.Config{}

	ApplyRuntimeSetting(cfg, "cache.memory_max_size_mb", "64")
	if cfg.Cache.MemoryMaxSizeMB != 64 {
		t.Fatalf("memory cache limit = %d, want 64", cfg.Cache.MemoryMaxSizeMB)
	}

	ApplyRuntimeSetting(cfg, "cache.memory_max_size_mb", "")
	if cfg.Cache.MemoryMaxSizeMB != config.DefaultCacheMemoryMaxSizeMB {
		t.Fatalf("empty value should restore default, got %d", cfg.Cache.MemoryMaxSizeMB)
	}

	ApplyRuntimeSetting(cfg, "cache.memory_max_size_mb", "99999")
	if cfg.Cache.MemoryMaxSizeMB != 4096 {
		t.Fatalf("memory cache limit should clamp to 4096, got %d", cfg.Cache.MemoryMaxSizeMB)
	}
}
