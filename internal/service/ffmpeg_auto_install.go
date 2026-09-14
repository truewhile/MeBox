package service

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

// ffmpegDownloadTarget 描述某个平台对应的官方构建下载源。
type ffmpegDownloadTarget struct {
	Label    string   // 展示名，如 "Windows x86_64"
	Kind     string   // 压缩包类型：zip / tar.xz
	Archives []string // 依次尝试的下载地址（主源 + 备用源）
}

// ffmpegTargetForPlatform 按当前运行环境（OS+架构）选择下载源。
func ffmpegTargetForPlatform() (*ffmpegDownloadTarget, error) {
	switch runtime.GOOS {
	case "windows":
		switch runtime.GOARCH {
		case "amd64":
			return &ffmpegDownloadTarget{
				Label: "Windows x86_64",
				Kind:  "zip",
				Archives: []string{
					"https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip",
					"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip",
				},
			}, nil
		case "386":
			return &ffmpegDownloadTarget{
				Label: "Windows x86",
				Kind:  "zip",
				Archives: []string{
					"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win32-gpl.zip",
				},
			}, nil
		}
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return &ffmpegDownloadTarget{
				Label: "Linux x86_64",
				Kind:  "tar.xz",
				Archives: []string{
					"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linux64-gpl.tar.xz",
					"https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-amd64-static.tar.xz",
				},
			}, nil
		case "arm64":
			return &ffmpegDownloadTarget{
				Label: "Linux ARM64",
				Kind:  "tar.xz",
				Archives: []string{
					"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linuxarm64-gpl.tar.xz",
					"https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-arm64-static.tar.xz",
				},
			}, nil
		case "arm":
			return &ffmpegDownloadTarget{
				Label: "Linux ARM (32 位)",
				Kind:  "tar.xz",
				Archives: []string{
					"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linuxarmhf-gpl.tar.xz",
				},
			}, nil
		case "loong64":
			return &ffmpegDownloadTarget{
				Label: "Linux LoongArch64",
				Kind:  "tar.xz",
				Archives: []string{
					"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linuxloongarch64-gpl.tar.xz",
				},
			}, nil
		}
	case "darwin":
		switch runtime.GOARCH {
		case "amd64":
			return &ffmpegDownloadTarget{
				Label: "macOS x86_64",
				Kind:  "zip",
				Archives: []string{
					"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-osx64-gpl.zip",
				},
			}, nil
		case "arm64":
			return &ffmpegDownloadTarget{
				Label: "macOS Apple Silicon",
				Kind:  "zip",
				Archives: []string{
					"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-osxarm64-gpl.zip",
				},
			}, nil
		}
	}
	return nil, fmt.Errorf("暂不支持自动下载的平台 %s/%s，请手动填写 ffmpeg/ffprobe 路径", runtime.GOOS, runtime.GOARCH)
}

// installFFmpegTools 按平台下载并安装 ffmpeg/ffprobe 到 data/tools/ffmpeg/，
// 返回两个可执行文件的绝对路径。progress 用于回传阶段消息（UI 展示）。
func installFFmpegTools(ctx context.Context, log *zap.Logger, cfg *config.Config, progress func(string)) (ffmpegPath, ffprobePath string, err error) {
	target, err := ffmpegTargetForPlatform()
	if err != nil {
		return "", "", err
	}
	installDir := filepath.Join(cfg.App.DataDir, "tools", "ffmpeg")
	if err := os.MkdirAll(installDir, 0o750); err != nil {
		return "", "", fmt.Errorf("创建安装目录失败: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "mebox-ffmpeg-*")
	if err != nil {
		return "", "", fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tempDir)

	progress("下载 " + target.Label + " 版本…")
	archivePath := filepath.Join(tempDir, "ffmpeg-archive."+target.Kind)
	if err := downloadFFmpegArchive(ctx, log, target.Archives, archivePath); err != nil {
		return "", "", err
	}

	progress("解压…")
	extractDir := filepath.Join(tempDir, "extract")
	if err := extractFFmpegArchive(target.Kind, archivePath, extractDir); err != nil {
		return "", "", fmt.Errorf("解压失败: %w", err)
	}

	exeSuffix := ""
	if runtime.GOOS == "windows" {
		exeSuffix = ".exe"
	}
	srcFFmpeg, srcFFprobe, err := locateFFmpegBinaries(extractDir, exeSuffix)
	if err != nil {
		return "", "", err
	}

	progress("安装到 data 目录…")
	ffmpegPath = filepath.Join(installDir, "ffmpeg"+exeSuffix)
	ffprobePath = filepath.Join(installDir, "ffprobe"+exeSuffix)
	if err := copyFileMode(srcFFmpeg, ffmpegPath); err != nil {
		return "", "", fmt.Errorf("复制 ffmpeg 失败: %w", err)
	}
	if err := copyFileMode(srcFFprobe, ffprobePath); err != nil {
		_ = os.Remove(ffmpegPath)
		return "", "", fmt.Errorf("复制 ffprobe 失败: %w", err)
	}

	// 验证两个工具都能运行（失败则回滚，避免留下坏文件）。
	for _, bin := range []string{ffmpegPath, ffprobePath} {
		cmd := exec.CommandContext(ctx, bin, "-version") // #nosec G204 -- bin 是安装目录中刚写入的固定文件名。
		if out, verr := cmd.Output(); verr != nil {
			_ = os.Remove(ffmpegPath)
			_ = os.Remove(ffprobePath)
			return "", "", fmt.Errorf("安装后 %s 无法运行：%v", filepath.Base(bin), verr)
		} else if log != nil {
			log.Info("ffmpeg 工具安装验证通过", zap.String("bin", filepath.Base(bin)),
				zap.String("version", strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])))
		}
	}
	progress("安装完成")
	return ffmpegPath, ffprobePath, nil
}

