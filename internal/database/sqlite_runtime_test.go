package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSQLiteWriteGateRejectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	gate := newSQLiteWriteGate()
	holder, err := gate.Lock(ctx)
	defer gate.Unlock(holder)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Lock error=%v, want context.Canceled", err)
	}
}

func TestSQLiteWriteGateWakesForExpiredHolder(t *testing.T) {
	gate := newSQLiteWriteGate()
	holder, err := gate.Lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Unlock(holder)
	// Make the lease expire shortly, without waiting a full minute.
	gate.mu.Lock()
	holder.acquired = time.Now().Add(-sqliteGateHoldLimit + 20*time.Millisecond)
	gate.mu.Unlock()
	done := make(chan *sqliteGateHolder, 1)
	go func() {
		next, _ := gate.Lock(t.Context())
		done <- next
	}()
	select {
	case next := <-done:
		gate.Unlock(holder) // A stale holder must not unlock its successor.
		gate.mu.Lock()
		owner := gate.owner
		gate.mu.Unlock()
		gate.Unlock(next)
		if next == nil || owner != next {
			t.Fatal("expired holder released the new owner's lock")
		}
	case <-time.After(time.Second):
		gate.Unlock(holder)
		gate.Unlock(<-done)
		t.Fatal("waiter was not woken when the holder expired")
	}
}

func TestSQLiteWriteGateWakesForCancellation(t *testing.T) {
	gate := newSQLiteWriteGate()
	holder, err := gate.Lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Unlock(holder)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		next, err := gate.Lock(ctx)
		gate.Unlock(next)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Lock error=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		gate.Unlock(holder)
		<-done
		t.Fatal("waiter was not woken for cancellation")
	}
}
