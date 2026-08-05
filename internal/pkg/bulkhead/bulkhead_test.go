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
