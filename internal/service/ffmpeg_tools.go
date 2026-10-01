// Package service — ffmpeg/ffprobe 自动下载安装。
//
// FFmpegToolsService 负责「一键下载」：点击后按当前运行平台（OS+架构）选择
// 官方构建包（Windows: gyan.dev / BtbN；Linux: BtbN / johnvansickle；
// macOS: BtbN），下载解压 ffmpeg/ffprobe 到 data 目录（data/tools/ffmpeg/），
// 并把绝对路径写入设置（ffmpeg.path / ffprobe.path），系统随即使用安装的
// 工具，无需手动填写路径。
package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/repository"
)

// FFmpegToolsService 管理 ffmpeg/ffprobe 的自动下载安装状态（单飞：同一时间
// 只允许一个安装任务）。
type FFmpegToolsService struct {
	cfg  *config.Config
	log  *zap.Logger
	repo *repository.Container

	mu      sync.Mutex
	running bool
	msg     string    // 最近阶段/结果消息
	errMsg  string    // 最近一次失败原因
	started time.Time // 最近一次安装开始时间
	done    time.Time // 最近一次安装结束时间
}

// NewFFmpegToolsService 构造工具安装服务。
func NewFFmpegToolsService(cfg *config.Config, log *zap.Logger, repo *repository.Container) *FFmpegToolsService {
	return &FFmpegToolsService{cfg: cfg, log: log, repo: repo}
}

// ffmpegInstallDir 返回 data 目录下的安装位置。
func (s *FFmpegToolsService) ffmpegInstallDir() string {
	return filepath.Join(s.cfg.App.DataDir, "tools", "ffmpeg")
}

// installedBinaries 检查安装目录中是否已存在 ffmpeg/ffprobe 可执行文件。
func (s *FFmpegToolsService) installedBinaries() (ffmpeg, ffprobe string) {
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	ffmpeg = filepath.Join(s.ffmpegInstallDir(), "ffmpeg"+exe)
	ffprobe = filepath.Join(s.ffmpegInstallDir(), "ffprobe"+exe)
	if _, err := os.Stat(ffmpeg); err != nil {
		return "", ""
	}
	if _, err := os.Stat(ffprobe); err != nil {
		return "", ""
	}
	return ffmpeg, ffprobe
}

// ffToolVersion 取工具第一行版本信息。
func ffToolVersion(path string) string {
	if path == "" {
		return ""
	}
	out, err := exec.Command(path, "-version").Output() // #nosec G204 -- path 来自配置/安装目录中的已知工具。
	if err != nil {
		return ""
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	if len(line) == 0 || strings.TrimSpace(line[0]) == "" {
		return ""
	}
	return strings.TrimSpace(line[0])
}

// ffToolInfo 是单个工具的安装状态（返回给前端展示）。
type ffToolInfo struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
}

// Status 返回当前安装状态（供 GET /api/admin/tools/ffmpeg/status 使用）。
func (s *FFmpegToolsService) Status(ctx context.Context) map[string]any {
	s.mu.Lock()
	running, msg, errMsg, started, done := s.running, s.msg, s.errMsg, s.started, s.done
	s.mu.Unlock()

	startedAt, doneAt := "", ""
	if !started.IsZero() {
		startedAt = started.Format(time.RFC3339)
	}
	if !done.IsZero() {
		doneAt = done.Format(time.RFC3339)
	}

	out := map[string]any{
		"installing":  running,
		"message":     msg,
		"error":       errMsg,
		"started_at":  startedAt,
		"finished_at": doneAt,
		"install_dir": s.ffmpegInstallDir(),
	}
	target, targetErr := ffmpegTargetForPlatform()
	if targetErr != nil {
		out["target"] = map[string]any{"label": targetErr.Error()}
	} else {
		out["target"] = map[string]any{
			"os":    runtime.GOOS,
			"arch":  runtime.GOARCH,
			"label": target.Label,
		}
	}
	// 报告「系统当前实际会使用」的工具：优先已生效配置（安装完成会把设置指到
	// data 目录），其次 PATH / 常见目录。
	ffmpegPath, ferr := resolveLocalExecutable(s.cfg.App.FFmpegPath, "ffmpeg")
	ffprobePath, perr := resolveLocalExecutable(s.cfg.App.FFprobePath, "ffprobe")
	out["ffmpeg"] = ffToolInfo{Installed: ferr == nil, Path: ffmpegPath, Version: ffToolVersion(ffmpegPath)}
	out["ffprobe"] = ffToolInfo{Installed: perr == nil, Path: ffprobePath, Version: ffToolVersion(ffprobePath)}
	return out
}

// StartInstall 启动后台安装（幂等）。正在安装时返回错误；data 目录已有完整
// 工具时直接应用路径设置并返回（无需重新下载）。
func (s *FFmpegToolsService) StartInstall(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("工具正在安装中，请稍候")
	}
	if ffmpeg, ffprobe := s.installedBinaries(); ffmpeg != "" && ffprobe != "" {
		s.mu.Unlock()
		s.setMessage("检测到已安装，直接应用配置")
		if err := s.applyInstalledPaths(ctx, ffmpeg, ffprobe); err != nil {
			return err
		}
		return nil
	}
	s.running = true
	s.errMsg = ""
	s.started = time.Now()
	s.mu.Unlock()

	s.setMessage("准备下载…")
	helper.Go(s.log, "ffmpeg.install", s.runInstall)
	return nil
}

// runInstall 在后台执行下载、解压、验证与配置落盘。
func (s *FFmpegToolsService) runInstall() {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.done = time.Now()
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	ffmpegPath, ffprobePath, err := installFFmpegTools(ctx, s.log, s.cfg, s.setMessage)
	if err != nil {
		s.mu.Lock()
		s.errMsg = err.Error()
		s.msg = "安装失败"
		s.mu.Unlock()
		s.log.Error("ffmpeg 工具安装失败", zap.Error(err))
		return
	}
	if err := s.applyInstalledPaths(ctx, ffmpegPath, ffprobePath); err != nil {
		s.mu.Lock()
		s.errMsg = "安装完成，但写入设置失败：" + err.Error()
		s.msg = "安装完成，设置写入失败"
		s.mu.Unlock()
		s.log.Error("写入 ffmpeg 工具路径设置失败", zap.Error(err))
		return
	}
	s.setMessage("安装完成")
	s.log.Info("ffmpeg 工具安装完成",
		zap.String("ffmpeg", ffmpegPath), zap.String("ffprobe", ffprobePath))
}

// applyInstalledPaths 把安装后的路径写入设置表并热应用到运行配置。
func (s *FFmpegToolsService) applyInstalledPaths(ctx context.Context, ffmpeg, ffprobe string) error {
	if s.repo != nil && s.repo.Setting != nil {
		if err := s.repo.Setting.Set(ctx, "ffmpeg.path", ffmpeg); err != nil {
			return err
		}
		if err := s.repo.Setting.Set(ctx, "ffprobe.path", ffprobe); err != nil {
			return err
		}
	}
	ApplyRuntimeSetting(s.cfg, "ffmpeg.path", ffmpeg)
	ApplyRuntimeSetting(s.cfg, "ffprobe.path", ffprobe)
	return nil
}

func (s *FFmpegToolsService) setMessage(msg string) {
	s.mu.Lock()
	s.msg = msg
	s.mu.Unlock()
}
