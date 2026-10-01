//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/truewhile/MeBox/internal/brand"
)

const (
	runRegistryKey     = `Software\Microsoft\Windows\CurrentVersion\Run`
	runRegistryValue   = "MeBox"
	singleInstanceName = `Local\MeBox-Server`
)

var errAlreadyRunning = errors.New("MeBox is already running")

func runProgram() {
	if err := prepareWorkingDirectory(); err != nil {
		reportError("MeBox 启动失败", fmt.Errorf("切换工作目录失败: %w", err))
		return
	}

	instance, err := acquireSingleInstance()
	if errors.Is(err, errAlreadyRunning) {
		reportError("MeBox", errors.New("MeBox 已在运行，请查看右下角托盘图标"))
		return
	}
	if err != nil {
		reportError("MeBox 启动失败", fmt.Errorf("创建单实例锁失败: %w", err))
		return
	}

	app, err := newApplication()
	if err != nil {
		_ = instance.Close()
		reportError("MeBox 启动失败", err)
		return
	}

	controller := &trayController{app: app}
	systray.Run(controller.onReady, controller.onExit)

	// Release the mutex before starting the replacement process. The new
	// process must be able to acquire it immediately after the old one exits.
	_ = instance.Close()

	if !controller.readyClosed.Load() {
		_ = app.Shutdown()
		reportError("MeBox 启动失败", errors.New("系统托盘初始化失败"))
		return
	}
	if controller.restartRequested.Load() {
		if err := launchSelf(); err != nil {
			reportError("MeBox 重启失败", err)
		}
	}
}

type trayController struct {
	app *application

	readyClosed      atomic.Bool
	restartRequested atomic.Bool
	shutdownStarted  atomic.Bool
}

func (c *trayController) onReady() {
	defer func() {
		c.readyClosed.Store(true)
	}()

	systray.SetIcon(brand.Icon)
	systray.SetTooltip("MeBox")

	mOpen := systray.AddMenuItem("打开 MeBox", "在浏览器中打开 MeBox")
	mAutoStart := systray.AddMenuItemCheckbox("开机自启", "登录 Windows 后自动启动 MeBox", autoStartEnabled())
	mLogs := systray.AddMenuItem("查看日志", "打开 MeBox 应用日志")
	systray.AddSeparator()
	mRestart := systray.AddMenuItem("重启 MeBox", "重启 MeBox 服务")
	mQuit := systray.AddMenuItem("退出 MeBox", "停止服务并退出")

	initialAutoStart := mAutoStart.Checked()
	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				if err := openURL(c.app.localURL()); err != nil {
					reportError("MeBox", fmt.Errorf("打开 MeBox 失败: %w", err))
				}
			case <-mAutoStart.ClickedCh:
				enable := !mAutoStart.Checked()
				if err := setAutoStart(enable); err != nil {
					if initialAutoStart {
						mAutoStart.Check()
					} else {
						mAutoStart.Uncheck()
					}
					reportError("MeBox", fmt.Errorf("更新开机自启设置失败: %w", err))
					continue
				}
				if enable {
					mAutoStart.Check()
				} else {
					mAutoStart.Uncheck()
				}
				initialAutoStart = enable
			case <-mLogs.ClickedCh:
				if err := c.app.openLog(); err != nil {
					reportError("MeBox", fmt.Errorf("打开日志失败: %w", err))
				}
			case <-mRestart.ClickedCh:
				c.restart()
			case <-mQuit.ClickedCh:
				c.quit()
			}
		}
	}()
}

func (c *trayController) onExit() {
	_ = c.app.Shutdown()
}

func (c *trayController) quit() {
	if !c.shutdownStarted.CompareAndSwap(false, true) {
		return
	}
	go func() {
		_ = c.app.Shutdown()
		systray.Quit()
	}()
}

func (c *trayController) restart() {
	if !c.shutdownStarted.CompareAndSwap(false, true) {
		return
	}
	c.restartRequested.Store(true)
	go func() {
		_ = c.app.Shutdown()
		systray.Quit()
	}()
}

func (a *application) openLog() error {
	appLog, _, _ := logFilePaths(a.cfg)
	if appLog != "" {
		if _, err := os.Stat(appLog); err == nil {
			return openPath(appLog)
		}
		_ = os.MkdirAll(filepath.Dir(appLog), 0o750)
		return openPath(filepath.Dir(appLog))
	}

	logDir := filepath.Join(a.cfg.App.DataDir, "logs")
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return err
	}
	return openPath(logDir)
}

func prepareWorkingDirectory() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exeDir := filepath.Dir(exe)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if samePath(exeDir, cwd) || looksLikeProjectDirectory(cwd) {
		return nil
	}
	return os.Chdir(exeDir)
}

func looksLikeProjectDirectory(dir string) bool {
	for _, name := range []string{"go.mod", "config.yaml", "data", filepath.Join("web", "dist")} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

type singleInstance struct {
	handle windows.Handle
}

func acquireSingleInstance() (*singleInstance, error) {
	name, err := windows.UTF16PtrFromString(singleInstanceName)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return nil, errAlreadyRunning
	}
	if err != nil {
		return nil, err
	}
	return &singleInstance{handle: handle}, nil
}

func (s *singleInstance) Close() error {
	if s == nil || s.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(s.handle)
	s.handle = 0
	return err
}

func launchSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func autoStartEnabled() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, runRegistryKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()

	value, _, err := key.GetStringValue(runRegistryValue)
	if err != nil {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(strings.Trim(value, `"`)), filepath.Clean(exe))
}

func setAutoStart(enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runRegistryKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()

	if !enabled {
		if err := key.DeleteValue(runRegistryValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return key.SetStringValue(runRegistryValue, syscall.EscapeArg(filepath.Clean(exe)))
}

func openURL(url string) error {
	return shellOpen(url)
}

func openPath(path string) error {
	return shellOpen(path)
}

func shellOpen(target string) error {
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	verbPtr, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verbPtr, targetPtr, nil, nil, 1)
}

func reportError(title string, err error) {
	if err == nil {
		return
	}
	text, textErr := windows.UTF16PtrFromString(title + "\r\n\r\n" + err.Error())
	if textErr != nil {
		return
	}
	caption, captionErr := windows.UTF16PtrFromString("MeBox")
	if captionErr != nil {
		return
	}
	_, _ = windows.MessageBox(0, text, caption, windows.MB_OK|windows.MB_ICONERROR|windows.MB_SETFOREGROUND)
}
