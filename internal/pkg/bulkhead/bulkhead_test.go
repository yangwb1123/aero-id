package bulkhead

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBulkhead_LimitsConcurrency(t *testing.T) {
	b := New(2)
	var inFlight, maxInFlight atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.Acquire(context.Background()); err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			defer b.Release()
			current := inFlight.Add(1)
			for {
				seen := maxInFlight.Load()
				if current <= seen || maxInFlight.CompareAndSwap(seen, current) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			inFlight.Add(-1)
		}()
	}
	wg.Wait()
	if got := maxInFlight.Load(); got != 2 {
		t.Errorf("max concurrency = %d, want 2", got)
	}
}

func TestBulkhead_AcquireHonorsContext(t *testing.T) {
	b := New(1)
	if err := b.Acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire error = %v, want DeadlineExceeded", err)
	}
	b.Release()
	if err := b.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	b.Release()
}

func TestBulkhead_NewDefaultsToSerial(t *testing.T) {
	b := New(0) // invalid limit => serial
	if err := b.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("serial bulkhead must not admit a second slot, got %v", err)
	}
	b.Release()
}

// TestBulkhead_AcquireTokenReturnsReleaseFunc: a successful AcquireToken
// returns a non-nil release token; releasing it returns exactly one slot, so
// a subsequent acquire succeeds.
func TestBulkhead_AcquireTokenReturnsReleaseFunc(t *testing.T) {
	b := New(1)
	release, err := b.AcquireToken(context.Background())
	if err != nil {
		t.Fatalf("acquire token: %v", err)
	}
	if release == nil {
		t.Fatal("successful acquire must return a non-nil release token")
	}
	// Slot is held: a second acquire must block and then succeed after release.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := b.Acquire(context.Background()); err != nil {
			t.Errorf("acquire after token release: %v", err)
		}
		b.Release()
	}()
	select {
	case <-done:
		t.Fatal("second acquire succeeded while the token still held the slot")
	case <-time.After(10 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("acquire did not unblock after the token released the slot")
	}
}

// TestBulkhead_AcquireTokenDoubleReleaseNoOp: a second release of the same
// token is a no-op — no panic, no block, and exactly one slot returned (a
// fresh acquire succeeds immediately).
func TestBulkhead_AcquireTokenDoubleReleaseNoOp(t *testing.T) {
	b := New(1)
	release, err := b.AcquireToken(context.Background())
	if err != nil {
		t.Fatalf("acquire token: %v", err)
	}
	release()
	release() // must not panic, must not block, must not consume a second slot
	if err := b.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire after double release: %v", err)
	}
	b.Release()
}

// TestBulkhead_AcquireTokenCancelledNoToken: when the context is cancelled
// while the sole slot is held, AcquireToken returns the context error and a
// nil token — a release token cannot exist without a successful acquire.
func TestBulkhead_AcquireTokenCancelledNoToken(t *testing.T) {
	b := New(1)
	if err := b.Acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	token, err := b.AcquireToken(ctx)
	if token != nil {
		t.Fatalf("cancelled acquire must not return a release token")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire error = %v, want context.Canceled", err)
	}
	b.Release()
}

// TestBulkhead_AcquireTokenCancelledNoSlotConsumed: a failed AcquireToken
// never consumes a slot. With the sole slot held (channel full), the
// cancelled acquire deterministically fails — the send case cannot be ready —
// and a fresh-context acquire succeeds immediately after releasing the
// original slot, with no intervening release.
func TestBulkhead_AcquireTokenCancelledNoSlotConsumed(t *testing.T) {
	b := New(1)
	if err := b.Acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	token, err := b.AcquireToken(ctx)
	if token != nil {
		t.Fatal("cancelled acquire must not return a release token")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire error = %v, want context.Canceled", err)
	}
	// The failed acquire consumed no slot: releasing the original slot frees
	// the semaphore entirely, so a fresh acquire succeeds immediately.
	b.Release()
	if err := b.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire after failed token acquire + release: %v", err)
	}
	b.Release()
}

func TestBulkhead_Run(t *testing.T) {
	b := New(1)
	ran := false
	if err := b.Run(context.Background(), func() { ran = true }); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !ran {
		t.Fatal("fn did not run")
	}
	// Hold the only slot, then Run must respect the context deadline.
	if err := b.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := b.Run(ctx, func() {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run without slot = %v, want DeadlineExceeded", err)
	}
	b.Release()
}
