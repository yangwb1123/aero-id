//go:build integration

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/connector/auditgovernance"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"gorm.io/gorm"
)

func startCallbackService(t *testing.T, key string) (*gorm.DB, CallbackService, repository.CallbackRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: key})
	callbackRepo := repository.NewCallbackRepository(db, ledger)
	svc := NewCallbackService(callbackRepo, CallbackConfig{
		MasterKey: "integration-master-key-0123456789abcdef", RequestTimeout: 2 * time.Second,
		MaxResponseBytes: 4096, MaxAttempts: 8, RetryBase: time.Second, WorkerConcurrency: 4,
	})
	return db, svc, callbackRepo
}

func seedInFlightDelivery(t *testing.T, repo repository.CallbackRepository) (*domain.CallbackSubscription, *domain.CallbackDelivery) {
	t.Helper()
	subscription := &domain.CallbackSubscription{ID: idgen.NewID(), OwnerUID: "u-1", TenantID: "tenant-a",
		Endpoint: "https://cb.example.test/hook", EventTypes: []string{"account.provisioned"},
		SecretCiphertext: "cipher", Enabled: true, Verified: true}
	if err := repo.CreateSubscription(t.Context(), subscription); err != nil {
		t.Fatal(err)
	}
	delivery := &domain.CallbackDelivery{ID: idgen.NewID(), SubscriptionID: subscription.ID, EventID: idgen.NewID(),
		Action: "account.provisioned", Payload: map[string]any{"schema_version": 1}, Status: "pending"}
	if err := repo.EnqueueDelivery(t.Context(), delivery); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.FetchDueDeliveries(t.Context(), 50, "worker-1", 5*time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d deliveries, err %v; want 1", len(claimed), err)
	}
	return subscription, claimed[0]
}

func replayCtx() context.Context {
	return WithIdentity(context.Background(), &domain.TokenIdentity{UID: "u-1", Issuer: "snaplink",
		Subject: "u-1", TenantID: "tenant-a"})
}

// staticTokenSource satisfies auditgovernance.TokenSource for loopback tests
// without a real OAuth token endpoint.
type staticTokenSource struct {
	tenantID string
}

func (s staticTokenSource) AccessToken(context.Context, auditgovernance.SourceBinding) (string, error) {
	return "service-token", nil
}

func (s staticTokenSource) HasBinding(tenantID string) bool {
	return tenantID == s.tenantID
}

func (s staticTokenSource) Invalidate(auditgovernance.SourceBinding) error {
	return nil
}

func (s staticTokenSource) BindingUnhealthy(auditgovernance.SourceBinding) bool {
	return false
}

func (s staticTokenSource) MarkBindingUnhealthy(auditgovernance.SourceBinding) {}

