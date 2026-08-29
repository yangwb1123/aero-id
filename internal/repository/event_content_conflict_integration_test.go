//go:build integration

package repository_test

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

const eventContentSigningKey = "event-content-integration-key-0123456789abcdef"

func newEventContentHarness(t *testing.T) (*gorm.DB, domain.Account, repository.EventStoreRepository, repository.EventReceiptRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: eventContentSigningKey})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "event-content", "event-content", "local")
	if err != nil {
		t.Fatal(err)
	}
	store := repository.NewEventStoreRepository(db)
	receipts := repository.NewEventReceiptRepositoryWithStore(db, store, ledger)
	return db, *account, store, receipts
}

func countEventContentRows(t *testing.T, db *gorm.DB, table string, envelope *event.Envelope) int64 {
	t.Helper()
	var count int64
	query := "SELECT count(*) FROM event_receipts WHERE tenant_id = ? AND source = ? AND event_id = ?"
	if table == "event_store" {
		query = "SELECT count(*) FROM event_store WHERE tenant_id = ? AND source = ? AND event_id = ?"
	}
	if err := db.Raw(query, envelope.TenantID, envelope.Source, envelope.EventID).Scan(&count).Error; err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}
	return count
}

func eventContentAuditFact(envelope *event.Envelope) *domain.AuditFact {
	return &domain.AuditFact{EventID: envelope.EventID, Source: envelope.Source, SourceRegion: envelope.SourceRegion,
		PartitionKey: "event-content", TenantID: envelope.TenantID, ActorUID: envelope.ActorUID,
		Action: "source.event", ResourceType: envelope.AggregateType, ResourceID: envelope.AggregateID,
		OccurredAt: envelope.OccurredAt, Payload: map[string]any{"kind": "event-content-test"}}
}

func TestEventContentExactDuplicateAndChangedEnvelopeHaveNoExtraEffects(t *testing.T) {
	db, account, store, receipts := newEventContentHarness(t)
	envelope := eventStoreEnvelope(account.ID, "aero-vault.buckets", 1, time.Now().UTC())
	envelope.EventID = idgen.NewID()
	mutation, err := envelope.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	audited := receipts.(repository.EventSnapshotAuditRepository)
	if result, err := audited.ApplySnapshotEventWithAudit(t.Context(), envelope, mutation, eventContentAuditFact(envelope)); err != nil || result.Status != repository.EventApplied {
		t.Fatalf("first apply = %v, %v; want applied", result, err)
	}
	if result, err := audited.ApplySnapshotEventWithAudit(t.Context(), envelope, mutation, eventContentAuditFact(envelope)); err != nil || result.Status != repository.EventDuplicate {
		t.Fatalf("exact duplicate = %v, %v; want duplicate", result, err)
	}

	changed := *envelope
	changed.Payload = cloneEventContentPayload(envelope.Payload)
	changed.Payload["data"] = map[string]any{"usage_bytes": float64(999)}
	changedMutation, err := changed.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audited.ApplySnapshotEventWithAudit(t.Context(), &changed, changedMutation, eventContentAuditFact(&changed)); !errors.Is(err, repository.ErrEventIntegrityConflict) {
		t.Fatalf("changed payload error = %v, want integrity conflict", err)
	}

	metadata := *envelope
	metadata.SourceRegion = "eu-west"
	if _, err := audited.ApplySnapshotEventWithAudit(t.Context(), &metadata, mutation, eventContentAuditFact(&metadata)); !errors.Is(err, repository.ErrEventIntegrityConflict) {
		t.Fatalf("changed metadata error = %v, want integrity conflict", err)
	}
	if countEventContentRows(t, db, "event_receipts", envelope) != 1 || countEventContentRows(t, db, "event_store", envelope) != 1 {
		t.Fatal("duplicate or conflict created an extra receipt or event-store row")
	}
	if countAuditLedgerRows(t, db, envelope.EventID) != 1 {
		t.Fatal("duplicate or conflict created an extra audit fact")
	}
	stored, _, err := store.ListByAccount(t.Context(), envelope.TenantID, envelope.Source, account.ID.String(), domain.ID{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var usage float64
	dataOK, usageOK := false, false
	if len(stored) == 1 {
		data, ok := stored[0].Payload["data"].(map[string]any)
		dataOK = ok
		if ok {
			usage, usageOK = numericValue(data["usage_bytes"])
		}
	}
	if len(stored) != 1 || !dataOK || !usageOK || usage != 10 {
		t.Fatalf("projection/event store changed after conflict: %#v", stored)
	}
}

func TestEventStoreDirectAppendRejectsChangedContentAndStoresOnlyDigest(t *testing.T) {
	db, account, store, _ := newEventContentHarness(t)
	envelope := eventStoreEnvelope(account.ID, "aero-vault.buckets", 1, time.Now().UTC())
	if err := store.Append(t.Context(), envelope); err != nil {
		t.Fatal(err)
	}
	changed := *envelope
	changed.Payload = cloneEventContentPayload(envelope.Payload)
	changed.Payload["data"] = map[string]any{"usage_bytes": float64(1234)}
	if err := store.Append(t.Context(), &changed); !errors.Is(err, repository.ErrEventIntegrityConflict) {
		t.Fatalf("direct append conflict = %v, want integrity conflict", err)
	}
	var hash string
	var payload string
	if err := db.Raw("SELECT content_hash, payload::text FROM event_store WHERE tenant_id = ? AND source = ? AND event_id = ?",
		envelope.TenantID, envelope.Source, envelope.EventID).Row().Scan(&hash, &payload); err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 || strings.Contains(hash, "redact-me") || strings.Contains(hash, "secret") {
		t.Fatalf("stored digest is invalid or contains source content: %q", hash)
	}
	if strings.Contains(payload, "redact-me") {
		t.Fatalf("stored event payload leaked a sensitive value: %s", payload)
	}
	if countEventContentRows(t, db, "event_store", envelope) != 1 {
		t.Fatal("direct conflict changed event-store cardinality")
	}
}

func TestEventContentConcurrentDifferentWritersHaveOneWinner(t *testing.T) {
	db, account, _, receipts := newEventContentHarness(t)
	base := eventStoreEnvelope(account.ID, "aero-vault.buckets", 1, time.Now().UTC())
	base.EventID = idgen.NewID()
	left := *base
	left.Payload = cloneEventContentPayload(base.Payload)
	left.Payload["data"] = map[string]any{"usage_bytes": float64(10)}
	right := *base
	right.Payload = cloneEventContentPayload(base.Payload)
	right.Payload["data"] = map[string]any{"usage_bytes": float64(20)}
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, candidate := range []*event.Envelope{&left, &right} {
		group.Add(1)
		go func(value *event.Envelope) {
			defer group.Done()
			<-start
			mutation, err := value.Snapshot()
			if err == nil {
				_, err = receipts.ApplySnapshotEvent(t.Context(), value, mutation)
			}
			results <- err
		}(candidate)
	}
	close(start)
	group.Wait()
	close(results)
	accepted, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, repository.ErrEventIntegrityConflict), errors.Is(err, repository.ErrEventReceiptBusy):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatalf("concurrent results = accepted %d, conflicts %d; want one each", accepted, conflicts)
	}
	if countEventContentRows(t, db, "event_receipts", base) != 1 || countEventContentRows(t, db, "event_store", base) != 1 {
		t.Fatal("concurrent writers created more than one durable event binding")
	}
}

