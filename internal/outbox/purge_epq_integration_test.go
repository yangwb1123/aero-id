//go:build integration

package outbox_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/repository/testpg"
	"github.com/google/uuid"
)

// TestOutboxPurgeDeleteBlockedByDeadLetterFlip (T-2, db §2): the outbox
// dead-letter purge leg had the same EPQ hole as the publication leg — the
// status predicate lived only inside the DELETE's subquery, so a row flipped
// out of dead_letter concurrently could be deleted by the outer DELETE
// (EPQ re-checks only the outer quals: id IN (subquery) still matches). The
// fix mirrors the published leg's own guard: the outer DELETE carries
// `AND dead_letter = true`, re-checked against the NEW row version.
//
// No live code flips an outbox row out of dead_letter today (MarkFailed sets
// it and clears the claim in the same statement; MarkPublished/Release/
// RenewClaims require a held claim, which dead rows never have), so the
// concurrent writer is synthetic — the exact shape a future outbox requeue
// must use. This is latent defense-in-depth, pinned deterministically: the
// writer holds its UPDATE (row lock held, pre-commit); the purge DELETE
// captures the row at its snapshot and blocks on the row lock; after COMMIT
// the EPQ recheck sees dead_letter = false and the row survives.
func TestOutboxPurgeDeleteBlockedByDeadLetterFlip(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	// Seed one dead-lettered outbox row far past the cutoff.
	rowID := uuid.New()
	if err := db.Exec(`INSERT INTO outbox_events (id, event_type, aggregate_type, aggregate_id, payload,
		created_at, retry_count, dead_letter, claim_token, claimed_at)
		VALUES (?, 'account.provisioned', 'account', 'a-1', '{}',
		now() - interval '30 days', 5, true, NULL, NULL)`, rowID).Error; err != nil {
		t.Fatal(err)
	}

	// B2 writer: hold `UPDATE ... SET dead_letter = false` (the requeue shape)
	// in an explicit transaction.
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		`UPDATE outbox_events SET dead_letter = false WHERE id = $1 AND dead_letter = true`, rowID); err != nil {
		t.Fatal(err)
	}

	// Purge goroutine: the real PurgeDeadLetters; its DELETE captures the row
	// at the statement snapshot (still dead_letter) and blocks on the
	// writer's row lock (the outbox leg has no SKIP LOCKED — guard only).
	repo := outbox.NewGormRepository(db)
	purgedCh := make(chan int64, 1)
	errCh := make(chan error, 1)
	go func() {
		n, err := repo.PurgeDeadLetters(ctx, time.Now().UTC(), time.Now().UTC(), 500)
		if err != nil {
			errCh <- err
			return
		}
		purgedCh <- n
	}()

	// The DELETE is provably in flight and blocked on the writer's row lock.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked bool
		if err := sqlDB.QueryRow(`SELECT EXISTS(
			SELECT 1 FROM pg_locks WHERE locktype = 'transactionid' AND granted = false AND pid <> pg_backend_pid())`).
			Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("purge DELETE never blocked on the held row — the interleaving was not forced")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The writer commits the flip.
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}

	select {
	case purged := <-purgedCh:
		if purged != 0 {
			t.Fatalf("purge deleted %d rows over the concurrent flip, want 0 (guarded)", purged)
		}
	case err := <-errCh:
		t.Fatalf("purge: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("purge scan never completed after the writer COMMIT")
	}

	// The flipped row survives, dead_letter = false.
	var deadLetter bool
	var count int64
	if err := db.Raw(`SELECT dead_letter FROM outbox_events WHERE id = ?`, rowID).Scan(&deadLetter).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT count(*) FROM outbox_events WHERE id = ?`, rowID).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 || deadLetter {
		t.Fatalf("row after purge = count %d dead_letter %v, want 1/false (survived)", count, deadLetter)
	}
}

// TestOutboxPurgeDeleteBlockedByPublishedFlip (T-2 positive twin): the
// published leg already carries the outer guard (AND published_at IS NOT
// NULL); a hypothetical replay that clears published_at concurrently must not
// lose the row to the purge.
func TestOutboxPurgeDeleteBlockedByPublishedFlip(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	rowID := uuid.New()
	if err := db.Exec(`INSERT INTO outbox_events (id, event_type, aggregate_type, aggregate_id, payload,
		created_at, published_at, retry_count, dead_letter)
		VALUES (?, 'account.provisioned', 'account', 'a-1', '{}',
		now() - interval '30 days', now() - interval '30 days', 0, false)`, rowID).Error; err != nil {
		t.Fatal(err)
	}

	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		`UPDATE outbox_events SET published_at = NULL WHERE id = $1 AND published_at IS NOT NULL`, rowID); err != nil {
		t.Fatal(err)
	}

	repo := outbox.NewGormRepository(db)
	purgedCh := make(chan int64, 1)
	errCh := make(chan error, 1)
	go func() {
		n, err := repo.PurgeDeadLetters(ctx, time.Now().UTC(), time.Now().UTC(), 500)
		if err != nil {
			errCh <- err
			return
		}
		purgedCh <- n
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked bool
		if err := sqlDB.QueryRow(`SELECT EXISTS(
			SELECT 1 FROM pg_locks WHERE locktype = 'transactionid' AND granted = false AND pid <> pg_backend_pid())`).
			Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("purge DELETE never blocked on the held row — the interleaving was not forced")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	select {
	case purged := <-purgedCh:
		if purged != 0 {
			t.Fatalf("purge deleted %d rows over the concurrent flip, want 0 (guarded)", purged)
		}
	case err := <-errCh:
		t.Fatalf("purge: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("purge scan never completed after the writer COMMIT")
	}
	var count int64
	if err := db.Raw(`SELECT count(*) FROM outbox_events WHERE id = ?`, rowID).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	var publishedAtIsNull bool
	if err := db.Raw(`SELECT published_at IS NULL FROM outbox_events WHERE id = ?`, rowID).Scan(&publishedAtIsNull).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 || !publishedAtIsNull {
		t.Fatalf("row after purge = count %d published_at_is_null %v, want 1/true (survived)", count, publishedAtIsNull)
	}
}
