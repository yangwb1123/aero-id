//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"gorm.io/gorm"
)

// waitForLockPoll polls pg_stat_activity until at least one backend waits on
// the given lock event (advisory / transactionid), or fails the test.
func waitForLockPoll(t *testing.T, db *gorm.DB, waitEvent string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var n int64
		if err := db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND wait_event = ?`, waitEvent).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for lock event %q", waitEvent)
}

// TestAppendConcurrentDuplicateAcrossPartitions (C1): the unique constraint
// uq_audit_ledger_tenant_event is the final guard — checkAuditDuplicate is only
// a pre-check. Two concurrent appends of the same (tenant_id, source,
// event_id) on DIFFERENT partitions take different advisory locks, so the
// loser's pre-check cannot see the winner's uncommitted row; the loser's INSERT
// then fails with a raw 23505, which must be translated to ErrAuditDuplicate
// (not a 500 at the service layer).
//
// Deterministic handshake: (1) coordinator holds a session advisory lock on
// partition p-2; (2) goroutine L appends triple T on p-2 — its pre-check
// passes, then it blocks on the xact lock; (3) winner session inserts triple T
// on p-1 UNCOMMITTED; (4) release the session lock; (5) L passes its post-lock
// pre-check (winner invisible) and blocks on the unique index; (6) commit the
// winner; (7) L's INSERT fails raw 23505 → ErrAuditDuplicate.
func TestAppendConcurrentDuplicateAcrossPartitions(t *testing.T) {
	ctx := context.Background()
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	triple := idgen.NewID()

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	if _, err := coordinator.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext('p-2'))`); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "p-2",
			TenantID: "tenant-a", EventID: triple, Action: "test.dup", OccurredAt: time.Now().UTC(),
			Payload: map[string]any{"k": "v"}}
		errCh <- repo.Append(ctx, fact)
	}()
	waitForLockPoll(t, db, "advisory", 10*time.Second)

	winner, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer winner.Close()
	if _, err := winner.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	factID := idgen.NewID()
	if _, err := winner.ExecContext(ctx, `
		INSERT INTO audit_ledger (id, hash_version, chain_sequence, source, source_region, partition_key,
			event_id, tenant_id, action, occurred_at, payload, payload_hash, previous_hash, record_hash, created_at)
		VALUES ($1, 2, 1, 'aero-id', 'local', 'p-1', $2, 'tenant-a', 'test.dup', now(), '{}'::jsonb, 'ph', '', 'rh', now())`,
		factID, triple); err != nil {
		t.Fatalf("winner ledger insert: %v", err)
	}
	if _, err := winner.ExecContext(ctx, `
		INSERT INTO audit_publications (id, fact_id, status, attempt, next_attempt_at, created_at, updated_at)
		VALUES ($1, $2, 'pending', 0, now(), now(), now())`, idgen.NewID(), factID); err != nil {
		t.Fatalf("winner publication insert: %v", err)
	}

	if _, err := coordinator.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext('p-2'))`); err != nil {
		t.Fatal(err)
	}
	// L is now past its post-lock pre-check and blocked on the unique index.
	waitForLockPoll(t, db, "transactionid", 10*time.Second)
	if _, err := winner.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}

	err = <-errCh
	if !errors.Is(err, ErrAuditDuplicate) {
		t.Fatalf("loser append error = %v, want ErrAuditDuplicate (raw 23505 translated)", err)
	}
	var ledgerRows, publicationRows int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE tenant_id = 'tenant-a' AND source = 'aero-id' AND event_id = ?`, triple).Scan(&ledgerRows).Error; err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 1 {
		t.Fatalf("ledger rows for triple = %d, want exactly 1", ledgerRows)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id
		WHERE l.tenant_id = 'tenant-a' AND l.source = 'aero-id' AND l.event_id = ?`, triple).Scan(&publicationRows).Error; err != nil {
		t.Fatal(err)
	}
	if publicationRows != 1 {
		t.Fatalf("publication rows for triple = %d, want exactly 1", publicationRows)
	}
}

// TestAppendConcurrentDuplicateBarrier is the regression net for C1: 8
// concurrent appends of the same triple across partitions must all resolve to
// nil or ErrAuditDuplicate with exactly one ledger + one publication row.
func TestAppendConcurrentDuplicateBarrier(t *testing.T) {
	ctx := context.Background()
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	triple := idgen.NewID()

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			<-start
			fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "p-" + string(rune('a'+i)),
				TenantID: "tenant-a", EventID: triple, Action: "test.dup", OccurredAt: time.Now().UTC(),
				Payload: map[string]any{"i": i}}
			errs <- repo.Append(ctx, fact)
		}(i)
	}
	close(start)
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil && !errors.Is(err, ErrAuditDuplicate) {
			t.Fatalf("worker %d error = %v, want nil or ErrAuditDuplicate", i, err)
		}
	}
	var ledgerRows, publicationRows int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE tenant_id = 'tenant-a' AND source = 'aero-id' AND event_id = ?`, triple).Scan(&ledgerRows).Error; err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 1 {
		t.Fatalf("ledger rows = %d, want exactly 1", ledgerRows)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_publications`).Scan(&publicationRows).Error; err != nil {
		t.Fatal(err)
	}
	if publicationRows != 1 {
		t.Fatalf("publication rows = %d, want exactly 1", publicationRows)
	}
}

// TestAuditPublicationDrainCycleSingleDelivery (REQ-2): one publication row
// per fact across the full claim/finish/re-claim cycle — claim → failing
// finish (pending, past) → re-claim (attempt incremented) → completed → empty
// claim. Exactly one publication row exists throughout; there is no second
// delivery path.
func TestAuditPublicationDrainCycleSingleDelivery(t *testing.T) {
	ctx := context.Background()
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	pubRepo, ok := repo.(AuditPublicationRepository)
	if !ok {
		t.Fatal("ledger repo must implement AuditPublicationRepository")
	}
	fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "drain",
		TenantID: "tenant-a", EventID: idgen.NewID(), Action: "test.drain", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{}}
	if err := repo.Append(ctx, fact); err != nil {
		t.Fatal(err)
	}
	assertPublicationCount := func(want int64) {
		t.Helper()
		var n int64
		if err := db.Raw(`SELECT count(*) FROM audit_publications`).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("publication rows = %d, want %d", n, want)
		}
	}
	assertPublicationCount(1)

	items, err := pubRepo.ClaimPublication(ctx, "worker-1", time.Minute, 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("first claim = %d items, err %v; want 1", len(items), err)
	}
	if items[0].Attempt != 0 || items[0].Status != "delivering" {
		t.Fatalf("first claim attempt/status = %d/%s, want 0/delivering (M-4: claim never increments)", items[0].Attempt, items[0].Status)
	}
	// Failing finish: keep the row pending with a past next attempt. The
	// caller persists the incremented attempt (1 = this delivery attempt).
	past := time.Now().UTC().Add(-time.Minute)
	if err := pubRepo.FinishPublication(ctx, items[0].ID, "worker-1", items[0].LeaseGeneration, 1, "pending", "audit.publication_failed", past); err != nil {
		t.Fatal(err)
	}
	// Re-claim: same row, attempt untouched by the claim (the persisted value
	// from the failed finish is what comes back).
	items, err = pubRepo.ClaimPublication(ctx, "worker-1", time.Minute, 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("re-claim = %d items, err %v; want 1", len(items), err)
	}
	if items[0].Attempt != 1 {
		t.Fatalf("re-claim attempt = %d, want 1 (persisted by the pending finish, not by the claim)", items[0].Attempt)
	}
	// Completed finish (attempt 2 = second delivery attempt); claim is now
	// empty; still exactly one row.
	if err := pubRepo.FinishPublication(ctx, items[0].ID, "worker-1", items[0].LeaseGeneration, 2, "completed", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	items, err = pubRepo.ClaimPublication(ctx, "worker-1", time.Minute, 50)
	if err != nil || len(items) != 0 {
		t.Fatalf("claim after completion = %d items, err %v; want 0", len(items), err)
	}
	assertPublicationCount(1)
}

// TestDeadLetterPublicationNeverReclaimed (A-4.2, FM-5): a publication
// finished as dead_letter is terminal. The claim predicate admits only
// (pending AND due) or (delivering AND lease expired), so no worker — the
// same one or a different one — can ever re-claim a dead_letter row.
func TestDeadLetterPublicationNeverReclaimed(t *testing.T) {
	ctx := context.Background()
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	pubRepo, ok := repo.(AuditPublicationRepository)
	if !ok {
		t.Fatal("ledger repo must implement AuditPublicationRepository")
	}
	fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "deadletter",
		TenantID: "tenant-a", EventID: idgen.NewID(), Action: "test.deadletter", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{}}
	if err := repo.Append(ctx, fact); err != nil {
		t.Fatal(err)
	}
	items, err := pubRepo.ClaimPublication(ctx, "worker-1", time.Minute, 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("first claim = %d items, err %v; want 1", len(items), err)
	}
	if items[0].Attempt != 0 || items[0].Status != "delivering" {
		t.Fatalf("first claim attempt/status = %d/%s, want 0/delivering (M-4: claim never increments)", items[0].Attempt, items[0].Status)
	}
	id := items[0].ID
	// Terminal settlement: MaxAttempts exhausted (publicationOutcome's
	// dead_letter decision; the repo-level test drives FinishPublication
	// directly — the end-to-end MaxAttempts leg is A-4.3). The caller
	// persists the incremented attempt (1 = this delivery attempt).
	if err := pubRepo.FinishPublication(ctx, id, "worker-1", items[0].LeaseGeneration, 1, "dead_letter", "audit.publication_failed",
		time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Neither the same worker nor a different worker can re-claim it.
	for _, worker := range []string{"worker-1", "worker-2"} {
		items, err = pubRepo.ClaimPublication(ctx, worker, time.Minute, 50)
		if err != nil || len(items) != 0 {
			t.Fatalf("claim after dead_letter (%s) = %d items, err %v; want 0", worker, len(items), err)
		}
	}
	var got struct {
		Status    string
		ErrorCode string
		Attempt   int
	}
	if err := db.Raw(`SELECT status, error_code, attempt FROM audit_publications WHERE id = ?`, id).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != "dead_letter" || got.ErrorCode != "audit.publication_failed" {
		t.Fatalf("terminal row = %s/%s, want dead_letter/audit.publication_failed", got.Status, got.ErrorCode)
	}
	var n int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("publication rows = %d, want exactly 1", n)
	}
}

// TestExpiredLeasePublicationReclaimedByAnotherWorker (A-5, FM-3/FM-4): the
// status='delivering' AND lease_until < now claim leg and cross-worker
// ownership fencing. An unexpired lease blocks other workers; after lease
// expiry (crash simulation via DB-clock backdate) a different worker
// re-claims (attempt untouched — M-4); a stale finish from the original
// worker is fenced by ErrAuditPublicationLeaseLost; the new owner settles
// exactly once and the row is then unclaimable.
func TestExpiredLeasePublicationReclaimedByAnotherWorker(t *testing.T) {
	ctx := context.Background()
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	pubRepo, ok := repo.(AuditPublicationRepository)
	if !ok {
		t.Fatal("ledger repo must implement AuditPublicationRepository")
	}
	fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "lease",
		TenantID: "tenant-a", EventID: idgen.NewID(), Action: "test.lease", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{}}
	if err := repo.Append(ctx, fact); err != nil {
		t.Fatal(err)
	}
	items, err := pubRepo.ClaimPublication(ctx, "worker-1", 5*time.Minute, 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("first claim = %d items, err %v; want 1", len(items), err)
	}
	if items[0].LeaseUntil == nil {
		t.Fatal("claimed lease_until is nil, want ≈ now+5min")
	}
	// M-1: the lease window is written and compared in DB time (now() +
	// interval), so the fencing clock is shared across instances and app clock
	// skew cannot re-claim a live lease. Assert the persisted value against the
	// DB clock — the returned struct carries only an app-clock approximation.
	var inWindow bool
	if err := db.Raw(`SELECT lease_until > now() + interval '4 minutes' AND lease_until < now() + interval '6 minutes'
		FROM audit_publications WHERE id = ?`, items[0].ID).Scan(&inWindow).Error; err != nil {
		t.Fatal(err)
	}
	if !inWindow {
		t.Fatal("persisted lease_until is outside ≈ now()+5min (DB clock)")
	}
	if items[0].Attempt != 0 || items[0].Status != "delivering" || items[0].LeaseOwner != "worker-1" {
		t.Fatalf("claim state = attempt %d status %s owner %s, want 0/delivering/worker-1",
			items[0].Attempt, items[0].Status, items[0].LeaseOwner)
	}
	id := items[0].ID

	// Unexpired lease must block other workers (delivering leg predicate).
	items, err = pubRepo.ClaimPublication(ctx, "worker-2", 5*time.Minute, 50)
	if err != nil || len(items) != 0 {
		t.Fatalf("claim by worker-2 with unexpired lease = %d items, err %v; want 0", len(items), err)
	}

	// Crash simulation: the row stays delivering; backdate the lease with the
	// DB clock (M-1: the lease is DB-clock, so expiry is shared across
	// processes by construction).
	if err := db.Exec(`UPDATE audit_publications SET lease_until = now() - interval '1 minute' WHERE id = ?`, id).Error; err != nil {
		t.Fatal(err)
	}

	// Lease expired: worker-2 re-claims; owner changes and attempt stays
	// untouched (M-4: claim never increments — the counter only moves at
	// settlement).
	items, err = pubRepo.ClaimPublication(ctx, "worker-2", 5*time.Minute, 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("re-claim by worker-2 after expiry = %d items, err %v; want 1", len(items), err)
	}
	if items[0].Attempt != 0 || items[0].LeaseOwner != "worker-2" {
		t.Fatalf("re-claim state = attempt %d owner %s, want 0/worker-2", items[0].Attempt, items[0].LeaseOwner)
	}

	// Stale finish from the original worker is fenced by the lease guard.
	if err := pubRepo.FinishPublication(ctx, id, "worker-1", 1, 1, "completed", "", time.Now().UTC()); !errors.Is(err, ErrAuditPublicationLeaseLost) {
		t.Fatalf("stale finish error = %v, want ErrAuditPublicationLeaseLost", err)
	}

	// The new owner settles exactly once; the row is terminal and unclaimable.
	if err := pubRepo.FinishPublication(ctx, id, "worker-2", items[0].LeaseGeneration, 1, "completed", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	items, err = pubRepo.ClaimPublication(ctx, "worker-2", 5*time.Minute, 50)
	if err != nil || len(items) != 0 {
		t.Fatalf("claim after completion = %d items, err %v; want 0", len(items), err)
	}
	var n int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("publication rows = %d, want exactly 1", n)
	}
}

// installClaimPoisonTrigger installs a BEFORE UPDATE trigger that raises on the
// claim transition (pending -> delivering) of the publication row for factID:
// the claim-stage poison-row injection (FM-1).
func installClaimPoisonTrigger(t *testing.T, db *gorm.DB, factID domain.ID) {
	t.Helper()
	if err := db.Exec(fmt.Sprintf(`CREATE FUNCTION fail_claim_poison() RETURNS trigger AS $$
		BEGIN
			IF OLD.status = 'pending' AND NEW.status = 'delivering' AND OLD.fact_id = '%s' THEN
				RAISE EXCEPTION 'injected claim poison';
			END IF;
			RETURN NEW;
		END $$ LANGUAGE plpgsql`, factID.String())).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_claim_poison_trigger
		BEFORE UPDATE ON audit_publications FOR EACH ROW EXECUTE FUNCTION fail_claim_poison()`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DROP TRIGGER IF EXISTS fail_claim_poison_trigger ON audit_publications`).Error
		_ = db.Exec(`DROP FUNCTION IF EXISTS fail_claim_poison()`).Error
	})
}

// installFinishCompletedTrigger installs a BEFORE UPDATE trigger that raises on
// any transition into status='completed': the finish-stage raw-DB-error
// injection (FM-3).
func installFinishCompletedTrigger(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE FUNCTION fail_completed_finish() RETURNS trigger AS $$
		BEGIN
			IF NEW.status = 'completed' THEN
				RAISE EXCEPTION 'injected finish failure';
			END IF;
			RETURN NEW;
		END $$ LANGUAGE plpgsql`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_completed_finish_trigger
		BEFORE UPDATE ON audit_publications FOR EACH ROW EXECUTE FUNCTION fail_completed_finish()`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DROP TRIGGER IF EXISTS fail_completed_finish_trigger ON audit_publications`).Error
		_ = db.Exec(`DROP FUNCTION IF EXISTS fail_completed_finish()`).Error
	})
}