// downloadFFmpegArchive 按顺序尝试下载源，全部失败才返回错误。
func downloadFFmpegArchive(ctx context.Context, log *zap.Logger, urls []string, dest string) error {
	var lastErr error
	for i, u := range urls {
		if i > 0 && log != nil {
			log.Warn("ffmpeg 主下载源不可用，切换备用源", zap.String("url", redactSensitiveURL(u)))
		}
		if err := downloadFFmpegFile(ctx, log, u, dest); err != nil {
			lastErr = err
			if log != nil {
				log.Warn("ffmpeg 下载失败",
					zap.String("url", redactSensitiveURL(u)),
					zap.Error(redactSensitiveError(err)))
			}
			continue
		}
		return nil
	}
	return redactSensitiveError(fmt.Errorf("所有下载源均失败：%v", lastErr))
}

// downloadFFmpegFile 下载单个归档文件（最多 10 分钟，限制大小上限）。
func downloadFFmpegFile(ctx context.Context, log *zap.Logger, url, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "MeBox/ffmpeg-installer ("+runtime.GOOS+"/"+runtime.GOARCH+")")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败，HTTP 状态码 %d", resp.StatusCode)
	}
	out, err := os.Create(dest) // #nosec G304 -- dest 是安装器在临时目录下生成的文件。
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(resp.Body, 500<<20+1)) // 归档上限 500MB
	if err != nil {
		return err
	}
	if n > 500<<20 {
		return fmt.Errorf("归档文件过大（>500MB）: %s", redactSensitiveURL(url))
	}
	if log != nil {
		log.Info("ffmpeg 归档下载完成",
			zap.String("url", redactSensitiveURL(url)),
			zap.Int64("bytes", n))
	}
	return nil
}

// extractFFmpegArchive 按类型解压 zip 或 tar.xz。
func extractFFmpegArchive(kind, archivePath, destDir string) error {
	switch kind {
	case "zip":
		return unzip(nil, archivePath, destDir)
	case "tar.xz":
		return untarXZ(archivePath, destDir)
	default:
		return fmt.Errorf("不支持的归档类型: %s", kind)
	}
}

// untarXZ 解压 .tar.xz 归档（GNU tar + xz 流式解压，纯 Go 无外部依赖），
// 路径安全校验与 ZIP 解压一致。
func untarXZ(archivePath, destDir string) error {
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return err
	}
	destRoot, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	f, err := os.Open(archivePath) // #nosec G304 -- archivePath 是安装器在临时目录下生成的文件。
	if err != nil {
		return err
	}
	defer f.Close()
	xzReader, err := xz.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(xzReader)
	var totalWritten int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeZipTarget(destRoot, hdr.Name) // 与 ZIP 相同的路径穿越防护
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(hdr.Mode).Perm())
			if err != nil {
				return err
			}
			written, copyErr := io.Copy(dst, io.LimitReader(tr, maxFFmpegZipEntryBytes+1))
			totalWritten += written
			closeErr := dst.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if written > maxFFmpegZipEntryBytes || totalWritten > maxFFmpegZipTotalBytes {
				return fmt.Errorf("tar 内容过大: %s", hdr.Name)
			}
		default:
			// 符号链接/设备等一律跳过（静态构建不会依赖它们）。
			continue
		}
	}
}

// locateFFmpegBinaries 在解压目录中查找 ffmpeg/ffprobe 可执行文件（兼容
// 不同构建包的目录布局：gyan 的 bin/、BtbN/johnvansickle 的根目录等）。
func locateFFmpegBinaries(root, exeSuffix string) (ffmpeg, ffprobe string, err error) {
	wantFFmpeg := "ffmpeg" + strings.ToLower(exeSuffix)
	wantFFprobe := "ffprobe" + strings.ToLower(exeSuffix)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(d.Name()) {
		case wantFFmpeg:
			if ffmpeg == "" {
				ffmpeg = path
			}
		case wantFFprobe:
			if ffprobe == "" {
				ffprobe = path
			}
		}
		return nil
	})
	if err != nil {
		return "", "", fmt.Errorf("扫描解压目录失败: %w", err)
	}
	if ffmpeg == "" || ffprobe == "" {
		return "", "", fmt.Errorf("解压内容中未找到 ffmpeg/ffprobe 可执行文件")
	}
	return ffmpeg, ffprobe, nil
}

// copyFileMode 复制文件并赋予可执行权限。
func copyFileMode(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 -- src 来自解压目录遍历结果。
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
