//go:build integration

package outbox

import (
	"context"
	"database/sql"
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/repository/testpg"
)

func setupClaimRepo(t *testing.T) (*gorm.DB, *GormRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return db, NewGormRepository(db)
}

func appendEvent(ctx context.Context, db *gorm.DB, repo *GormRepository, eventType string) *Event {
	t := time.Now().UTC()
	e := &Event{
		ID:            uuid.New(),
		AggregateType: "message",
		AggregateID:   uuid.New().String(),
		EventType:     eventType,
		Payload:       []byte(`{"k":"v"}`),
		CreatedAt:     t,
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return repo.Append(ctx, tx, e)
	}); err != nil {
		panic(fmt.Sprintf("append event: %v", err))
	}
	return e
}

func unpublishedCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&n).Error; err != nil {
		t.Fatalf("count unpublished: %v", err)
	}
	return n
}

// REQ-1 / AC-1 core: one atomic statement claims a disjoint batch, and the
// claims are durable (visible in the DB) until released or expired.
func TestClaimBatch_AtomicClaim(t *testing.T) {
	ctx := context.Background()
	db, repo := setupClaimRepo(t)
	for i := 0; i < 10; i++ {
		appendEvent(ctx, db, repo, "message.sent")
	}

	claimed, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 10 {
		t.Fatalf("claimed %d, want 10", len(claimed))
	}
	for _, ce := range claimed {
		if ce.Claim.Token == uuid.Nil {
			t.Fatal("every claim must carry a non-nil token")
		}
		if ce.ClaimedAt.IsZero() {
			t.Fatal("every claim must carry a DB-clock claimed_at")
		}
		// Claim must be durable in the DB (not just in memory).
		var row struct {
			Token *uuid.UUID
			At    *time.Time
		}
		if err := db.Raw(`SELECT claim_token AS token, claimed_at AS at FROM outbox_events WHERE id = ?`, ce.Event.ID).Scan(&row).Error; err != nil {
			t.Fatalf("read claim row: %v", err)
		}
		if row.Token == nil || *row.Token != ce.Claim.Token {
			t.Fatalf("durable claim token = %v, want %v", row.Token, ce.Claim.Token)
		}
		if row.At == nil {
			t.Fatal("durable claimed_at must be set")
		}
	}

	// While claims are unexpired, a second claimer sees nothing.
	second, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second claimer got %d events while claims unexpired, want 0", len(second))
	}
}

// AC-1: two relays sharing one repository deliver each event exactly once
// within the claim window. The per-event barrier forces both relays to fetch
// the same rows pre-fix (red); post-fix the claim predicate excludes them.
func TestTwoRelaysExactlyOnce(t *testing.T) {
	ctx := context.Background()
	db, repo := setupClaimRepo(t)
	const n = 10
	for i := 0; i < n; i++ {
		appendEvent(ctx, db, repo, "message.sent")
	}

	var mu sync.Mutex
	invocations := map[uuid.UUID]int{}
	entered := make(chan uuid.UUID, 2*n)
	// Barrier: the first invocation of each event blocks briefly, giving a
	// second relay the chance to claim the same row (pre-fix both will).
	handler := func(e *Event) error {
		mu.Lock()
		invocations[e.ID]++
		first := invocations[e.ID] == 1
		mu.Unlock()
		entered <- e.ID
		if first {
			time.Sleep(150 * time.Millisecond)
		}
		return nil
	}

	relayA := NewRelay(repo, HandlerFunc(func(_ context.Context, e *Event) error { return handler(e) }), 0, nil)
	relayB := NewRelay(repo, HandlerFunc(func(_ context.Context, e *Event) error { return handler(e) }), 0, nil)

	var wg sync.WaitGroup
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && unpublishedCount(t, db) > 0 {
		wg.Add(2)
		go func() { defer wg.Done(); relayA.drain(ctx) }()
		go func() { defer wg.Done(); relayB.drain(ctx) }()
		wg.Wait()
		time.Sleep(10 * time.Millisecond)
	}
	if unpublishedCount(t, db) != 0 {
		t.Fatal("outbox never drained within the deadline")
	}

	mu.Lock()
	defer mu.Unlock()
	for id, c := range invocations {
		if c != 1 {
			t.Fatalf("event %s handled %d times, want exactly once", id, c)
		}
	}
	if len(invocations) != n {
		t.Fatalf("handled %d distinct events, want %d", len(invocations), n)
	}
}

