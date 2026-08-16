package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

// invalidProjectionEnvelope passes envelope.Validate() (payload non-nil,
// valid source/region/type/aggregate) but fails Snapshot(): the required
// payload.account_id field is missing.
func invalidProjectionEnvelope() *event.Envelope {
	envelope := testEnvelope("us-west")
	delete(envelope.Payload, "account_id")
	return envelope
}

// quarantineAuditAppendSpy records every ledger append attempt and folds a
// duplicate (tenant, source, event_id) triple with ErrAuditDuplicate, exactly
// like the real ledger's uq_audit_ledger_tenant_event unique constraint.
type quarantineAuditAppendSpy struct {
	auditLedgerRepoSpy
	facts []*domain.AuditFact
	seen  map[string]bool
}

func (s *quarantineAuditAppendSpy) Append(_ context.Context, fact *domain.AuditFact) error {
	key := fact.TenantID + "|" + fact.Source + "|" + fact.EventID.String()
	s.facts = append(s.facts, fact)
	if s.seen[key] {
		return repository.ErrAuditDuplicate
	}
	s.seen[key] = true
	return nil
}

func requireAppError(t *testing.T, err error) *apperr.AppError {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var ae *apperr.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("error = %v, want *AppError", err)
	}
	return ae
}

// TestEventIngest_RegionQuarantineRecordsAnchoredAuditFact (REQ-1): a
// residency-policy violation quarantine writes exactly one anchored
// 'quarantined' ledger fact (Action "source.event", EventID == envelope
// EventID, payload status "quarantined") and returns the deterministic 403.
func TestEventIngest_RegionQuarantineRecordsAnchoredAuditFact(t *testing.T) {
	receipts := &fakeReceiptRepo{}
	ledger := &quarantineAuditAppendSpy{seen: map[string]bool{}}
	svc := NewEventIngestService(receipts,
		WithEventAllowedRegions([]string{"us-west", "eu-west"}),
		WithEventIngestAudit(NewAuditLedgerService(ledger)))

	envelope := testEnvelope("cn-north")
	_, err := svc.Ingest(context.Background(), envelope)
	ae := requireAppError(t, err)
	if ae.HTTPStatus() != http.StatusForbidden {
		t.Fatalf("HTTPStatus = %d, want 403", ae.HTTPStatus())
	}
	if ae.Code != "event.region_not_allowed" {
		t.Fatalf("code = %q, want event.region_not_allowed", ae.Code)
	}
	if len(receipts.quarantined) != 1 || receipts.quarantined[0] != "event.region_not_allowed" {
		t.Fatalf("quarantine codes = %v, want [event.region_not_allowed]", receipts.quarantined)
	}
	if len(ledger.facts) != 1 {
		t.Fatalf("ledger facts = %d, want exactly 1", len(ledger.facts))
	}
	fact := ledger.facts[0]
	if fact.Action != "source.event" {
		t.Errorf("action = %q, want source.event", fact.Action)
	}
	if fact.EventID != envelope.EventID {
		t.Errorf("event_id = %s, want %s (anchored on the envelope event_id)", fact.EventID, envelope.EventID)
	}
	if fact.Payload["status"] != "quarantined" {
		t.Errorf("payload status = %v, want quarantined", fact.Payload["status"])
	}
	if fact.TenantID != envelope.TenantID || fact.Source != envelope.Source ||
		fact.ResourceType != envelope.AggregateType || fact.ResourceID != envelope.AggregateID {
		t.Errorf("tenant/source/resource = %q/%q/%q/%q", fact.TenantID, fact.Source, fact.ResourceType, fact.ResourceID)
	}
	if len(receipts.applied) != 0 {
		t.Fatalf("event must not be applied, applied = %v", receipts.applied)
	}
}

