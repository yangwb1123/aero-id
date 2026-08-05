// Package bulkhead provides a small semaphore-based concurrency limiter for
// background workloads (P6-06). It bounds in-flight outbound work — callback
// deliveries, audit publications — so a slow external endpoint cannot exhaust
// interactive request pools. It is a coordination aid only: correctness state
// lives in PostgreSQL (leases, receipts, delivery rows).
package bulkhead

import "context"

// Bulkhead limits concurrent in-flight work to Limit slots.
type Bulkhead struct {
	sem chan struct{}
}

// New builds a Bulkhead with the given concurrency limit. limit <= 0 falls
// back to 1 (serial), matching the pre-bulkhead worker behavior.
func New(limit int) *Bulkhead {
	if limit <= 0 {
		limit = 1
	}
	return &Bulkhead{sem: make(chan struct{}, limit)}
}

// Acquire blocks until a slot is free or ctx is done. Callers must Release
// exactly once after Acquire succeeds.
func (b *Bulkhead) Acquire(ctx context.Context) error {
	select {
	case b.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release returns one slot. Safe to call only after a successful Acquire.
func (b *Bulkhead) Release() {
	<-b.sem
}

// Run acquires a slot, runs fn, and releases it. Returns ctx.Err() when the
// slot cannot be acquired before ctx is done; fn is not executed in that case.
func (b *Bulkhead) Run(ctx context.Context, fn func()) error {
	if err := b.Acquire(ctx); err != nil {
		return err
	}
	defer b.Release()
	fn()
	return nil
}
