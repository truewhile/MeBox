//go:build unix

package service

import (
	"os/exec"
	"syscall"
)

// setFFmpegSysProcAttr puts ffmpeg in its own process group so cancel can
// tear down the whole group (and any helpers) reliably on Linux/macOS.
func setFFmpegSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