// TestClaimPublicationPoisonRowReturnsRawError (FM-1): a row that errors
// inside ClaimPublication fails the whole claim transaction and rolls it back
// atomically — no partial claim, no row stranded delivering, attempts
// untouched. The raw DB error surfaces to the caller untouched (never remapped
// to ErrAuditPublicationLeaseLost, which only FinishPublication can produce);
// after the poison is removed the same claim succeeds and both rows settle.
func TestClaimPublicationPoisonRowReturnsRawError(t *testing.T) {
	ctx := context.Background()
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	pubRepo, ok := repo.(AuditPublicationRepository)
	if !ok {
		t.Fatal("ledger repo must implement AuditPublicationRepository")
	}
	poison := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "poison",
		TenantID: "tenant-a", EventID: idgen.NewID(), Action: "test.poison", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{}}
	if err := repo.Append(ctx, poison); err != nil {
		t.Fatal(err)
	}
	healthy := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "poison",
		TenantID: "tenant-a", EventID: idgen.NewID(), Action: "test.poison", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{}}
	if err := repo.Append(ctx, healthy); err != nil {
		t.Fatal(err)
	}
	installClaimPoisonTrigger(t, db, poison.ID)

	_, err := pubRepo.ClaimPublication(ctx, "worker-1", time.Minute, 50)
	if err == nil {
		t.Fatal("claim must fail on the poison row")
	}
	if errors.Is(err, ErrAuditPublicationLeaseLost) {
		t.Fatalf("claim error = %v, want the raw DB error (lease-lost only comes from FinishPublication)", err)
	}
	if !strings.Contains(err.Error(), "injected claim poison") {
		t.Fatalf("claim error = %v, want the injected trigger error", err)
	}
	// Atomic rollback: neither row was leased or attempt-incremented; nothing
	// stranded delivering.
	for _, fact := range []*domain.AuditFact{poison, healthy} {
		var row struct {
			Status     string
			Attempt    int
			LeaseOwner string
		}
		if err := db.Raw(`SELECT ap.status, ap.attempt, ap.lease_owner FROM audit_publications ap
			JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.event_id = ?`, fact.EventID).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.Status != "pending" || row.Attempt != 0 || row.LeaseOwner != "" {
			t.Fatalf("row for %s after failed claim = %s/attempt %d/owner %q, want pending/0/\"\"",
				fact.EventID, row.Status, row.Attempt, row.LeaseOwner)
		}
	}

	// Recovery: poison removed, the same claim succeeds and both rows settle.
	_ = db.Exec(`DROP TRIGGER IF EXISTS fail_claim_poison_trigger ON audit_publications`).Error
	_ = db.Exec(`DROP FUNCTION IF EXISTS fail_claim_poison()`).Error
	items, err := pubRepo.ClaimPublication(ctx, "worker-1", time.Minute, 50)
	if err != nil || len(items) != 2 {
		t.Fatalf("claim after poison removal = %d items, err %v; want 2", len(items), err)
	}
	for _, item := range items {
		if err := pubRepo.FinishPublication(ctx, item.ID, "worker-1", item.LeaseGeneration, 1, "completed", "", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	remaining, err := pubRepo.ClaimPublication(ctx, "worker-1", time.Minute, 50)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("claim after settlement = %d items, err %v; want 0", len(remaining), err)
	}
}