// TestEventIngest_RegionQuarantineReceiptFailureReturns5xxNoFact (REQ-2): a
// failed durable quarantine receipt write surfaces as a >= 500 AppError
// (event.quarantine_failed -> Nak/redelivery) and records ZERO ledger facts.
func TestEventIngest_RegionQuarantineReceiptFailureReturns5xxNoFact(t *testing.T) {
	receipts := &fakeReceiptRepo{quarantineErr: errors.New("receipt write failed")}
	ledger := &quarantineAuditAppendSpy{seen: map[string]bool{}}
	svc := NewEventIngestService(receipts,
		WithEventAllowedRegions([]string{"us-west"}),
		WithEventIngestAudit(NewAuditLedgerService(ledger)))

	_, err := svc.Ingest(context.Background(), testEnvelope("cn-north"))
	ae := requireAppError(t, err)
	if ae.HTTPStatus() < 500 {
		t.Fatalf("HTTPStatus = %d, want >= 500 (Nak/redelivery)", ae.HTTPStatus())
	}
	if ae.Code != "event.quarantine_failed" {
		t.Fatalf("code = %q, want event.quarantine_failed", ae.Code)
	}
	if len(ledger.facts) != 0 {
		t.Fatalf("ledger facts = %d, want zero (no fact without a durable receipt)", len(ledger.facts))
	}
	if len(receipts.quarantined) != 1 {
		t.Fatalf("quarantine attempts = %d, want 1", len(receipts.quarantined))
	}
}

// TestEventIngest_InvalidProjectionQuarantineRecordsAnchoredAuditFact
// (REQ-2): the invalid-projection quarantine writes one anchored 'quarantined'
// fact and returns the deterministic 400.
func TestEventIngest_InvalidProjectionQuarantineRecordsAnchoredAuditFact(t *testing.T) {
	receipts := &fakeReceiptRepo{}
	ledger := &quarantineAuditAppendSpy{seen: map[string]bool{}}
	svc := NewEventIngestService(receipts,
		WithEventAllowedRegions([]string{"us-west"}),
		WithEventIngestAudit(NewAuditLedgerService(ledger)))

	envelope := invalidProjectionEnvelope()
	_, err := svc.Ingest(context.Background(), envelope)
	ae := requireAppError(t, err)
	if ae.HTTPStatus() != http.StatusBadRequest {
		t.Fatalf("HTTPStatus = %d, want 400", ae.HTTPStatus())
	}
	if ae.Code != "event.invalid_projection" {
		t.Fatalf("code = %q, want event.invalid_projection", ae.Code)
	}
	if len(receipts.quarantined) != 1 || receipts.quarantined[0] != "event.invalid_projection" {
		t.Fatalf("quarantine codes = %v, want [event.invalid_projection]", receipts.quarantined)
	}
	if len(ledger.facts) != 1 {
		t.Fatalf("ledger facts = %d, want exactly 1", len(ledger.facts))
	}
	fact := ledger.facts[0]
	if fact.Action != "source.event" {
		t.Errorf("action = %q, want source.event", fact.Action)
	}
	if fact.EventID != envelope.EventID {
		t.Errorf("event_id = %s, want %s (anchored on the envelope event_id)", fact.EventID, envelope.EventID)
	}
	if fact.Payload["status"] != "quarantined" {
		t.Errorf("payload status = %v, want quarantined", fact.Payload["status"])
	}
}

// TestEventIngest_InvalidProjectionReceiptFailureSuppressesFalseFact
// (REQ-2): a failed receipt write in the invalid-projection path returns >= 500
// and records ZERO facts — the pre-fix unconditional 'quarantined' fact (false
// fact) must not exist when the receipt is not durable.
func TestEventIngest_InvalidProjectionReceiptFailureSuppressesFalseFact(t *testing.T) {
	receipts := &fakeReceiptRepo{quarantineErr: errors.New("receipt write failed")}
	ledger := &quarantineAuditAppendSpy{seen: map[string]bool{}}
	svc := NewEventIngestService(receipts,
		WithEventAllowedRegions([]string{"us-west"}),
		WithEventIngestAudit(NewAuditLedgerService(ledger)))

	_, err := svc.Ingest(context.Background(), invalidProjectionEnvelope())
	ae := requireAppError(t, err)
	if ae.HTTPStatus() < 500 {
		t.Fatalf("HTTPStatus = %d, want >= 500 (Nak/redelivery)", ae.HTTPStatus())
	}
	if ae.Code != "event.quarantine_failed" {
		t.Fatalf("code = %q, want event.quarantine_failed", ae.Code)
	}
	if len(ledger.facts) != 0 {
		t.Fatalf("ledger facts = %d, want zero (false fact suppressed)", len(ledger.facts))
	}
	if len(receipts.applied) != 0 {
		t.Fatalf("event must not be applied, applied = %v", receipts.applied)
	}
}

