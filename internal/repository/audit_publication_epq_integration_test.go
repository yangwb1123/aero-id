//go:build integration

package repository_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// TestPurgeDeleteBlockedByRequeueCommitEPQ (T-1, db §2 High): the purge
// DELETE can race a concurrent requeue commit on the same row. The requeue tx
// is held open between its UPDATE (row lock held, pre-append, pre-commit) by
// the B1 advisory-lock trigger; the purge scan then runs while the row is
// still dead_letter-from-the-DELETE's-perspective. Two guards make the row
// survive deterministically:
//
//   - FOR UPDATE SKIP LOCKED in the purge subquery: the scan skips the row
//     locked by the in-flight requeue instead of capturing it;
//   - the outer status qual (WHERE id IN (sub) AND status = 'dead_letter'):
//     even if the DELETE captured the id at its snapshot, the EPQ recheck
//     evaluates the outer quals against the NEW row version (pending) and
//     matches nothing.
//
// Without both, the row is deleted and the requeue's committed audit fact
// records a requeue the purge silently undid (the exact-partition invariant
// the landing claimed probabilistically).
func TestPurgeDeleteBlockedByRequeueCommitEPQ(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	// Seed exactly one dead_letter row, THEN install the trigger (fixture
	// writes must never fire it).
	id := drivePublicationDeadLetter(t, db, publications, "tenant-a")
	const holdKey = "replay-hold-pub"
	installHoldRequeueTrigger(t, sqlDB, "trg_hold_replay_pub", "audit_publications", holdKey)
	release := holdAdvisoryLock(t, sqlDB, holdKey)

	// Goroutine R: the REAL RequeuePublication — its UPDATE fires the trigger
	// and the tx blocks holding the row lock, pre-append/pre-commit.
	errCh := make(chan error, 1)
	type requeueResult struct {
		n     int
		facts int
	}
	resultCh := make(chan requeueResult, 1)
	go func() {
		n, facts, err := publications.RequeuePublication(ctx, nil, 0, replayIntent("", []string{"tenant-a"}))
		if err != nil {
			errCh <- fmt.Errorf("requeue: %w", err)
			return
		}
		resultCh <- requeueResult{n: n, facts: len(facts)}
	}()

	// The trigger fired iff the UPDATE ran: the row lock is held and the tx is
	// parked on the test session's advisory lock.
	waitForLockPoll(t, sqlDB, "advisory", 10*time.Second)

	// Goroutine P: the purge scan while the requeue holds the row lock. With
	// SKIP LOCKED it skips the locked row and returns 0 (never blocks); the
	// outer guard keeps the same outcome if the row were captured at snapshot.
	purgedCh := make(chan int64, 1)
	go func() {
		n, err := publications.PurgeDeadLetterPublications(ctx, time.Now().UTC().Add(time.Hour), 10)
		if err != nil {
			errCh <- fmt.Errorf("purge: %w", err)
			return
		}
		purgedCh <- n
	}()

	var purged int64
	select {
	case purged = <-purgedCh:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("purge scan blocked on the requeued row — SKIP LOCKED missing")
	}
	if purged != 0 {
		t.Fatalf("purge deleted %d rows over the in-flight requeue, want 0 (requeued row must survive)", purged)
	}

	// Release the requeue: trigger returns, the fact appends in-tx, the tx
	// commits. The row is pending with a cleared lease and manual_replay.
	release()
	select {
	case result := <-resultCh:
		if result.n != 1 {
			t.Fatalf("requeue = %d, want 1", result.n)
		}
		if result.facts != 1 {
			t.Fatalf("requeue facts = %d, want 1 (appended atomically with the reset)", result.facts)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("requeue did not commit after the advisory release")
	}
	status, errorCode, attempt, leaseOwner, _ := publicationState(t, db, id)
	if status != "pending" || errorCode != "manual_replay" || attempt != 0 || leaseOwner != "" {
		t.Fatalf("row = %s/%s/%d/%q, want pending/manual_replay/0/cleared", status, errorCode, attempt, leaseOwner)
	}
	var facts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts != 1 {
		t.Fatalf("requeue facts = %d, want 1", facts)
	}
}

// TestPurgeDeleteBlockedByRequeueCommitEPQDeliveryLeg (T-1 positive twin): the
// delivery leg already carried the outer status guard (callback_audit.go); the
// remediation adds the same SKIP LOCKED + (updated_at, id) ordering. This test
// pins the reference behavior — a concurrently requeued delivery survives a
// purge — so the guarded leg can never regress into the unguarded shape.
func TestPurgeDeleteBlockedByRequeueCommitEPQDeliveryLeg(t *testing.T) {
	ctx := context.Background()
	db, callbacks, _ := startCallbackAuditRepo(t, callbackAuditTestKey)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	sub := &domain.CallbackSubscription{ID: idgen.NewID(), OwnerUID: "u-1", TenantID: "tenant-a",
		Endpoint: "https://cb.example.test/hook", EventTypes: []string{"account.provisioned"},
		SecretCiphertext: "cipher", Enabled: true, Verified: true}
	if err := callbacks.CreateSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	delivery := &domain.CallbackDelivery{SubscriptionID: sub.ID, EventID: idgen.NewID(),
		Action: "account.provisioned", Payload: map[string]any{}}
	if err := callbacks.EnqueueDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	fetched, err := callbacks.FetchDueDeliveries(ctx, 10, "test-worker", 5*time.Minute)
	if err != nil || len(fetched) != 1 {
		t.Fatalf("fetch = %v, %v; want 1", fetched, err)
	}
	if err := callbacks.MarkDelivery(ctx, fetched[0].ID, "test-worker", fetched[0].LeaseGeneration, "dead_letter", 0, "", "delivery.failed",
		time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	const holdKey = "replay-hold-del"
	installHoldRequeueTrigger(t, sqlDB, "trg_hold_replay_del", "callback_deliveries", holdKey)
	release := holdAdvisoryLock(t, sqlDB, holdKey)

	errCh := make(chan error, 1)
	resultCh := make(chan int, 1)
	go func() {
		n, facts, err := callbacks.RequeueDead(ctx, nil, 0, deliveryReplayIntent("", []string{"tenant-a"}))
		if err != nil {
			errCh <- fmt.Errorf("requeue: %w", err)
			return
		}
		if len(facts) != 1 {
			errCh <- fmt.Errorf("delivery requeue facts = %d, want 1", len(facts))
			return
		}
		resultCh <- n
	}()
	waitForLockPoll(t, sqlDB, "advisory", 10*time.Second)

	purgedCh := make(chan int64, 1)
	go func() {
		n, err := callbacks.PurgeDeadLetterDeliveries(ctx, time.Now().UTC().Add(time.Hour), 10)
		if err != nil {
			errCh <- fmt.Errorf("purge: %w", err)
			return
		}
		purgedCh <- n
	}()
	select {
	case purged := <-purgedCh:
		if purged != 0 {
			t.Fatalf("purge deleted %d deliveries over the in-flight requeue, want 0", purged)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("purge scan blocked on the requeued delivery — SKIP LOCKED missing")
	}

	release()
	select {
	case n := <-resultCh:
		if n != 1 {
			t.Fatalf("requeue = %d, want 1", n)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("requeue did not commit after the advisory release")
	}
	var row struct {
		Status     string
		ErrorCode  string
		Attempt    int
		LeaseOwner any
	}
	if err := db.Raw(`SELECT status, error_code, attempt, lease_owner FROM callback_deliveries WHERE id = ?`, fetched[0].ID).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "pending" || row.ErrorCode != "manual_replay" || row.Attempt != 0 || row.LeaseOwner != nil {
		t.Fatalf("delivery = %s/%s/%d/%v, want pending/manual_replay/0/cleared", row.Status, row.ErrorCode, row.Attempt, row.LeaseOwner)
	}
}

// TestRequeueCrashMidTxRollsBackByteIdentical (T-6 §8.1): a process death
// mid-transaction (after the requeue UPDATE, before commit) rolls back
// identically to an explicit ROLLBACK. The deterministic equivalent drives the
// requeue UPDATE verbatim on a held raw connection, observes the row lock, and
// ROLLBACKs. The row must be byte-identical to the pre-crash state and no
// replay facts or publication rows may exist.
func TestRequeueCrashMidTxRollsBackByteIdentical(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)
	id := drivePublicationDeadLetter(t, db, publications, "tenant-a")
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	// B2: held raw connection executes the requeue UPDATE verbatim
	// (requeueReset's shape) inside an explicit transaction.
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		`UPDATE audit_publications SET status = 'pending', attempt = 0, next_attempt_at = now(),
		 lease_owner = '', lease_until = NULL, error_code = 'manual_replay', updated_at = now()
		 WHERE status = 'dead_letter' AND id = $1`, id); err != nil {
		t.Fatal(err)
	}

	// The row lock is held: a second connection attempting the same UPDATE
	// blocks on the transactionid (deterministic observation of the held row).
	blocked := make(chan error, 1)
	go func() {
		// status = status keeps the probe a true no-op: it still needs the row
		// lock (so it blocks), but changes nothing once released.
		if err := db.Exec(`UPDATE audit_publications SET status = status WHERE id = ?`, id).Error; err != nil {
			blocked <- err
			return
		}
		blocked <- nil
	}()
	waitForLockPoll(t, sqlDB, "transactionid", 10*time.Second)

	// Crash: the connection aborts without committing (Postgres rolls back
	// identically for a killed backend).
	if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("blocked UPDATE: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("blocked UPDATE never completed after ROLLBACK")
	}

	// Byte-identical pre-crash state: dead_letter, original attempt, original
	// error code, cleared lease.
	status, errorCode, attempt, leaseOwner, _ := publicationState(t, db, id)
	if status != "dead_letter" || errorCode != "audit.publication_failed" || attempt != 1 || leaseOwner != "" {
		t.Fatalf("row after mid-tx crash = %s/%s/%d/%q, want dead_letter/audit.publication_failed/1/cleared",
			status, errorCode, attempt, leaseOwner)
	}
	var facts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts != 0 {
		t.Fatalf("replay facts after mid-tx crash = %d, want 0", facts)
	}
}