// REQ-3 / AC-2: finalization is conditional on holding the matching token.
func TestConditionalFinalize(t *testing.T) {
	ctx := context.Background()
	db, repo := setupClaimRepo(t)
	e := appendEvent(ctx, db, repo, "message.sent")

	claimed, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d, want 1", len(claimed))
	}
	real := claimed[0].Claim

	// (a) Foreign token: rejected, row unchanged.
	foreign := Claim{EventID: real.EventID, Token: uuid.New()}
	if err := repo.MarkPublished(ctx, []Claim{foreign}); !stderrors.Is(err, ErrClaimLost) {
		t.Fatalf("foreign-token publish err = %v, want ErrClaimLost", err)
	}
	var pub sql.NullTime
	if err := db.Raw(`SELECT published_at FROM outbox_events WHERE id = ?`, e.ID).Scan(&pub).Error; err != nil {
		t.Fatal(err)
	}
	if pub.Valid {
		t.Fatal("foreign-token publish must not stamp published_at")
	}

	// (b) Real token: published.
	if err := repo.MarkPublished(ctx, []Claim{real}); err != nil {
		t.Fatal(err)
	}

	// (c) Re-claim with a fresh token; finalize with the dead token → no-op.
	appendEvent(ctx, db, repo, "message.sent")
	reclaimed, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("reclaimed %d, want 1", len(reclaimed))
	}
	if err := repo.MarkPublished(ctx, []Claim{real}); !stderrors.Is(err, ErrClaimLost) {
		t.Fatalf("dead-token publish err = %v, want ErrClaimLost", err)
	}
	if err := repo.MarkPublished(ctx, []Claim{reclaimed[0].Claim}); err != nil {
		t.Fatal(err)
	}
}