// TestAuditPublicationDuplicateConflictReceiptSettlesCompleted (FM-3 × FM-9):
// a worker crash after the governance endpoint accepted the event but before
// FinishPublication leaves the row leased; after lease expiry another worker
// re-claims and re-delivers the same event (at-least-once by design). The
// endpoint answers the duplicate with a conflict receipt, which the connector
// must treat as idempotent success — the row settles completed, never
// dead_letter.
func TestAuditPublicationDuplicateConflictReceiptSettlesCompleted(t *testing.T) {
	ctx := t.Context()
	db, svc, _ := startCallbackService(t, "integration-callback-svc-key-0123456789abcdef")
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-audit-ledger-key-0123456789abcdef"})
	pubRepo, ok := ledger.(repository.AuditPublicationRepository)
	if !ok {
		t.Fatal("ledger repo must implement AuditPublicationRepository")
	}
	concrete := svc.(*callbackService)
	concrete.SetAuditPublicationRepository(pubRepo)

	var mu sync.Mutex
	var deliveries []map[string]any
	var conflictReplies int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/events" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode event body: %v", err)
		}
		mu.Lock()
		deliveries = append(deliveries, body)
		conflict := len(deliveries) > 1
		if conflict {
			conflictReplies++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"receipt": map[string]any{
			"event_id": body["event_id"], "tenant_id": "tenant-a", "status": "ledgered",
			"accepted_at": time.Now().UTC(), "conflict": conflict,
		}})
	}))
	defer server.Close()
	tokens := staticTokenSource{tenantID: "tenant-a"}
	publisher, err := auditgovernance.NewPublisher(auditgovernance.Config{
		Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
	}, tokens, nil)
	if err != nil {
		t.Fatal(err)
	}
	concrete.SetAuditPublishers(publisher)

	fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "fm9",
		TenantID: "tenant-a", EventID: idgen.NewID(), Action: "test.fm9", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{}}
	if err := ledger.Append(ctx, fact); err != nil {
		t.Fatal(err)
	}
	first, err := pubRepo.ClaimPublication(ctx, "worker-1", time.Minute, 50)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim = %d items, err %v; want 1", len(first), err)
	}
	// Delivery #1: the endpoint accepts (202 + ledgered receipt). No
	// FinishPublication — simulated worker crash after remote success.
	if err := concrete.publishAuditFact(ctx, first[0].Fact); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	// Lease expires; worker-2 re-claims and re-delivers the same event.
	if err := db.Exec(`UPDATE audit_publications SET lease_until = now() - interval '1 minute' WHERE id = ?`, first[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	second, err := pubRepo.ClaimPublication(ctx, "worker-2", time.Minute, 50)
	if err != nil || len(second) != 1 {
		t.Fatalf("re-claim = %d items, err %v; want 1", len(second), err)
	}
	if second[0].Attempt != first[0].Attempt || second[0].ID != first[0].ID {
		t.Fatalf("re-claim attempt/id = %d/%v, want %d/same row (M-4: claim never increments)",
			second[0].Attempt, second[0].ID, first[0].Attempt)
	}
	// Delivery #2: conflict receipt for the already-accepted event_id. With
	// the FM-9 resolution this is idempotent success, so the row completes.
	// The settlement persists the incremented attempt (2 = second delivery).
	if err := concrete.publishAuditFact(ctx, second[0].Fact); err != nil {
		t.Fatalf("duplicate publish must be idempotent success, got %v", err)
	}
	if err := pubRepo.FinishPublication(ctx, second[0].ID, "worker-2", 2, "completed", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	remaining, err := pubRepo.ClaimPublication(ctx, "worker-1", time.Minute, 50)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("claim after completion = %d items, err %v; want 0", len(remaining), err)
	}
	var status string
	if err := db.Raw(`SELECT status FROM audit_publications WHERE id = ?`, first[0].ID).Scan(&status).Error; err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("publication status = %q, want completed (never dead_letter)", status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deliveries) != 2 || conflictReplies != 1 {
		t.Fatalf("deliveries = %d (conflict replies %d), want 2 deliveries with exactly 1 conflict", len(deliveries), conflictReplies)
	}
	for _, delivery := range deliveries {
		if delivery["event_id"] != fact.ID.String() || delivery["idempotency_key"] != fact.ID.String() {
			t.Fatalf("delivery event_id/idempotency_key = %v/%v, want %s for both", delivery["event_id"], delivery["idempotency_key"], fact.ID)
		}
	}
}

// TestDrainExhaustsAttemptsIntoDeadLetter (REQ-4 A-4.3, FM-5): a full drain
// cycle with a persistently failing governance publisher terminates in
// dead_letter after MaxAttempts, the row is never re-claimed afterwards, and
// the terminal settlement is counted on the outcome metric. It also pins the
// A-2.2 leg at integration level: exactly one enqueueFact (one
// callback_deliveries row) per claimed item.
func TestDrainExhaustsAttemptsIntoDeadLetter(t *testing.T) {
	ctx := t.Context()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-audit-ledger-key-0123456789abcdef"})
	callbackRepo := repository.NewCallbackRepository(db, ledger)
	svc := NewCallbackService(callbackRepo, CallbackConfig{
		MasterKey: "integration-master-key-0123456789abcdef", RequestTimeout: 2 * time.Second,
		MaxResponseBytes: 4096, MaxAttempts: 3, RetryBase: time.Second, WorkerConcurrency: 4,
	}).(*callbackService)
	pubRepo, ok := ledger.(repository.AuditPublicationRepository)
	if !ok {
		t.Fatal("ledger repo must implement AuditPublicationRepository")
	}
	svc.SetAuditPublicationRepository(pubRepo)
	publisher := &failingAuditPublisher{}
	svc.SetAuditPublishers(publisher)
	metrics := &metricSpy{}
	svc.metrics = metrics

	// Seed one enabled subscription so enqueueFact materializes one delivery
	// row per claim (the A-2.2 single-enqueue leg).
	if err := callbackRepo.CreateSubscription(ctx, &domain.CallbackSubscription{
		ID: idgen.NewID(), OwnerUID: "u-1", TenantID: "tenant-a",
		Endpoint: "https://cb.example.test/hook", EventTypes: []string{"test.dlq"},
		Enabled: true, Verified: true,
	}); err != nil {
		t.Fatal(err)
	}
	fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "dlq",
		TenantID: "tenant-a", EventID: idgen.NewID(), Action: "test.dlq", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{}}
	if err := ledger.Append(ctx, fact); err != nil {
		t.Fatal(err)
	}

	// Drive drain cycles; between cycles the row is pending with a future
	// next_attempt_at (backoff), so backdate it to make the next claim due.
	const maxCycles = 5
	var status, errorCode string
	var attempt int
	for cycle := 0; cycle < maxCycles; cycle++ {
		svc.drainPublications(ctx)
		var row struct {
			Status    string
			ErrorCode string
			Attempt   int
		}
		if err := db.Raw(`SELECT ap.status, ap.error_code, ap.attempt
			FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id
			WHERE l.event_id = ?`, fact.EventID).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		status, errorCode, attempt = row.Status, row.ErrorCode, row.Attempt
		if status == "dead_letter" {
			break
		}
		if err := db.Exec(`UPDATE audit_publications SET next_attempt_at = now() - interval '1 minute'
			WHERE fact_id = (SELECT id FROM audit_ledger WHERE event_id = ?)`, fact.EventID).Error; err != nil {
			t.Fatal(err)
		}
	}
	if status != "dead_letter" {
		t.Fatalf("publication status after %d cycles = %q, want dead_letter", maxCycles, status)
	}
	if errorCode != "audit.publication_failed" {
		t.Fatalf("error code = %q, want audit.publication_failed", errorCode)
	}
	if attempt != 3 {
		t.Fatalf("attempt = %d, want 3 (MaxAttempts)", attempt)
	}
	if publisher.calls != 3 {
		t.Fatalf("publisher calls = %d, want 3 (one per claimed item)", publisher.calls)
	}
	// One enqueueFact per claimed item: every claim reached the publisher,
	// which publishAuditFact only does after enqueueFact succeeded. The
	// callback_deliveries row itself is deduped by uq_callback_delivery_event
	// (EnqueueDelivery is OnConflict DoNothing), so exactly one row exists for
	// the three enqueue attempts — the enqueue layer, not the drain loop, is
	// the dedup point.
	var enqueued int64
	if err := db.Raw(`SELECT count(*) FROM callback_deliveries WHERE event_id = ?`, fact.ID).Scan(&enqueued).Error; err != nil {
		t.Fatal(err)
	}
	if enqueued != 1 {
		t.Fatalf("enqueued deliveries = %d, want 1 (deduped across 3 claims)", enqueued)
	}
	// Terminal settlement counted on the outcome metric, per committed decision.
	if len(metrics.outcomes) != 3 || metrics.outcomes[0] != "pending" || metrics.outcomes[1] != "pending" || metrics.outcomes[2] != "dead_letter" {
		t.Fatalf("outcome observations = %v, want [pending pending dead_letter]", metrics.outcomes)
	}
	// Never re-claimed: a further drain cycle and a direct claim see nothing.
	svc.drainPublications(ctx)
	remaining, err := pubRepo.ClaimPublication(ctx, "worker-x", time.Minute, 50)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("claim after dead_letter = %d items, err %v; want 0", len(remaining), err)
	}
}

