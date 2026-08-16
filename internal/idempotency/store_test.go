//go:build integration

package idempotency_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/idempotency"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestGormStore_ReserveCompleteReplayRelease(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	store := idempotency.NewGormStore(db)
	ctx := context.Background()

	const uid, endpoint, key = "u1", "POST /v1/messages", "k-1"

	// First reserve wins.
	if _, reserved, err := store.Reserve(ctx, uid, endpoint, key); err != nil || !reserved {
		t.Fatalf("first reserve: reserved=%v err=%v", reserved, err)
	}
	// A second reserve sees an in-progress record (not completed).
	rec, reserved, err := store.Reserve(ctx, uid, endpoint, key)
	if err != nil || reserved || rec == nil || rec.Completed {
		t.Fatalf("second reserve: reserved=%v rec=%+v err=%v", reserved, rec, err)
	}

	// Complete it; a later reserve replays the stored response.
	body := []byte(`{"id":"msg-1"}`)
	if err := store.Complete(ctx, uid, endpoint, key, idempotency.Record{Status: 201, ContentType: "application/json", Body: body}, time.Hour); err != nil {
		t.Fatal(err)
	}
	rec, reserved, err = store.Reserve(ctx, uid, endpoint, key)
	if err != nil || reserved || rec == nil || !rec.Completed || rec.Status != 201 || string(rec.Body) != string(body) {
		t.Fatalf("after complete: reserved=%v rec=%+v err=%v", reserved, rec, err)
	}

	// Release lets a fresh reserve win again.
	if err := store.Release(ctx, uid, endpoint, key); err != nil {
		t.Fatal(err)
	}
	if _, reserved, err := store.Reserve(ctx, uid, endpoint, key); err != nil || !reserved {
		t.Fatalf("reserve after release: reserved=%v err=%v", reserved, err)
	}
}

// TestGormStore_Reserve_ConcurrentReclaimSingleWinner pins the single-winner
// invariant of the stale-reservation reclaim path. A crashed leader's
// reservation (force-expired via SQL) is reclaimed by concurrent retries; the
// reclaim is a compare-and-swap on the observed expires_at, so exactly one
// caller may win (reserved=true) and run the handler. Losers must observe an
// in-progress record (reserved=false, Completed=false), which the middleware
// maps to 409 idempotency.in_progress. Without the CAS predicate (pre-fix
// `WHERE id = ? AND status = 0`), two reclaimers of the same stale row both
// get RowsAffected == 1 and both win — double-executing the mutation.
//
// N exceeds testpg's MaxOpenConns (5) and every goroutine is released by a
// closed-channel barrier, so read-then-reclaim phases genuinely overlap.
func TestGormStore_Reserve_ConcurrentReclaimSingleWinner(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	store := idempotency.NewGormStore(db)
	ctx := context.Background()

	const uid, endpoint = "u-race", "POST /v1/messages"
	const n = 8 // > testpg.MaxOpenConns (5) to force real connection interleaving

	expiredAt := time.Now().UTC().Add(-time.Hour)
	round := func(key string) {
		t.Helper()

		// Fresh reservation: the first caller wins.
		if _, reserved, err := store.Reserve(ctx, uid, endpoint, key); err != nil || !reserved {
			t.Fatalf("initial reserve: reserved=%v err=%v", reserved, err)
		}
		// Simulate a crashed leader: force the reservation past reservationTTL.
		if err := db.Exec(
			`UPDATE idempotency_keys SET expires_at = ? WHERE uid = ? AND endpoint = ? AND idem_key = ?`,
			expiredAt, uid, endpoint, key,
		).Error; err != nil {
			t.Fatalf("force-expire reservation: %v", err)
		}

		// N concurrent retries behind a start barrier.
		start := make(chan struct{})
		results := make([]struct {
			rec      *idempotency.Record
			reserved bool
			err      error
		}, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				results[i].rec, results[i].reserved, results[i].err = store.Reserve(ctx, uid, endpoint, key)
			}(i)
		}
		close(start)
		wg.Wait()

		winners, losers := 0, 0
		for i, r := range results {
			if r.err != nil {
				t.Fatalf("reserve %d: unexpected error: %v", i, r.err)
			}
			if r.reserved {
				winners++
			} else {
				losers++
				if r.rec == nil || r.rec.Completed {
					t.Fatalf("reserve %d: loser rec=%+v want non-nil in-progress record", i, r.rec)
				}
			}
		}
		if winners != 1 {
			t.Fatalf("concurrent stale reclaim: got %d winner(s), want exactly 1 (losers=%d)", winners, losers)
		}

		// The winner's reservation is still usable: Complete then replay.
		body := []byte(`{"id":"race-msg"}`)
		if err := store.Complete(ctx, uid, endpoint, key, idempotency.Record{Status: 201, ContentType: "application/json", Body: body}, time.Hour); err != nil {
			t.Fatal(err)
		}
		rec, reserved, err := store.Reserve(ctx, uid, endpoint, key)
		if err != nil || reserved || rec == nil || !rec.Completed || rec.Status != 201 || string(rec.Body) != string(body) {
			t.Fatalf("replay after winner complete: reserved=%v rec=%+v err=%v", reserved, rec, err)
		}
	}

	// Distinct key per round; two rounds harden the pre-fix failure detection
	// (multi-winner is probabilistic under the bug, deterministic under the CAS).
	for i := 0; i < 2; i++ {
		round(fmt.Sprintf("race-%s-%d", uuid.NewString(), i))
	}
}
