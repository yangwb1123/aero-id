//go:build integration

package repository_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/event"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"github.com/aero/aero-id/internal/service"
)

// ingestAuditSigningKey: the audit ledger requires a signing key even for the
// standalone-append path (audit_ledger.go Append fails closed without one).
const ingestAuditSigningKey = "event-ingest-quarantine-integration-key-0123456789abcdef"

// newIngestQuarantineHarness wires the real ingest service over the real
// receipt + ledger repositories with a residency allow-list of us-west only.
func newIngestQuarantineHarness(t *testing.T) (*gorm.DB, service.EventIngestService) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: ingestAuditSigningKey})
	receipts := repository.NewEventReceiptRepository(db, ledger)
	svc := service.NewEventIngestService(receipts,
		service.WithEventAllowedRegions([]string{"us-west"}),
		service.WithEventIngestAudit(service.NewAuditLedgerService(ledger)))
	return db, svc
}

func requireIngestAppError(t *testing.T, err error) *apperr.AppError {
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

func countQuarantineReceiptRows(t *testing.T, db *gorm.DB, envelope *event.Envelope) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM event_receipts WHERE tenant_id = ? AND source = ? AND event_id = ?`,
		envelope.TenantID, envelope.Source, envelope.EventID).Scan(&n).Error; err != nil {
		t.Fatalf("count receipt rows: %v", err)
	}
	return n
}

func fetchQuarantineReceipt(t *testing.T, db *gorm.DB, envelope *event.Envelope) (status, errorCode string) {
	t.Helper()
	var row struct {
		Status    string `gorm:"column:status"`
		ErrorCode string `gorm:"column:error_code"`
	}
	if err := db.Raw(`SELECT status, error_code FROM event_receipts WHERE tenant_id = ? AND source = ? AND event_id = ?`,
		envelope.TenantID, envelope.Source, envelope.EventID).Scan(&row).Error; err != nil {
		t.Fatalf("fetch receipt row: %v", err)
	}
	return row.Status, row.ErrorCode
}

func fetchQuarantineLedgerFact(t *testing.T, db *gorm.DB, envelope *event.Envelope) (action, status string) {
	t.Helper()
	var row struct {
		Action string `gorm:"column:action"`
		Status string `gorm:"column:status"`
	}
	if err := db.Raw(`SELECT action, payload->>'status' AS status FROM audit_ledger WHERE tenant_id = ? AND source = ? AND event_id = ?`,
		envelope.TenantID, envelope.Source, envelope.EventID).Scan(&row).Error; err != nil {
		t.Fatalf("fetch ledger row: %v", err)
	}
	return row.Action, row.Status
}

// TestEventIngestQuarantine_RegionDisallowed (REQ-7.1): a residency-policy
// violation produces exactly one event_receipts row (status quarantined,
// error_code event.region_not_allowed) AND exactly one audit_ledger row for
// (tenant_id, source, event_id) with action source.event and payload status
// quarantined; the deterministic 403 is returned.
func TestEventIngestQuarantine_RegionDisallowed(t *testing.T) {
	db, svc := newIngestQuarantineHarness(t)
	envelope := eventStoreEnvelope(idgen.NewID(), "aero-vault.buckets", 3, time.Now().UTC())
	envelope.SourceRegion = "cn-north" // outside the us-west allow-list

	_, err := svc.Ingest(t.Context(), envelope)
	ae := requireIngestAppError(t, err)
	if ae.HTTPStatus() != 403 || ae.Code != "event.region_not_allowed" {
		t.Fatalf("error = %s (HTTP %d), want event.region_not_allowed 403", ae.Code, ae.HTTPStatus())
	}
	if n := countQuarantineReceiptRows(t, db, envelope); n != 1 {
		t.Fatalf("receipt rows = %d, want exactly 1", n)
	}
	if status, code := fetchQuarantineReceipt(t, db, envelope); status != "quarantined" || code != "event.region_not_allowed" {
		t.Fatalf("receipt = (%s, %s), want (quarantined, event.region_not_allowed)", status, code)
	}
	if n := countAuditLedgerRows(t, db, envelope.EventID); n != 1 {
		t.Fatalf("ledger rows = %d, want exactly 1", n)
	}
	if action, status := fetchQuarantineLedgerFact(t, db, envelope); action != "source.event" || status != "quarantined" {
		t.Fatalf("ledger fact = (%s, %s), want (source.event, quarantined)", action, status)
	}
}

// TestEventIngestQuarantine_InvalidProjection (REQ-7.2): an envelope whose
// Snapshot() fails (payload.account_id missing) produces one receipt row with
// error_code event.invalid_projection and one anchored ledger fact; the
// deterministic 400 is returned.
func TestEventIngestQuarantine_InvalidProjection(t *testing.T) {
	db, svc := newIngestQuarantineHarness(t)
	envelope := eventStoreEnvelope(idgen.NewID(), "aero-vault.buckets", 1, time.Now().UTC())
	delete(envelope.Payload, "account_id") // passes Validate(), fails Snapshot()

	_, err := svc.Ingest(t.Context(), envelope)
	ae := requireIngestAppError(t, err)
	if ae.HTTPStatus() != 400 || ae.Code != "event.invalid_projection" {
		t.Fatalf("error = %s (HTTP %d), want event.invalid_projection 400", ae.Code, ae.HTTPStatus())
	}
	if n := countQuarantineReceiptRows(t, db, envelope); n != 1 {
		t.Fatalf("receipt rows = %d, want exactly 1", n)
	}
	if status, code := fetchQuarantineReceipt(t, db, envelope); status != "quarantined" || code != "event.invalid_projection" {
		t.Fatalf("receipt = (%s, %s), want (quarantined, event.invalid_projection)", status, code)
	}
	if n := countAuditLedgerRows(t, db, envelope.EventID); n != 1 {
		t.Fatalf("ledger rows = %d, want exactly 1", n)
	}
	if action, status := fetchQuarantineLedgerFact(t, db, envelope); action != "source.event" || status != "quarantined" {
		t.Fatalf("ledger fact = (%s, %s), want (source.event, quarantined)", action, status)
	}
}

// TestEventIngestQuarantine_RedeliveryFolds (REQ-7.3): redelivering the same
// EventID keeps exactly one event_receipts row and exactly one audit_ledger
// row for the key (receipt ON CONFLICT DO NOTHING + ledger ErrAuditDuplicate
// fold), and Ingest returns the deterministic 403 with no 5xx audit failure.
func TestEventIngestQuarantine_RedeliveryFolds(t *testing.T) {
	db, svc := newIngestQuarantineHarness(t)
	envelope := eventStoreEnvelope(idgen.NewID(), "aero-vault.buckets", 2, time.Now().UTC())
	envelope.SourceRegion = "cn-north"

	if _, err := svc.Ingest(t.Context(), envelope); err == nil {
		t.Fatal("first delivery must be rejected")
	}
	_, redeliveryErr := svc.Ingest(t.Context(), envelope)
	if redeliveryErr == nil {
		t.Fatal("redelivery must be rejected with the deterministic status")
	}
	ae := requireIngestAppError(t, redeliveryErr)
	if ae.HTTPStatus() != 403 || ae.Code != "event.region_not_allowed" {
		t.Fatalf("redelivery error = %s (HTTP %d), want event.region_not_allowed 403 (ledger fold, no 5xx)", ae.Code, ae.HTTPStatus())
	}
	if n := countQuarantineReceiptRows(t, db, envelope); n != 1 {
		t.Fatalf("receipt rows after redelivery = %d, want still 1", n)
	}
	if n := countAuditLedgerRows(t, db, envelope.EventID); n != 1 {
		t.Fatalf("ledger rows after redelivery = %d, want still 1 (duplicate folded)", n)
	}
}
