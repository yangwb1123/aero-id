//go:build integration

package repository_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func eventStoreEnvelope(accountID domain.ID, dataset string, version int64, occurred time.Time) *event.Envelope {
	return &event.Envelope{
		EventID: idgen.NewID(), Source: "aero-vault", SourceRegion: "us-west",
		EventType: "vault.usage.updated", SchemaVersion: 1, AggregateType: "object",
		AggregateID: "obj-1", AggregateVersion: version, TenantID: "tenant-a",
		OperationID: idgen.NewID(), CorrelationID: idgen.NewID(), CausationID: idgen.NewID(),
		TraceID: "trace-1", ActorUID: "u-1", OccurredAt: occurred,
		Payload: map[string]any{
			"account_id": accountID.String(), "dataset": dataset, "status": "fresh",
			"version": float64(version), "data": map[string]any{"usage_bytes": version * 10},
			"secret": "redact-me", // must not survive redaction
		},
	}
}

func TestEventStoreAppendAndQuery(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	store := repository.NewEventStoreRepository(db)
	accountID := idgen.NewID()
	now := time.Now().UTC()

	v1 := eventStoreEnvelope(accountID, "aero-vault.buckets", 1, now)
	if err := store.Append(t.Context(), v1); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Duplicate append of the SAME envelope is a no-op (idempotent on event_id).
	if err := store.Append(t.Context(), v1); err != nil {
		t.Fatalf("duplicate append: %v", err)
	}
	if err := store.Append(t.Context(), v1); err != nil {
		t.Fatalf("second duplicate append: %v", err)
	}
	if err := store.Append(t.Context(), eventStoreEnvelope(accountID, "aero-vault.buckets", 2, now.Add(time.Minute))); err != nil {
		t.Fatalf("append v2: %v", err)
	}
	// Another account's events are isolated.
	other := idgen.NewID()
	if err := store.Append(t.Context(), eventStoreEnvelope(other, "aero-vault.buckets", 1, now)); err != nil {
		t.Fatalf("append other account: %v", err)
	}

	envelopes, next, err := store.ListByAccount(t.Context(), "tenant-a", "aero-vault", accountID.String(), domain.ID{}, 100)
	if err != nil {
		t.Fatalf("list by account: %v", err)
	}
	if len(envelopes) != 2 {
		t.Fatalf("account events = %d, want 2 (duplicate deduped)", len(envelopes))
	}
	if next != (domain.ID{}) {
		t.Errorf("next cursor = %v, want nil", next)
	}
	// Newest first (id DESC).
	if envelopes[0].AggregateVersion != 2 || envelopes[1].AggregateVersion != 1 {
		t.Errorf("order = %d,%d want 2,1", envelopes[0].AggregateVersion, envelopes[1].AggregateVersion)
	}
	if envelopes[0].TraceID != "trace-1" || envelopes[0].ActorUID != "u-1" {
		t.Errorf("envelope context fields lost: %+v", envelopes[0])
	}
	if envelopes[0].Payload["secret"] != nil {
		t.Error("payload was not redacted on storage")
	}
	if envelopes[0].Payload["account_id"] != accountID.String() {
		t.Errorf("account_id not preserved in redacted payload: %v", envelopes[0].Payload)
	}

	// Aggregate-scoped query.
	byAgg, err := store.ListByAggregate(t.Context(), "tenant-a", "aero-vault", "object", "obj-1", 2, 100)
	if err != nil {
		t.Fatalf("list by aggregate: %v", err)
	}
	if len(byAgg) != 1 || byAgg[0].AggregateVersion != 2 {
		t.Errorf("aggregate query = %+v, want [v2]", byAgg)
	}

	// Tenant isolation.
	otherTenant, _, err := store.ListByAccount(t.Context(), "tenant-b", "aero-vault", accountID.String(), domain.ID{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherTenant) != 0 {
		t.Errorf("cross-tenant events visible: %d", len(otherTenant))
	}
}

// TestEventStoreAtomicWithReceipt verifies an applied event lands in the store
// atomically with its receipt through the ingest path (P3-07).
func TestEventStoreAtomicWithReceipt(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "u-replay", "u-replay", "local")
	if err != nil {
		t.Fatal(err)
	}
	store := repository.NewEventStoreRepository(db)
	receipts := repository.NewEventReceiptRepositoryWithStore(db, store, ledger)
	envelope := eventStoreEnvelope(account.ID, "aero-vault.buckets", 1, time.Now().UTC())
	mutation, err := envelope.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	result, err := receipts.ApplySnapshotEvent(t.Context(), envelope, mutation)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if string(result.Status) != "applied" {
		t.Fatalf("status = %s, want applied", result.Status)
	}
	stored, _, err := store.ListByAccount(t.Context(), "tenant-a", "aero-vault", account.ID.String(), domain.ID{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored events = %d, want 1", len(stored))
	}
	// Duplicate ingest must not create a second stored event.
	if _, err := receipts.ApplySnapshotEvent(t.Context(), envelope, mutation); err != nil {
		t.Fatal(err)
	}
	stored, _, err = store.ListByAccount(t.Context(), "tenant-a", "aero-vault", account.ID.String(), domain.ID{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored events after duplicate = %d, want 1", len(stored))
	}
}