// TestFinishPublicationRawDBErrorReturned (FM-3, repository half): a raw DB
// error on the finish UPDATE (injected trigger) is returned to the caller
// untouched — it is NOT remapped to ErrAuditPublicationLeaseLost — and the row
// is left byte-identical (still delivering, same lease owner and attempt), so
// the decision is re-made after lease-expiry re-claim.
func TestFinishPublicationRawDBErrorReturned(t *testing.T) {
	ctx := context.Background()
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	pubRepo, ok := repo.(AuditPublicationRepository)
	if !ok {
		t.Fatal("ledger repo must implement AuditPublicationRepository")
	}
	fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "finish",
		TenantID: "tenant-a", EventID: idgen.NewID(), Action: "test.finish", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{}}
	if err := repo.Append(ctx, fact); err != nil {
		t.Fatal(err)
	}
	items, err := pubRepo.ClaimPublication(ctx, "worker-1", time.Minute, 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim = %d items, err %v; want 1", len(items), err)
	}
	installFinishCompletedTrigger(t, db)

	err = pubRepo.FinishPublication(ctx, items[0].ID, "worker-1", items[0].LeaseGeneration, 1, "completed", "", time.Now().UTC())
	if err == nil {
		t.Fatal("finish must fail on the injected trigger")
	}
	if errors.Is(err, ErrAuditPublicationLeaseLost) {
		t.Fatalf("finish error = %v, want the raw DB error, not lease-lost", err)
	}
	if !strings.Contains(err.Error(), "injected finish failure") {
		t.Fatalf("finish error = %v, want the injected trigger error", err)
	}
	// Row untouched: still delivering with the same lease owner and attempt
	// (the increment was part of the rolled-back UPDATE — M-4 keeps the
	// increment at settlement, so the failed settlement changes nothing).
	var row struct {
		Status     string
		Attempt    int
		LeaseOwner string
	}
	if err := db.Raw(`SELECT status, attempt, lease_owner FROM audit_publications WHERE id = ?`, items[0].ID).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "delivering" || row.Attempt != 0 || row.LeaseOwner != "worker-1" {
		t.Fatalf("row after failed finish = %s/attempt %d/owner %q, want delivering/0/worker-1",
			row.Status, row.Attempt, row.LeaseOwner)
	}

	// Poison removed: the same finish commits.
	_ = db.Exec(`DROP TRIGGER IF EXISTS fail_completed_finish_trigger ON audit_publications`).Error
	_ = db.Exec(`DROP FUNCTION IF EXISTS fail_completed_finish()`).Error
	if err := pubRepo.FinishPublication(ctx, items[0].ID, "worker-1", items[0].LeaseGeneration, 1, "completed", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.Raw(`SELECT status FROM audit_publications WHERE id = ?`, items[0].ID).Scan(&status).Error; err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("status after recovery = %q, want completed", status)
	}
}

