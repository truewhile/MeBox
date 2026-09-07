//go:build windows

package service

import "os/exec"

func setFFmpegSysProcAttr(cmd *exec.Cmd) {
	// Windows: CommandContext cancel is enough for the single ffmpeg process.
}