// recordingAuditPublisher succeeds on every publish and records the delivered
// facts — the first succeeding-publisher fake (only failingAuditPublisher
// existed before T-10.2c) — used to pin the exactly-once completed path.
// File-local to this test file.
type recordingAuditPublisher struct {
	facts []*domain.AuditFact
}

func (r *recordingAuditPublisher) Publish(_ context.Context, fact *domain.AuditFact) error {
	r.facts = append(r.facts, fact)
	return nil
}

// firstFactDrainHarness (T-10.2c): real publication repository + recording
// publisher + metricSpy; one enabled/verified subscription and one
// Record-produced first fact for the matching (tenant, action), so
// enqueueFact materializes exactly one delivery row.
func firstFactDrainHarness(t *testing.T) (*gorm.DB, *callbackService, *recordingAuditPublisher, *metricSpy, domain.ID) {
	t.Helper()
	ctx := t.Context()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-audit-ledger-key-0123456789abcdef"})
	callbackRepo := repository.NewCallbackRepository(db, ledger)
	svc := NewCallbackService(callbackRepo, CallbackConfig{
		MasterKey: "integration-master-key-0123456789abcdef", RequestTimeout: 2 * time.Second,
		MaxResponseBytes: 4096, MaxAttempts: 8, RetryBase: time.Second, WorkerConcurrency: 4,
	}).(*callbackService)
	pubRepo, ok := ledger.(repository.AuditPublicationRepository)
	if !ok {
		t.Fatal("ledger repo must implement AuditPublicationRepository")
	}
	svc.SetAuditPublicationRepository(pubRepo)
	publisher := &recordingAuditPublisher{}
	svc.SetAuditPublishers(publisher)
	metrics := &metricSpy{}
	svc.metrics = metrics
	if err := callbackRepo.CreateSubscription(ctx, &domain.CallbackSubscription{
		ID: idgen.NewID(), OwnerUID: "u-1", TenantID: "tenant-one",
		Endpoint: "https://cb.example.test/hook", EventTypes: []string{"account.provisioned"},
		Enabled: true, Verified: true,
	}); err != nil {
		t.Fatal(err)
	}
	eventID := idgen.NewID()
	if err := NewAuditLedgerService(ledger).Record(ctx, AuditFactInput{TenantID: "tenant-one",
		EventID: eventID, ActorUID: "actor-one", Action: "account.provisioned"}); err != nil {
		t.Fatal(err)
	}
	return db, svc, publisher, metrics, eventID
}

// assertPublicationState reads the publication row for eventID and requires
// the exact status/error_code pair.
func assertPublicationState(t *testing.T, db *gorm.DB, eventID domain.ID, status, errorCode string) {
	t.Helper()
	var row struct {
		Status    string
		ErrorCode string
	}
	if err := db.Raw(`SELECT ap.status, ap.error_code FROM audit_publications ap
		JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.event_id = ?`, eventID).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != status || row.ErrorCode != errorCode {
		t.Fatalf("publication = %s/%s, want %s/%s", row.Status, row.ErrorCode, status, errorCode)
	}
}