// TestConcurrentClaimPublicationPartition (T-4.1, REQ-3.1): the
// pending-and-due claim leg partitions under concurrency — W workers race the
// FOR UPDATE SKIP LOCKED claim select plus the in-tx lease write, then finish
// their own claims (claim-vs-finish interleavings happen for free: a finish
// committing while another worker's claim tx is in flight is exactly the
// interleaving the fence and the claim predicate must survive). The
// deterministic contract, asserted only after wg.Wait() in the test goroutine
// (workers never call t.Fatalf):
//   - multiplicity: every seeded row claimed exactly once — map[id]int with
//     len == N, every entry == 1, sum == N. A set union cannot see a double
//     claim (two workers claiming the same row still yield a set of size N);
//     the multiplicity map can.
//   - per-worker claimed >= 1: the start barrier makes all first claims
//     simultaneous and limit=4 < N=20 forces >= 2 claim waves (the default
//     limit=50 would let one worker drain the whole seed and the other three
//     exit on empty without ever exercising the claim leg concurrently).
//   - every row settled completed exactly once with attempt 1; none stranded
//     delivering; a post-phase duplicate finish by a foreign worker is fenced
//     by ErrAuditPublicationLeaseLost.
//
// Overlap is best-effort exercise — the partition is guaranteed by Postgres
// row locks, not by the harness, so no sleeps are needed (and must never be
// added to "stabilize" this test). -race is hygiene here, not mechanism: the
// partition proof is the DB, not the harness mutexes. SKIP LOCKED is
// deliberately behavior-pinned, not clause-pinned: replacing it with plain
// FOR UPDATE still partitions (transactions serialize) and the regression
// surfaces as head-of-line blocking — a clause-presence pin would need timing
// probes and be flaky.
func TestConcurrentClaimPublicationPartition(t *testing.T) {
	ctx := context.Background()
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	pubRepo, ok := repo.(AuditPublicationRepository)
	if !ok {
		t.Fatal("ledger repo must implement AuditPublicationRepository")
	}
	const (
		rows    = 20
		workers = 4
		limit   = 4 // < rows: forces >= 2 claim waves under the barrier
	)
	for i := 0; i < rows; i++ {
		fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "claim-partition",
			TenantID: "tenant-a", EventID: idgen.NewID(), Action: "test.partition", OccurredAt: time.Now().UTC(),
			Payload: map[string]any{}}
		if err := repo.Append(ctx, fact); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	claims := make(map[domain.ID]int)
	var claimsMu sync.Mutex
	perWorker := make([]int, workers)
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			workerID := fmt.Sprintf("worker-%d", w)
			<-start
			for {
				items, err := pubRepo.ClaimPublication(ctx, workerID, 5*time.Minute, limit)
				if err != nil {
					errCh <- fmt.Errorf("worker %d claim: %w", w, err)
					return
				}
				if len(items) == 0 {
					return
				}
				claimsMu.Lock()
				for _, item := range items {
					claims[item.ID]++
				}
				perWorker[w] += len(items)
				claimsMu.Unlock()
				for _, item := range items {
					if err := pubRepo.FinishPublication(ctx, item.ID, workerID, item.LeaseGeneration, 1, "completed", "", time.Now().UTC()); err != nil {
						errCh <- fmt.Errorf("worker %d finish %s: %w", w, item.ID, err)
						return
					}
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	if len(claims) != rows {
		t.Fatalf("distinct claimed rows = %d, want %d (lost row)", len(claims), rows)
	}
	total := 0
	for id, count := range claims {
		if count != 1 {
			t.Fatalf("row %s claimed %d times, want exactly 1 (claim overlap)", id, count)
		}
		total += count
	}
	if total != rows {
		t.Fatalf("total claims = %d, want %d", total, rows)
	}
	for w := 0; w < workers; w++ {
		if perWorker[w] == 0 {
			t.Fatalf("worker %d claimed 0 rows, want >= 1 (barrier + limit < N)", w)
		}
	}
	var completed, delivering int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'completed' AND attempt = 1`).Scan(&completed).Error; err != nil {
		t.Fatal(err)
	}
	if completed != rows {
		t.Fatalf("completed rows = %d, want %d", completed, rows)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'delivering'`).Scan(&delivering).Error; err != nil {
		t.Fatal(err)
	}
	if delivering != 0 {
		t.Fatalf("rows stranded delivering = %d, want 0", delivering)
	}

	// Post-phase fence probe: every row is completed (lease cleared), so a
	// duplicate finish by a foreign worker must be fenced. Runs after all
	// workers exited, so it cannot race a legitimate owner.
	var fenceID domain.ID
	for id := range claims {
		fenceID = id
		break
	}
	if err := pubRepo.FinishPublication(ctx, fenceID, "fence-probe-worker", 1, 2, "completed", "", time.Now().UTC()); !errors.Is(err, ErrAuditPublicationLeaseLost) {
		t.Fatalf("duplicate finish error = %v, want ErrAuditPublicationLeaseLost", err)
	}
}
