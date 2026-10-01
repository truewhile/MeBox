// Package helper provides shared utilities.
package helper

import (
	"fmt"
	"os"
	"runtime/debug"

	"go.uber.org/zap"
)

// Go runs fn in a new goroutine and recovers from panics so that a failure in
// a background task (scraper parsing remote responses, cloud-drive sync, ...)
// is logged instead of crashing the whole process. log may be nil.
func Go(log *zap.Logger, name string, fn func()) {
	go Run(log, name, fn)
}

// Run executes fn and recovers from panics, logging the task name and stack.
// Use it as the first statement inside goroutines spawned elsewhere, or wrap
// loop bodies so one bad iteration cannot kill a long-running worker.
func Run(log *zap.Logger, name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			logPanic(log, name, r)
		}
	}()
	fn()
}

// Recover runs fn and converts a panic into an error so callers can run their
// own deferred cleanup (releasing locks, updating job state) before unwinding.
func Recover(log *zap.Logger, name string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			logPanic(log, name, r)
			err = fmt.Errorf("%s panicked: %v", name, r)
		}
	}()
	return fn()
}

func logPanic(log *zap.Logger, name string, r any) {
	if log == nil {
		fmt.Fprintf(os.Stderr, "background task panicked: task=%s panic=%v\n%s\n", name, r, debug.Stack())
		return
	}
	log.Error("background task panicked",
		zap.String("task", name),
		zap.Any("panic", r),
		zap.ByteString("stack", debug.Stack()),
	)
}
