//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func runProgram() {
	app, err := newApplication()
	if err != nil {
		reportError("MeBox 启动失败", err)
		os.Exit(1)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	if err := app.Shutdown(); err != nil {
		reportError("MeBox 退出失败", err)
	}
}

func reportError(title string, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "%s: %v\n", title, err)
}