// REQ-4 / AC-2.3: MarkFailed is a relative, claim-gated write — a stale holder
// cannot clobber the retry counter (the lost-update regression).
func TestMarkFailedLostUpdateRegression(t *testing.T) {
	ctx := context.Background()
	db, repo := setupClaimRepo(t)
	e := appendEvent(ctx, db, repo, "message.sent")

	claimed, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	t1 := claimed[0].Claim

	// First holder records failure attempt 1.
	if err := repo.MarkFailed(ctx, t1, 1, 5, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var rc int
	if err := db.Raw(`SELECT retry_count FROM outbox_events WHERE id = ?`, e.ID).Scan(&rc).Error; err != nil {
		t.Fatal(err)
	}
	if rc != 1 {
		t.Fatalf("retry_count = %d, want 1", rc)
	}

	// Replayed stale T1 finalize: rejected, unchanged.
	if err := repo.MarkFailed(ctx, t1, 1, 5, time.Now().UTC()); !stderrors.Is(err, ErrClaimLost) {
		t.Fatalf("stale mark failed err = %v, want ErrClaimLost", err)
	}
	if err := db.Raw(`SELECT retry_count FROM outbox_events WHERE id = ?`, e.ID).Scan(&rc).Error; err != nil {
		t.Fatal(err)
	}
	if rc != 1 {
		t.Fatalf("stale mark failed changed retry_count to %d, want 1", rc)
	}

	// Fresh claim T2: failure attempt 2 lands (relative write).
	reclaimed, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("reclaimed %d, want 1", len(reclaimed))
	}
	if err := repo.MarkFailed(ctx, reclaimed[0].Claim, 2, 5, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT retry_count FROM outbox_events WHERE id = ?`, e.ID).Scan(&rc).Error; err != nil {
		t.Fatal(err)
	}
	if rc != 2 {
		t.Fatalf("retry_count = %d, want 2 (lost-update regression)", rc)
	}
}

// REQ-5 / AC-3: a crash between claim and finalize recovers via lease expiry —
// the event is re-claimed with a new token and delivered again (at-least-once).
func TestLeaseRecovery(t *testing.T) {
	ctx := context.Background()
	db, repo := setupClaimRepo(t)
	repo.claimLease = time.Second
	e := appendEvent(ctx, db, repo, "message.sent")

	// Crash after claim: no finalize.
	claimed, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d, want 1", len(claimed))
	}
	t1 := claimed[0].Claim

	// Negative: while unexpired, a second claimer cannot see the event.
	second, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second claimer got %d events while claim unexpired, want 0", len(second))
	}

	// Expire the claim via the DB clock (sleep-free).
	if err := db.Exec(`UPDATE outbox_events SET claimed_at = now() - interval '5 seconds' WHERE id = ?`, e.ID).Error; err != nil {
		t.Fatal(err)
	}
	reclaimed, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("reclaimed %d, want 1 after lease expiry", len(reclaimed))
	}
	t2 := reclaimed[0].Claim
	if t2.Token == t1.Token {
		t.Fatal("re-claim must mint a fresh token")
	}

	// The dead T1 can no longer finalize.
	if err := repo.MarkPublished(ctx, []Claim{t1}); !stderrors.Is(err, ErrClaimLost) {
		t.Fatalf("dead-token publish err = %v, want ErrClaimLost", err)
	}
	// The live T2 finalizes.
	if err := repo.MarkPublished(ctx, []Claim{t2}); err != nil {
		t.Fatal(err)
	}
	if unpublishedCount(t, db) != 0 {
		t.Fatal("outbox should be empty after T2 finalize")
	}
}

// REQ-6 / D4: backoff exclusion is Go-side — a backoff event IS claimed, and
// the relay's backstop releases it immediately (no lease-induced invisibility).
func TestBackoffGoSkipRelease(t *testing.T) {
	ctx := context.Background()
	db, repo := setupClaimRepo(t)
	e := appendEvent(ctx, db, repo, "message.sent")
	// Put the event inside its backoff window: failed once 1s ago → due in ~4s.
	past := time.Now().UTC().Add(-time.Second)
	if err := db.Exec(`UPDATE outbox_events SET retry_count = 1, last_retry_at = ? WHERE id = ?`, past, e.ID).Error; err != nil {
		t.Fatal(err)
	}

	// ClaimBatch returns it (no SQL backoff term).
	claimed, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d, want 1 (backoff events are claimable)", len(claimed))
	}
	// Release the probe claim so the relay's own ClaimBatch can take it — the
	// relay path is the one under test (backstop release after its claim).
	if err := repo.Release(ctx, []Claim{claimed[0].Claim}); err != nil {
		t.Fatal(err)
	}

	// Relay backstop: handler never invoked, claim released.
	var handled int
	relay := NewRelay(repo, HandlerFunc(func(context.Context, *Event) error {
		handled++
		return nil
	}), 0, nil)
	if n := relay.drain(ctx); n != 0 {
		t.Fatalf("drain handled %d, want 0", n)
	}
	if handled != 0 {
		t.Fatal("backoff event must not be handled")
	}

	// The released event is immediately claimable by a second instance.
	second, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("second claimer got %d events, want 1 (released claim must be visible)", len(second))
	}
}

// H5: the claim scan stays bounded under backlog — in-backoff rows are returned
// (not walked past), so ClaimBatch cost is independent of backlog size.
func TestClaimScanBoundedUnderBacklog(t *testing.T) {
	ctx := context.Background()
	db, repo := setupClaimRepo(t)
	// A due head event, oldest of all (created well before the backlog rows).
	head := &Event{
		ID:            uuid.New(),
		AggregateType: "message",
		AggregateID:   uuid.New().String(),
		EventType:     "message.sent",
		Payload:       []byte(`{"k":"v"}`),
		CreatedAt:     time.Now().UTC().Add(-time.Hour),
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return repo.Append(ctx, tx, head)
	}); err != nil {
		t.Fatal(err)
	}
	// 2000 in-backoff rows after it (retry_count 11 → 1h backoff).
	const backlog = 2000
	base := time.Now().UTC()
	for i := 0; i < backlog; i++ {
		e := &Event{
			ID:            uuid.New(),
			AggregateType: "message",
			AggregateID:   uuid.New().String(),
			EventType:     "message.sent",
			Payload:       []byte(`{"k":"v"}`),
			CreatedAt:     base.Add(-time.Duration(backlog-i) * time.Second),
		}
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return repo.Append(ctx, tx, e)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(`UPDATE outbox_events SET retry_count = 11, last_retry_at = now() - interval '1 minute'
WHERE id <> ?`, head.ID).Error; err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	claimed, err := repo.ClaimBatch(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if len(claimed) != 100 {
		t.Fatalf("claimed %d, want 100 (batch head, in-backoff rows included)", len(claimed))
	}
	if elapsed > 5*time.Second {
		t.Fatalf("claim took %v, want bounded scan (O(limit), not O(backlog))", elapsed)
	}
	// The due head event must be in the claimed batch (oldest-first ordering).
	if claimed[0].Event.ID != head.ID {
		t.Fatal("the due head event must be the first claimed row")
	}
}

// H1 / D12: renewal keeps batch claims alive across a long handler window —
// two relays with a short lease and a slow handler still deliver exactly once.
func TestTwoRelaysSlowHandlerRenewalExactlyOnce(t *testing.T) {
	ctx := context.Background()
	db, repo := setupClaimRepo(t)
	repo.claimLease = 3 * time.Second
	const n = 8
	for i := 0; i < n; i++ {
		appendEvent(ctx, db, repo, "message.sent")
	}

	var mu sync.Mutex
	invocations := map[uuid.UUID]int{}
	handler := func(e *Event) error {
		mu.Lock()
		invocations[e.ID]++
		mu.Unlock()
		time.Sleep(400 * time.Millisecond)
		return nil
	}

	relayA := NewRelay(repo, HandlerFunc(func(_ context.Context, e *Event) error { return handler(e) }), 0, nil)
	relayA.renewEvery = 500 * time.Millisecond
	relayB := NewRelay(repo, HandlerFunc(func(_ context.Context, e *Event) error { return handler(e) }), 0, nil)
	relayB.renewEvery = 500 * time.Millisecond

	var wg sync.WaitGroup
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && unpublishedCount(t, db) > 0 {
		wg.Add(2)
		go func() { defer wg.Done(); relayA.drain(ctx) }()
		go func() { defer wg.Done(); relayB.drain(ctx) }()
		wg.Wait()
		time.Sleep(10 * time.Millisecond)
	}
	if unpublishedCount(t, db) != 0 {
		t.Fatal("outbox never drained within the deadline")
	}
	mu.Lock()
	defer mu.Unlock()
	for id, c := range invocations {
		if c != 1 {
			t.Fatalf("event %s handled %d times, want exactly once (renewal keeps claims alive)", id, c)
		}
	}
}

// H1 interplay: a stale finalize must never advance retry state.
func TestMarkFailedExpiredClaimLeavesRetryCount(t *testing.T) {
	ctx := context.Background()
	db, repo := setupClaimRepo(t)
	repo.claimLease = time.Second
	e := appendEvent(ctx, db, repo, "message.sent")

	claimed, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d, want 1", len(claimed))
	}
	// Expire the claim via the DB clock (sleep-free).
	if err := db.Exec(`UPDATE outbox_events SET claimed_at = now() - interval '5 seconds' WHERE id = ?`, e.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkFailed(ctx, claimed[0].Claim, 1, 5, time.Now().UTC()); !stderrors.Is(err, ErrClaimLost) {
		t.Fatalf("expired-claim mark failed err = %v, want ErrClaimLost", err)
	}
	var rc int
	if err := db.Raw(`SELECT retry_count FROM outbox_events WHERE id = ?`, e.ID).Scan(&rc).Error; err != nil {
		t.Fatal(err)
	}
	if rc != 0 {
		t.Fatalf("expired-claim MarkFailed advanced retry_count to %d, want 0", rc)
	}
}

// REQ-7: migration 000058 is reversible; new rows start unclaimed.
func TestMigrationReversible(t *testing.T) {
	db := testpg.Start(t)
	root := filepath.Join(testpg.RepoRoot(t), "migrations")
	testpg.Migrate(t, db, root)

	var cols int
	if err := db.Raw(`SELECT count(*) FROM information_schema.columns
WHERE table_name = 'outbox_events' AND column_name IN ('claim_token', 'claimed_at')`).Scan(&cols).Error; err != nil {
		t.Fatal(err)
	}
	if cols != 2 {
		t.Fatalf("claim columns = %d, want 2", cols)
	}

	// Down, then re-up: schema round-trips.
	down, err := os.ReadFile(filepath.Join(root, "000058_claim_outbox_events.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(down)).Error; err != nil {
		t.Fatalf("down migration: %v", err)
	}
	if err := db.Raw(`SELECT count(*) FROM information_schema.columns
WHERE table_name = 'outbox_events' AND column_name IN ('claim_token', 'claimed_at')`).Scan(&cols).Error; err != nil {
		t.Fatal(err)
	}
	if cols != 0 {
		t.Fatalf("claim columns after down = %d, want 0", cols)
	}
	up, err := os.ReadFile(filepath.Join(root, "000058_claim_outbox_events.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(up)).Error; err != nil {
		t.Fatalf("re-apply up migration: %v", err)
	}
}