// countRows is the T-10.2c row-count helper.
func countRows(t *testing.T, db *gorm.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// TestFirstFactDrainDeliversExactlyOnce (REQ-4, T-10.2c): the first
// Record-produced fact is delivered exactly once and only by the drain
// worker. After Record: exactly one ledger row + one pending publication
// (attempt 0), zero publisher calls, zero callback_deliveries rows (no inline
// loop, no inline enqueue). After the first drainPublications: completed
// settlement, exactly one publish of the matching fact, exactly one dedup
// receipt row, outcome metric [completed]. A second drain tick is a no-op.
func TestFirstFactDrainDeliversExactlyOnce(t *testing.T) {
	db, svc, publisher, metrics, eventID := firstFactDrainHarness(t)
	const pendingQuery = `SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id
		WHERE l.event_id = ? AND ap.status = 'pending' AND ap.attempt = 0`

	// (1) Record performs no governance delivery and no callback enqueue.
	if n := countRows(t, db, `SELECT count(*) FROM audit_ledger WHERE event_id = ?`, eventID); n != 1 {
		t.Fatalf("ledger rows = %d, want 1", n)
	}
	if n := countRows(t, db, pendingQuery, eventID); n != 1 {
		t.Fatalf("pending publication rows = %d, want 1", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM callback_deliveries`); n != 0 {
		t.Fatalf("delivery rows = %d, want 0", n)
	}
	if len(publisher.facts) != 0 {
		t.Fatalf("publisher facts = %d, want 0 (no inline loop)", len(publisher.facts))
	}

	// (2) The drain worker delivers exactly once and settles completed.
	svc.drainPublications(t.Context())
	assertPublicationState(t, db, eventID, "completed", "")
	if len(publisher.facts) != 1 {
		t.Fatalf("publisher facts = %d, want 1", len(publisher.facts))
	}
	fact := publisher.facts[0]
	if fact.EventID != eventID || fact.TenantID != "tenant-one" || fact.Action != "account.provisioned" {
		t.Fatalf("delivered fact = %v/%v/%v, want %v/tenant-one/account.provisioned",
			fact.EventID, fact.TenantID, fact.Action, eventID)
	}
	if n := countRows(t, db, `SELECT count(*) FROM callback_deliveries`); n != 1 {
		t.Fatalf("delivery rows = %d, want 1 (dedup receipt)", n)
	}
	if len(metrics.outcomes) != 1 || metrics.outcomes[0] != "completed" {
		t.Fatalf("outcome observations = %v, want [completed]", metrics.outcomes)
	}

	// (3) A second drain tick is a no-op: no claim, no re-delivery.
	svc.drainPublications(t.Context())
	if len(publisher.facts) != 1 {
		t.Fatalf("publisher facts after second drain = %d, want 1 (exactly-once)", len(publisher.facts))
	}
	assertPublicationState(t, db, eventID, "completed", "")
}

// TestReplayDeliveryInFlightDoubleReplay (F1 / C2): every replay intent is a
// new user intent — it re-applies the reset and records its own fact with a
// FRESH event_id. Replay → in-flight → replay again → success, exactly two
// ledger rows with distinct event_ids, no rollback, no silent no-op; the stale
// worker's MarkDelivery still fails with ErrCallbackDeliveryLeaseLost.
func TestReplayDeliveryInFlightDoubleReplay(t *testing.T) {
	ctx := replayCtx()
	db, svc, repo := startCallbackService(t, "integration-callback-svc-key-0123456789abcdef")
	_, delivery := seedInFlightDelivery(t, repo)

	// Replay #1: in-flight → pending/manual_replay, lease cleared.
	if err := svc.ReplayDelivery(ctx, delivery.ID); err != nil {
		t.Fatalf("replay #1: %v", err)
	}
	first, err := repo.GetDelivery(ctx, delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "pending" || first.ErrorCode != "manual_replay" || first.LeaseOwner != "" {
		t.Fatalf("delivery after replay #1 = %+v, want pending/manual_replay, lease cleared", first)
	}

	// Re-claim → in-flight again.
	if _, err := repo.FetchDueDeliveries(ctx, 50, "worker-2", 5*time.Minute); err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	// Replay #2: success, delivery re-reset, second fact with a distinct event_id.
	if err := svc.ReplayDelivery(ctx, delivery.ID); err != nil {
		t.Fatalf("replay #2: %v", err)
	}
	second, err := repo.GetDelivery(ctx, delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "pending" || second.ErrorCode != "manual_replay" || second.LeaseOwner != "" {
		t.Fatalf("delivery after replay #2 = %+v, want pending/manual_replay, lease cleared", second)
	}
	var replayRows []struct {
		EventID string
		Action  string
	}
	if err := db.Raw(`SELECT event_id::text, action FROM audit_ledger
		WHERE tenant_id = 'tenant-a' AND action = 'callback.delivery.replayed' AND resource_id = ?
		ORDER BY occurred_at ASC`, delivery.ID.String()).Scan(&replayRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(replayRows) != 2 {
		t.Fatalf("replayed facts = %d, want 2 (one per replay intent)", len(replayRows))
	}
	if replayRows[0].EventID == replayRows[1].EventID {
		t.Fatalf("replay facts share event_id %s — C2 requires a fresh EventID per replay", replayRows[0].EventID)
	}
	var publicationRows int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id
		WHERE l.action = 'callback.delivery.replayed' AND l.resource_id = ?`, delivery.ID.String()).Scan(&publicationRows).Error; err != nil {
		t.Fatal(err)
	}
	if publicationRows != 2 {
		t.Fatalf("publication rows = %d, want 2", publicationRows)
	}

	// Stale worker: the re-set delivery is leased to nobody; the old worker's
	// MarkDelivery must lose the lease (delivery state is not double-applied).
	if err := repo.MarkDelivery(ctx, delivery.ID, "worker-1", "success", 200, "", "", time.Now().UTC()); !errors.Is(err, repository.ErrCallbackDeliveryLeaseLost) {
		t.Fatalf("stale worker MarkDelivery err = %v, want ErrCallbackDeliveryLeaseLost", err)
	}
}

// TestReplayDeliveryRollbackLeavesDeliveryUntouched (C2 companion): with a
// keyless ledger the replay fails with callback.delivery_replay_failed and the
// delivery row is byte-identical — the failed append rolled back the reset.
func TestReplayDeliveryRollbackLeavesDeliveryUntouched(t *testing.T) {
	ctx := replayCtx()
	db, svc, repo := startCallbackService(t, "")
	_, delivery := seedInFlightDelivery(t, repo)
	before, err := repo.GetDelivery(ctx, delivery.ID)
	if err != nil {
		t.Fatal(err)
	}

	err = svc.ReplayDelivery(ctx, delivery.ID)
	if err == nil {
		t.Fatal("replay with keyless ledger must fail")
	}
	after, err := repo.GetDelivery(ctx, delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != before.Status || after.LeaseOwner != before.LeaseOwner || after.ErrorCode != before.ErrorCode ||
		after.Attempt != before.Attempt {
		t.Fatalf("delivery after failed replay = %+v, want byte-identical %+v", after, before)
	}
	var ledgerRows, publicationRows int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE tenant_id = 'tenant-a' AND action = 'callback.delivery.replayed'`).Scan(&ledgerRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_publications`).Scan(&publicationRows).Error; err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 0 || publicationRows != 0 {
		t.Fatalf("ledger/publication rows after failed replay = %d/%d, want 0/0", ledgerRows, publicationRows)
	}
}

// drainHarness builds a fully wired callback service for multi-fact drain
// tests: real Postgres, real publication repository, recording publisher,
// metricSpy, and one enabled/verified subscription for (tenant, action) so
// enqueueFact materializes exactly one delivery row per distinct event.
func drainHarness(t *testing.T, tenant, action string) (*gorm.DB, *callbackService, *recordingAuditPublisher, *metricSpy, repository.AuditPublicationRepository) {
	t.Helper()
	ctx := t.Context()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-audit-ledger-key-0123456789abcdef"})
	callbackRepo := repository.NewCallbackRepository(db, ledger)
	svc := NewCallbackService(callbackRepo, CallbackConfig{
		MasterKey: "integration-master-key-0123456789abcdef", RequestTimeout: 2 * time.Second,
		MaxResponseBytes: 4096, MaxAttempts: 8, RetryBase: time.Second, WorkerConcurrency: 4,
	}).(*callbackService)
	pubRepo, ok := ledger.(repository.AuditPublicationRepository)
	if !ok {
		t.Fatal("ledger repo must implement AuditPublicationRepository")
	}
	svc.SetAuditPublicationRepository(pubRepo)
	publisher := &recordingAuditPublisher{}
	svc.SetAuditPublishers(publisher)
	metrics := &metricSpy{}
	svc.metrics = metrics
	if err := callbackRepo.CreateSubscription(ctx, &domain.CallbackSubscription{
		ID: idgen.NewID(), OwnerUID: "u-1", TenantID: tenant,
		Endpoint: "https://cb.example.test/hook", EventTypes: []string{action},
		Enabled: true, Verified: true,
	}); err != nil {
		t.Fatal(err)
	}
	return db, svc, publisher, metrics, pubRepo
}

// appendDrainFact appends one pending publication row for a fresh event.
func appendDrainFact(t *testing.T, ledger repository.AuditLedgerRepository, tenant, action string) *domain.AuditFact {
	t.Helper()
	fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "drain",
		TenantID: tenant, EventID: idgen.NewID(), Action: action, OccurredAt: time.Now().UTC(),
		Payload: map[string]any{}}
	if err := ledger.Append(t.Context(), fact); err != nil {
		t.Fatal(err)
	}
	return fact
}

// publicationState reads the full claim/finish state of one publication row.
func publicationState(t *testing.T, db *gorm.DB, eventID domain.ID) (status, leaseOwner string, attempt int) {
	t.Helper()
	var row struct {
		Status     string
		LeaseOwner string
		Attempt    int
	}
	if err := db.Raw(`SELECT ap.status, ap.lease_owner, ap.attempt FROM audit_publications ap
		JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.event_id = ?`, eventID).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.Status, row.LeaseOwner, row.Attempt
}

// installClaimPoisonTrigger raises on the claim transition (pending ->
// delivering) of the publication row for factID — the claim-stage poison-row
// injection (FM-1).
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

// installFinishCompletedTrigger raises on any transition into status='completed'
// — the finish-stage raw-DB-error injection (FM-3).
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

// TestDrainSettlesClaimedBatchInReverseOrder (out-of-order drain settlement,
// batch variant): two rows claimed in one batch are settled in REVERSE claim
// order (as the per-item drain goroutines may). Both settle exactly once each,
// the dedup receipts keep one delivery row per event, a stale finish after
// settlement is fenced, and a second drain is a no-op. Settlement order must
// not matter: each row's (id, status='delivering', lease_owner) guard makes
// out-of-order settlement safe.
func TestDrainSettlesClaimedBatchInReverseOrder(t *testing.T) {
	ctx := t.Context()
	db, svc, publisher, metrics, pubRepo := drainHarness(t, "tenant-o2o", "test.o2o")
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-audit-ledger-key-0123456789abcdef"})
	first := appendDrainFact(t, ledger, "tenant-o2o", "test.o2o")
	second := appendDrainFact(t, ledger, "tenant-o2o", "test.o2o")

	items, err := pubRepo.ClaimPublication(ctx, svc.worker, 5*time.Minute, 50)
	if err != nil || len(items) != 2 {
		t.Fatalf("claim = %d items, err %v; want 2", len(items), err)
	}
	// Settle the batch in reverse claim order via the real service path.
	svc.finishPublicationAttempt(ctx, items[1])
	svc.finishPublicationAttempt(ctx, items[0])

	assertPublicationState(t, db, first.EventID, "completed", "")
	assertPublicationState(t, db, second.EventID, "completed", "")
	if len(publisher.facts) != 2 {
		t.Fatalf("publisher facts = %d, want 2 (one per row, regardless of order)", len(publisher.facts))
	}
	seen := map[string]bool{}
	for _, fact := range publisher.facts {
		seen[fact.EventID.String()] = true
	}
	if !seen[first.EventID.String()] || !seen[second.EventID.String()] {
		t.Fatalf("publisher facts = %v, want both %s and %s exactly once", seen, first.EventID, second.EventID)
	}
	if n := countRows(t, db, `SELECT count(*) FROM callback_deliveries`); n != 2 {
		t.Fatalf("delivery rows = %d, want 2 (one dedup receipt per event)", n)
	}
	if len(metrics.outcomes) != 2 || metrics.outcomes[0] != "completed" || metrics.outcomes[1] != "completed" {
		t.Fatalf("outcome observations = %v, want [completed completed]", metrics.outcomes)
	}
	// A stale finish after both settlements is fenced: rows are no longer
	// delivering, so the guard UPDATE matches zero rows.
	if err := pubRepo.FinishPublication(ctx, items[0].ID, svc.worker, 1, "completed", "", time.Now().UTC()); !errors.Is(err, repository.ErrAuditPublicationLeaseLost) {
		t.Fatalf("stale finish error = %v, want ErrAuditPublicationLeaseLost", err)
	}
	// A second drain tick is a no-op: nothing re-claimed, nothing re-delivered.
	svc.drainPublications(ctx)
	if len(publisher.facts) != 2 {
		t.Fatalf("publisher facts after second drain = %d, want 2 (no re-delivery)", len(publisher.facts))
	}
}

// TestDrainStaleReclaimInterleavesFreshRow (out-of-order drain settlement,
// stale-claim variant): a drain tick runs while an older row is still leased to
// a stalled worker. The lease predicate and SKIP LOCKED skip the stale row, the
// FRESH row is claimed and settled; after the stale lease expires the row is
// re-claimed and settled — settlement order B-then-A, the reverse of creation
// order. The duplicate governance publish of A is legal (at-least-once); the
// dedup receipt keeps one delivery row per event, and the stale worker's finish
// is fenced mid-lease.
func TestDrainStaleReclaimInterleavesFreshRow(t *testing.T) {
	ctx := t.Context()
	db, svc, publisher, metrics, pubRepo := drainHarness(t, "tenant-stale", "test.stale")
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-audit-ledger-key-0123456789abcdef"})
	stale := appendDrainFact(t, ledger, "tenant-stale", "test.stale") // A — created first

	// Worker-1 claims A and stalls mid-publish (simulated crash): the row is
	// delivering with a live lease; the attempt is untouched (M-4: claim never
	// increments — the counter moves only at settlement).
	aItems, err := pubRepo.ClaimPublication(ctx, "worker-1", 5*time.Minute, 50)
	if err != nil || len(aItems) != 1 || aItems[0].Fact.EventID != stale.EventID {
		t.Fatalf("stale claim = %d items (err %v), want exactly row A", len(aItems), err)
	}

	fresh := appendDrainFact(t, ledger, "tenant-stale", "test.stale") // B — created second

	// The interleaved drain skips A (live lease) and settles only the fresh
	// row B. A is untouched: still worker-1's lease, attempt 0, delivering.
	svc.drainPublications(ctx)
	assertPublicationState(t, db, fresh.EventID, "completed", "")
	if status, owner, attempt := publicationState(t, db, stale.EventID); status != "delivering" || owner != "worker-1" || attempt != 0 {
		t.Fatalf("row A after interleaved drain = %s/owner %q/attempt %d, want delivering/worker-1/0", status, owner, attempt)
	}
	if len(publisher.facts) != 1 || publisher.facts[0].EventID != fresh.EventID {
		t.Fatalf("publisher facts = %v, want only the fresh row B", publisher.facts)
	}
	if len(metrics.outcomes) != 1 || metrics.outcomes[0] != "completed" {
		t.Fatalf("outcome observations = %v, want [completed] for B only", metrics.outcomes)
	}

	// A's lease expires; the drain re-claims it (attempt 2, new owner) and
	// settles it. Settlement order: B before A — the reverse of creation order.
	if err := db.Exec(`UPDATE audit_publications SET lease_until = now() - interval '1 minute'
		WHERE fact_id = (SELECT id FROM audit_ledger WHERE event_id = ?)`, stale.EventID).Error; err != nil {
		t.Fatal(err)
	}
	aItems, err = pubRepo.ClaimPublication(ctx, svc.worker, 5*time.Minute, 50)
	if err != nil || len(aItems) != 1 {
		t.Fatalf("re-claim of A = %d items, err %v; want 1", len(aItems), err)
	}
	if aItems[0].Attempt != 0 || aItems[0].LeaseOwner != svc.worker {
		t.Fatalf("re-claim state = attempt %d owner %s, want 0/%s (claim never increments)", aItems[0].Attempt, aItems[0].LeaseOwner, svc.worker)
	}
	// The stale worker's finish is fenced while the new owner holds the lease.
	if err := pubRepo.FinishPublication(ctx, aItems[0].ID, "worker-1", 1, "completed", "", time.Now().UTC()); !errors.Is(err, repository.ErrAuditPublicationLeaseLost) {
		t.Fatalf("stale worker finish = %v, want ErrAuditPublicationLeaseLost", err)
	}
	svc.finishPublicationAttempt(ctx, aItems[0])
	assertPublicationState(t, db, stale.EventID, "completed", "")
	if len(publisher.facts) != 2 || publisher.facts[0].EventID != fresh.EventID || publisher.facts[1].EventID != stale.EventID {
		t.Fatalf("publisher facts = %v, want [B A] (settlement order, reverse of creation order)", publisher.facts)
	}
	if n := countRows(t, db, `SELECT count(*) FROM callback_deliveries`); n != 2 {
		t.Fatalf("delivery rows = %d, want 2 (one dedup receipt per event, duplicate publish of A folded)", n)
	}
	if len(metrics.outcomes) != 2 || metrics.outcomes[0] != "completed" || metrics.outcomes[1] != "completed" {
		t.Fatalf("outcome observations = %v, want [completed completed]", metrics.outcomes)
	}
}

// TestDrainClaimPoisonRowObservesMetricRecovers (FM-1): a row that errors
// inside ClaimPublication fails the whole claim transaction and rolls it back
// atomically — no partial claim, no row stranded delivering — and the drain
// tick observes the failure on the claim-failed metric (phase=claim_tx)
// instead of returning silently. The poison row stalls the batch (the healthy
// row in the same claim tx is not claimed either) until removed; the next
// drain then claims and settles both rows.
func TestDrainClaimPoisonRowObservesMetricRecovers(t *testing.T) {
	ctx := t.Context()
	db, svc, publisher, metrics, _ := drainHarness(t, "tenant-poison", "test.poison")
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-audit-ledger-key-0123456789abcdef"})
	poison := appendDrainFact(t, ledger, "tenant-poison", "test.poison")
	healthy := appendDrainFact(t, ledger, "tenant-poison", "test.poison")
	installClaimPoisonTrigger(t, db, poison.ID)

	svc.drainPublications(ctx)
	if len(metrics.claimFailures) != 1 || metrics.claimFailures[0].phase != "claim_tx" {
		t.Fatalf("claim failures = %+v, want exactly one with phase claim_tx (never silent)", metrics.claimFailures)
	}
	if !strings.Contains(metrics.claimFailures[0].err.Error(), "injected claim poison") {
		t.Fatalf("claim failure error = %v, want the raw injected trigger error", metrics.claimFailures[0].err)
	}
	if len(metrics.outcomes) != 0 || len(metrics.finishFailures) != 0 {
		t.Fatalf("outcomes/finish failures = %v/%v, want none (nothing was claimed)", metrics.outcomes, metrics.finishFailures)
	}
	if len(publisher.facts) != 0 {
		t.Fatalf("publisher facts = %d, want 0 (claim failed before any delivery)", len(publisher.facts))
	}
	if n := countRows(t, db, `SELECT count(*) FROM callback_deliveries`); n != 0 {
		t.Fatalf("delivery rows = %d, want 0", n)
	}
	// Atomic rollback: both rows are still pending, attempt 0, unleased.
	for _, fact := range []*domain.AuditFact{poison, healthy} {
		if status, owner, attempt := publicationState(t, db, fact.EventID); status != "pending" || owner != "" || attempt != 0 {
			t.Fatalf("row for %s after failed claim = %s/owner %q/attempt %d, want pending/\"\"/0",
				fact.EventID, status, owner, attempt)
		}
	}

	// Recovery: poison removed, the next drain claims and settles both rows.
	_ = db.Exec(`DROP TRIGGER IF EXISTS fail_claim_poison_trigger ON audit_publications`).Error
	_ = db.Exec(`DROP FUNCTION IF EXISTS fail_claim_poison()`).Error
	svc.drainPublications(ctx)
	assertPublicationState(t, db, poison.EventID, "completed", "")
	assertPublicationState(t, db, healthy.EventID, "completed", "")
	if len(publisher.facts) != 2 {
		t.Fatalf("publisher facts after recovery = %d, want 2 (both rows delivered)", len(publisher.facts))
	}
	if len(metrics.outcomes) != 2 || metrics.outcomes[0] != "completed" || metrics.outcomes[1] != "completed" {
		t.Fatalf("outcome observations after recovery = %v, want [completed completed]", metrics.outcomes)
	}
}

// TestFinishPublicationRawDBErrorNotCountedUntilCommitted (FM-3 at integration
// level): a raw DB error on the finish UPDATE (injected trigger) is observed on
// the finish-failure metric with the DECIDED status, and the settlement is NOT
// counted — the row stays delivering with its lease, so the decision is re-made
// after lease-expiry re-claim. Only the later committed finish counts on the
// outcome metric. Exactly one publish per attempt (no inline retry), one dedup
// receipt row across both attempts.
func TestFinishPublicationRawDBErrorNotCountedUntilCommitted(t *testing.T) {
	db, svc, publisher, metrics, eventID := firstFactDrainHarness(t)
	installFinishCompletedTrigger(t, db)

	// Drain #1: claim and publish succeed; the finish UPDATE fails with the
	// raw injected error. Failure observed, settlement not counted.
	svc.drainPublications(t.Context())
	if len(metrics.finishFailures) != 1 || metrics.finishFailures[0] != "completed" {
		t.Fatalf("finish failures observed = %v, want [completed] (decided status)", metrics.finishFailures)
	}
	if len(metrics.finishFailureErrs) != 1 || errors.Is(metrics.finishFailureErrs[0], repository.ErrAuditPublicationLeaseLost) ||
		!strings.Contains(metrics.finishFailureErrs[0].Error(), "injected finish failure") {
		t.Fatalf("finish failure error = %v, want the raw DB error, not lease-lost", metrics.finishFailureErrs)
	}
	if len(metrics.outcomes) != 0 {
		t.Fatalf("outcomes observed = %v, want none (FM-3: not counted until committed)", metrics.outcomes)
	}
	if len(publisher.facts) != 1 {
		t.Fatalf("publisher facts = %d, want 1 (exactly one publish per attempt, no inline retry)", len(publisher.facts))
	}
	if status, owner, attempt := publicationState(t, db, eventID); status != "delivering" || owner != svc.worker || attempt != 0 {
		t.Fatalf("row after failed finish = %s/owner %q/attempt %d, want delivering/%s/0 (M-4: the increment rides in the rolled-back finish UPDATE)", status, owner, attempt, svc.worker)
	}

	// Drain #2 after the poison is removed and the lease expired: re-claim,
	// re-publish, finish commits — and only now is the outcome counted.
	_ = db.Exec(`DROP TRIGGER IF EXISTS fail_completed_finish_trigger ON audit_publications`).Error
	_ = db.Exec(`DROP FUNCTION IF EXISTS fail_completed_finish()`).Error
	if err := db.Exec(`UPDATE audit_publications SET lease_until = now() - interval '1 minute'
		WHERE fact_id = (SELECT id FROM audit_ledger WHERE event_id = ?)`, eventID).Error; err != nil {
		t.Fatal(err)
	}
	svc.drainPublications(t.Context())
	assertPublicationState(t, db, eventID, "completed", "")
	if len(publisher.facts) != 2 {
		t.Fatalf("publisher facts after re-claim = %d, want 2 (one per attempt)", len(publisher.facts))
	}
	if len(metrics.outcomes) != 1 || metrics.outcomes[0] != "completed" {
		t.Fatalf("outcome observations = %v, want [completed] counted only at commit", metrics.outcomes)
	}
	if n := countRows(t, db, `SELECT count(*) FROM callback_deliveries`); n != 1 {
		t.Fatalf("delivery rows = %d, want 1 (dedup receipt across both attempts)", n)
	}
}

// TestRequeuedPublicationDrainsExactlyOnce (T-4.1(b)): after RequeuePublication
// moves a dead-letter row back to pending, one drainPublications cycle claims,
// delivers to the governance publisher exactly once, and settles completed
// with the lease cleared; a second drain is a no-op.
func TestRequeuedPublicationDrainsExactlyOnce(t *testing.T) {
	ctx := t.Context()
	db, svc, publisher, metrics, eventID := firstFactDrainHarness(t)

	// Drive the seeded pending publication to dead_letter through the public
	// surface: claim + finish dead_letter.
	claimed, err := svc.publications.ClaimPublication(ctx, "drain-worker", 5*time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if err := svc.publications.FinishPublication(ctx, claimed[0].ID, "drain-worker", 1,
		"dead_letter", "audit.publication_failed", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertPublicationState(t, db, eventID, "dead_letter", "audit.publication_failed")

	// Operator recovery: requeue with the intent surface (AUD-1 tenant-scoped
	// fact derived from the rows).
	operationID := idgen.NewID()
	requeued, facts, err := svc.publications.RequeuePublication(ctx, nil, 10, repository.ReplayIntent{
		OperationID: operationID, ActorUID: "ops-bot",
		Envelope: domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "requeue",
			OperationID: operationID, CorrelationID: operationID, ActorUID: "ops-bot",
			Action: "audit.publication.requeued", ResourceType: "audit_publication",
			OccurredAt: time.Now().UTC(), Payload: map[string]any{}},
	})
	if err != nil || requeued != 1 {
		t.Fatalf("requeue = %d, %v; want 1, nil", requeued, err)
	}
	if len(facts) != 1 {
		t.Fatalf("requeue facts = %d, want 1", len(facts))
	}
	assertPublicationState(t, db, eventID, "pending", "manual_replay")
	if n := countRows(t, db, `SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`); n != 1 {
		t.Fatalf("requeue fact rows = %d, want 1 (same tx)", n)
	}

	// Drain once: the original fact is published exactly once and settles
	// completed. The requeue fact (audit.publication.requeued) is a pending
	// publication of its own and is delivered too — count per event ID.
	svc.drainPublications(ctx)
	assertPublicationState(t, db, eventID, "completed", "")
	originalPublishes := 0
	requeueFactPublishes := 0
	for _, fact := range publisher.facts {
		if fact.EventID.String() == eventID.String() {
			originalPublishes++
		}
		if fact.Action == "audit.publication.requeued" {
			requeueFactPublishes++
		}
	}
	if originalPublishes != 1 {
		t.Fatalf("original fact publishes = %d, want exactly 1", originalPublishes)
	}
	if requeueFactPublishes != 1 {
		t.Fatalf("requeue fact publishes = %d, want exactly 1 (it drains to governance too)", requeueFactPublishes)
	}
	if len(metrics.outcomes) != 2 || metrics.outcomes[0] != "completed" || metrics.outcomes[1] != "completed" {
		t.Fatalf("outcome observations = %v, want [completed completed]", metrics.outcomes)
	}
	// Only the original fact's action matches the seeded subscription
	// (account.provisioned); the requeue fact (audit.publication.requeued) has
	// no matching subscription, so exactly one delivery receipt exists.
	if n := countRows(t, db, `SELECT count(*) FROM callback_deliveries`); n != 1 {
		t.Fatalf("delivery rows = %d, want 1 (dedup receipt for the original fact)", n)
	}

	// Second drain: no claims, no republishes, statuses stay completed.
	svc.drainPublications(ctx)
	after := 0
	for _, fact := range publisher.facts {
		if fact.EventID.String() == eventID.String() {
			after++
		}
	}
	if after != 1 {
		t.Fatalf("original fact publishes after second drain = %d, want 1 (exactly-once)", after)
	}
	assertPublicationState(t, db, eventID, "completed", "")
}

// TestPurgeScanSkipsWhenGovernanceDisabled (T-4.3 drain-loop leg): with no
// positive retention the purge scan never touches the repositories; with
// purge_pause set it is skipped even when retention is configured.
func TestPurgeScanSkipsWhenGovernanceDisabled(t *testing.T) {
	ctx := t.Context()
	db, svc, _, _, eventID := firstFactDrainHarness(t)
	if err := db.Exec(`UPDATE audit_publications SET status = 'dead_letter', updated_at = now() - interval '10 days'`).Error; err != nil {
		t.Fatal(err)
	}

	// Retention 0 (default): scan is a no-op; rows survive.
	svc.purgeDeadLetters(ctx)
	var dead int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'dead_letter'`).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 1 {
		t.Fatalf("dead-letter rows with retention 0 = %d, want 1 (never purge by default)", dead)
	}

	// purge_pause: scan skipped even with a positive retention.
	svc.cfg.DeadLetterRetention = 24 * time.Hour
	svc.cfg.PurgePause = true
	svc.purgeDeadLetters(ctx)
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'dead_letter'`).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 1 {
		t.Fatalf("dead-letter rows while paused = %d, want 1 (preserved bit-for-bit)", dead)
	}

	// Unpaused with retention: past-retention dead letters are purged.
	svc.cfg.PurgePause = false
	svc.purgeDeadLetters(ctx)
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'dead_letter'`).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 0 {
		t.Fatalf("dead-letter rows after unpaused scan = %d, want 0", dead)
	}
	// The ledger fact survives the publication purge.
	if n := countRows(t, db, `SELECT count(*) FROM audit_ledger WHERE event_id = ?`, eventID); n != 1 {
		t.Fatalf("ledger facts after purge = %d, want 1", n)
	}
}