func TestEventContentLegacyRowsFailClosed(t *testing.T) {
	db, account, store, receipts := newEventContentHarness(t)
	envelope := eventStoreEnvelope(account.ID, "aero-vault.buckets", 1, time.Now().UTC())
	mutation, err := envelope.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receipts.ApplySnapshotEvent(t.Context(), envelope, mutation); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE event_receipts SET content_hash = '' WHERE tenant_id = ? AND source = ? AND event_id = ?",
		envelope.TenantID, envelope.Source, envelope.EventID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := receipts.ApplySnapshotEvent(t.Context(), envelope, mutation); !errors.Is(err, repository.ErrEventIntegrityConflict) {
		t.Fatalf("legacy receipt replay = %v, want integrity conflict", err)
	}
	if err := db.Exec("UPDATE event_store SET content_hash = '' WHERE tenant_id = ? AND source = ? AND event_id = ?",
		envelope.TenantID, envelope.Source, envelope.EventID).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Append(t.Context(), envelope); !errors.Is(err, repository.ErrEventIntegrityConflict) {
		t.Fatalf("legacy event-store replay = %v, want integrity conflict", err)
	}
}

func TestEventContentBindingIsSharedByReceiptAndEventStore(t *testing.T) {
	db, _, store, receipts := newEventContentHarness(t)
	envelope := eventStoreEnvelope(idgen.NewID(), "aero-vault.buckets", 1, time.Now().UTC())
	if err := receipts.QuarantineEvent(t.Context(), envelope, "event.invalid_projection"); err != nil {
		t.Fatal(err)
	}
	changed := *envelope
	changed.Payload = cloneEventContentPayload(envelope.Payload)
	changed.Payload["data"] = map[string]any{"usage_bytes": float64(88)}
	if err := store.Append(t.Context(), &changed); !errors.Is(err, repository.ErrEventIntegrityConflict) {
		t.Fatalf("store append after receipt conflict = %v, want integrity conflict", err)
	}
	if countEventContentRows(t, db, "event_store", envelope) != 0 {
		t.Fatal("conflicting store append created a row for a quarantined event")
	}

	other := eventStoreEnvelope(idgen.NewID(), "aero-vault.buckets", 1, time.Now().UTC())
	if err := store.Append(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	otherChanged := *other
	otherChanged.SourceRegion = "eu-west"
	if err := receipts.QuarantineEvent(t.Context(), &otherChanged, "event.invalid_projection"); !errors.Is(err, repository.ErrEventIntegrityConflict) {
		t.Fatalf("receipt quarantine after store conflict = %v, want integrity conflict", err)
	}
	if countEventContentRows(t, db, "event_receipts", other) != 0 {
		t.Fatal("conflicting receipt quarantine created a row for an event-store binding")
	}
}

func TestEventContentQuarantineRedeliveryAndConflict(t *testing.T) {
	db, svc := newIngestQuarantineHarness(t)
	envelope := eventStoreEnvelope(idgen.NewID(), "aero-vault.buckets", 2, time.Now().UTC())
	envelope.SourceRegion = "cn-north"
	if _, err := svc.Ingest(t.Context(), envelope); err == nil {
		t.Fatal("first disallowed event must be quarantined and rejected")
	}
	if _, err := svc.Ingest(t.Context(), envelope); err == nil {
		t.Fatal("exact quarantine redelivery must retain its deterministic rejection")
	}
	changed := *envelope
	changed.Payload = cloneEventContentPayload(envelope.Payload)
	changed.Payload["data"] = map[string]any{"usage_bytes": float64(777)}
	_, err := svc.Ingest(t.Context(), &changed)
	ae := requireIngestAppError(t, err)
	if ae.Code != "event.integrity_conflict" || ae.HTTPStatus() != 409 {
		t.Fatalf("changed quarantine error = %s (%d), want event.integrity_conflict (409)", ae.Code, ae.HTTPStatus())
	}
	if countQuarantineReceiptRows(t, db, envelope) != 1 || countAuditLedgerRows(t, db, envelope.EventID) != 1 {
		t.Fatal("quarantine redelivery or conflict created an extra receipt or audit fact")
	}
}

func TestEventContentDirectStoreAndReceiptWritersAreSerialized(t *testing.T) {
	db, account, store, receipts := newEventContentHarness(t)
	base := eventStoreEnvelope(account.ID, "aero-vault.buckets", 1, time.Now().UTC())
	base.EventID = idgen.NewID()
	storeEnvelope := *base
	storeEnvelope.Payload = cloneEventContentPayload(base.Payload)
	storeEnvelope.Payload["data"] = map[string]any{"usage_bytes": float64(10)}
	receiptEnvelope := *base
	receiptEnvelope.Payload = cloneEventContentPayload(base.Payload)
	receiptEnvelope.Payload["data"] = map[string]any{"usage_bytes": float64(20)}
	mutation, err := receiptEnvelope.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		results <- store.Append(t.Context(), &storeEnvelope)
	}()
	go func() {
		defer group.Done()
		<-start
		_, applyErr := receipts.ApplySnapshotEvent(t.Context(), &receiptEnvelope, mutation)
		results <- applyErr
	}()
	close(start)
	group.Wait()
	close(results)
	accepted, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, repository.ErrEventIntegrityConflict):
			conflicts++
		default:
			t.Fatalf("unexpected cross-path result: %v", err)
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatalf("cross-path results = accepted %d, conflicts %d; want one each", accepted, conflicts)
	}
	if countEventContentRows(t, db, "event_receipts", base) > 1 || countEventContentRows(t, db, "event_store", base) > 1 {
		t.Fatal("cross-path writers created duplicate durable rows")
	}
}

func cloneEventContentPayload(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

type eventContentUnserializable struct{ secret string }

func (value eventContentUnserializable) MarshalJSON() ([]byte, error) {
	return nil, errors.New("cannot encode " + value.secret)
}

func TestEventContentDigestFailureIsNonSensitive(t *testing.T) {
	db, account, store, _ := newEventContentHarness(t)
	envelope := eventStoreEnvelope(account.ID, "aero-vault.buckets", 1, time.Now().UTC())
	const secret = "digest-error-secret"
	envelope.Payload["bad"] = eventContentUnserializable{secret: secret}
	err := store.Append(t.Context(), envelope)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("digest error = %v, leaked or missing expected non-sensitive error", err)
	}
	if countEventContentRows(t, db, "event_store", envelope) != 0 {
		t.Fatal("digest failure created an event-store row")
	}
}