// TestEventIngest_QuarantineRetryFoldsAtLedger (REQ-3): redelivering the same
// EventID after a successful first quarantine folds the second ledger append
// via ErrAuditDuplicate (uq_audit_ledger_tenant_event behavior) — the
// redelivery completes with the deterministic 403, never a 5xx audit failure,
// and every append attempt carries the anchored EventID.
func TestEventIngest_QuarantineRetryFoldsAtLedger(t *testing.T) {
	receipts := &fakeReceiptRepo{}
	ledger := &quarantineAuditAppendSpy{seen: map[string]bool{}}
	svc := NewEventIngestService(receipts,
		WithEventAllowedRegions([]string{"us-west"}),
		WithEventIngestAudit(NewAuditLedgerService(ledger)))

	envelope := testEnvelope("cn-north")
	if _, err := svc.Ingest(context.Background(), envelope); err == nil {
		t.Fatal("first delivery must be rejected")
	}
	if len(ledger.facts) != 1 {
		t.Fatalf("append attempts after first delivery = %d, want 1", len(ledger.facts))
	}

	_, err := svc.Ingest(context.Background(), envelope)
	ae := requireAppError(t, err)
	if ae.HTTPStatus() != http.StatusForbidden {
		t.Fatalf("redelivery HTTPStatus = %d, want 403 (ledger fold must not surface as 5xx)", ae.HTTPStatus())
	}
	if ae.Code != "event.region_not_allowed" {
		t.Fatalf("redelivery code = %q, want event.region_not_allowed", ae.Code)
	}
	if len(ledger.facts) != 2 {
		t.Fatalf("append attempts after redelivery = %d, want 2", len(ledger.facts))
	}
	for i, fact := range ledger.facts {
		if fact.EventID != envelope.EventID {
			t.Errorf("append attempt %d event_id = %s, want %s (anchored)", i, fact.EventID, envelope.EventID)
		}
	}
	if len(ledger.seen) != 1 {
		t.Fatalf("unique ledger triples = %d, want 1 (duplicate folded)", len(ledger.seen))
	}
}

// TestEventIngest_QuarantineRetryAfterAuditFailure (REQ-3): a partially failed
// first attempt (receipt durable, ledger append failed -> event.audit_failed
// 5xx -> Nak/redelivery) completes on redelivery with the deterministic 403;
// the retried fact re-derives the identical anchored EventID.
func TestEventIngest_QuarantineRetryAfterAuditFailure(t *testing.T) {
	receipts := &fakeReceiptRepo{}
	repo := &auditLedgerRepoSpy{appendErr: errors.New("ledger down")}
	svc := NewEventIngestService(receipts,
		WithEventAllowedRegions([]string{"us-west"}),
		WithEventIngestAudit(NewAuditLedgerService(repo)))

	envelope := testEnvelope("cn-north")
	_, err := svc.Ingest(context.Background(), envelope)
	ae := requireAppError(t, err)
	if ae.HTTPStatus() < 500 {
		t.Fatalf("first attempt HTTPStatus = %d, want >= 500 (event.audit_failed)", ae.HTTPStatus())
	}
	if ae.Code != "event.audit_failed" {
		t.Fatalf("first attempt code = %q, want event.audit_failed", ae.Code)
	}

	repo.appendErr = nil
	_, err = svc.Ingest(context.Background(), envelope)
	ae = requireAppError(t, err)
	if ae.HTTPStatus() != http.StatusForbidden {
		t.Fatalf("redelivery HTTPStatus = %d, want 403", ae.HTTPStatus())
	}
	if repo.appended == nil || repo.appended.EventID != envelope.EventID {
		t.Fatalf("retried fact event_id = %v, want %s (anchored)", repo.appended.EventID, envelope.EventID)
	}
	if len(receipts.quarantined) != 2 {
		t.Fatalf("quarantine attempts = %d, want 2 (receipt idempotent on retry)", len(receipts.quarantined))
	}
}
