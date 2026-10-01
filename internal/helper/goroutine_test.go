package helper

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newObservedLogger(t *testing.T) (*zap.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.ErrorLevel)
	return zap.New(core), logs
}

func waitForLogs(t *testing.T, logs *observer.ObservedLogs, n int) []observer.LoggedEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if entries := logs.All(); len(entries) >= n {
			return entries
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d log entries, got %d", n, logs.Len())
	return nil
}

func TestRunRecoversPanic(t *testing.T) {
	log, logs := newObservedLogger(t)
	ran := false
	Run(log, "unit.panic", func() {
		ran = true
		panic("boom")
	})
	if !ran {
		t.Fatal("fn should have run before panicking")
	}
	entries := waitForLogs(t, logs, 1)
	if entries[0].Message != "background task panicked" {
		t.Fatalf("unexpected message: %s", entries[0].Message)
	}
	found := false
	for _, f := range entries[0].Context {
		if f.Key == "task" && f.String == "unit.panic" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected task name in log context: %v", entries[0].Context)
	}
}

func TestRunNoPanicNoLog(t *testing.T) {
	log, logs := newObservedLogger(t)
	Run(log, "unit.ok", func() {})
	time.Sleep(10 * time.Millisecond)
	if logs.Len() != 0 {
		t.Fatalf("expected no error log, got %d", logs.Len())
	}
}

func TestRecoverConvertsPanicToError(t *testing.T) {
	log, _ := newObservedLogger(t)
	err := Recover(log, "unit.recover", func() error {
		panic("kaboom")
	})
	if err == nil {
		t.Fatal("expected error from recovered panic")
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("panic value should be in error: %v", err)
	}
}

func TestRecoverReturnsFnError(t *testing.T) {
	sentinel := errors.New("plain failure")
	err := Recover(nil, "unit.err", func() error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected fn error, got %v", err)
	}
}

func TestRunWithNilLoggerDoesNotCrash(t *testing.T) {
	Run(nil, "unit.nillog", func() { panic("still caught") })
}

func TestGoLogsPanicFromSpawnedGoroutine(t *testing.T) {
	log, logs := newObservedLogger(t)
	Go(log, "unit.go", func() { panic("async boom") })
	waitForLogs(t, logs, 1)
}
