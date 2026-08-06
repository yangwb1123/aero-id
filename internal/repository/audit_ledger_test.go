package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

func TestAuditRecordHashBindsRequestIDInVersionTwo(t *testing.T) {
	base := &domain.AuditFact{ID: idgen.NewID(), HashVersion: 2, ChainSequence: 1, Source: "aero-id",
		SourceRegion: "local", PartitionKey: "account-1", EventID: idgen.NewID(), RequestID: "request-1",
		Action: "account.viewed", OccurredAt: time.Unix(1, 0).UTC(), CreatedAt: time.Unix(2, 0).UTC(), PayloadHash: "payload"}
	first := auditRecordHash(base)
	base.RequestID = "request-2"
	if first == auditRecordHash(base) {
		t.Fatal("version-two audit hash does not bind request id")
	}
}

func TestAuditLedgerFactToRowPreservesHashVersion(t *testing.T) {
	fact := &domain.AuditFact{ID: idgen.NewID(), HashVersion: 2, Source: "aero-id", SourceRegion: "local",
		PartitionKey: "p", Action: "test", OccurredAt: time.Now().UTC()}
	row := auditLedgerFactToRow(fact, []byte(`{}`))
	if row.HashVersion != 2 {
		t.Fatalf("row hash version = %d, want 2", row.HashVersion)
	}
}

func validAuditFact() *domain.AuditFact {
	return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "p",
		TenantID: "tenant-a", Action: "test", OccurredAt: time.Now().UTC()}
}

// TestAuditLedgerFailClosedWithoutSigningKey: with an empty key the ledger must
// refuse append and verification before touching the database (a zero-value
// *gorm.DB panics on any use, so reaching the DB would fail the test).
func TestAuditLedgerFailClosedWithoutSigningKey(t *testing.T) {
	repo := NewAuditLedgerRepository(&gorm.DB{}, AuditLedgerConfig{})
	txRepo, ok := repo.(AuditLedgerTransactionalRepository)
	if !ok {
		t.Fatal("ledger repository must implement the transactional interface")
	}
	tests := []struct {
		name string
		err  error
	}{
		{"append", repo.Append(t.Context(), validAuditFact())},
		{"append nil fact", repo.Append(t.Context(), nil)},
		{"append in tx", txRepo.AppendInTx(t.Context(), nil, validAuditFact())},
		{"verify chain", repo.VerifyChain(t.Context(), "aero-id", "p")},
	}
	for _, tc := range tests {
		if tc.err == nil || !strings.Contains(tc.err.Error(), "signing key is not configured") {
			t.Fatalf("%s without key = %v, want signing-key error", tc.name, tc.err)
		}
	}
}

// TestAuditLedgerGuardsAreKeyConditional: with a key configured the guards pass
// and the next deterministic validation error surfaces instead.
func TestAuditLedgerGuardsAreKeyConditional(t *testing.T) {
	repo := NewAuditLedgerRepository(&gorm.DB{}, AuditLedgerConfig{SigningKey: "test-key"})
	txRepo, ok := repo.(AuditLedgerTransactionalRepository)
	if !ok {
		t.Fatal("ledger repository must implement the transactional interface")
	}
	if err := repo.Append(t.Context(), nil); err == nil || strings.Contains(err.Error(), "signing key") {
		t.Fatalf("Append(nil) with key = %v, want audit-fact validation error", err)
	}
	if err := txRepo.AppendInTx(t.Context(), nil, validAuditFact()); err == nil || strings.Contains(err.Error(), "signing key") {
		t.Fatalf("AppendInTx(nil tx) with key = %v, want transaction error", err)
	}
	keyed := NewAuditLedgerRepository(newAuditLedgerErrorDB(t), AuditLedgerConfig{SigningKey: "test-key"})
	if err := keyed.Append(t.Context(), validAuditFact()); err == nil || strings.Contains(err.Error(), "signing key") {
		t.Fatalf("Append with key = %v, want database-layer error", err)
	}
	if err := keyed.VerifyChain(t.Context(), "aero-id", "p"); err == nil || strings.Contains(err.Error(), "signing key") {
		t.Fatalf("VerifyChain with key = %v, want database-layer error", err)
	}
}

// auditLedgerErrorPool fails every call, so a keyed repository demonstrably
// reaches the storage layer without any real database.

type auditLedgerErrorPool struct{}

func (auditLedgerErrorPool) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	return nil, errors.New("audit ledger error db")
}

func (auditLedgerErrorPool) ExecContext(context.Context, string, ...interface{}) (sql.Result, error) {
	return nil, errors.New("audit ledger error db")
}

func (auditLedgerErrorPool) QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error) {
	return nil, errors.New("audit ledger error db")
}

func (auditLedgerErrorPool) QueryRowContext(context.Context, string, ...interface{}) *sql.Row {
	return nil
}

type auditLedgerErrorDialector struct{}

func (auditLedgerErrorDialector) Name() string { return "audit-ledger-error" }

func (auditLedgerErrorDialector) Initialize(db *gorm.DB) error {
	db.ConnPool = auditLedgerErrorPool{}
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{})
	return nil
}

func (auditLedgerErrorDialector) Migrator(*gorm.DB) gorm.Migrator { return nil }

func (auditLedgerErrorDialector) DataTypeOf(*schema.Field) string { return "" }

func (auditLedgerErrorDialector) DefaultValueOf(*schema.Field) clause.Expression { return nil }

func (auditLedgerErrorDialector) BindVarTo(clause.Writer, *gorm.Statement, interface{}) {}

func (auditLedgerErrorDialector) QuoteTo(clause.Writer, string) {}

func (auditLedgerErrorDialector) Explain(string, ...interface{}) string { return "" }

func newAuditLedgerErrorDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(auditLedgerErrorDialector{}, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// unitAuditRow builds a valid chain row exactly as the append path computes
// it (payload hash, record hash, and HMAC for signed rows), so a verification
// failure can only come from the property under test.
func unitAuditRow(key []byte, sequence int64, previous, payload string, signatureVersion int) *auditLedgerRow {
	raw := []byte(payload)
	fact := &domain.AuditFact{ID: idgen.NewID(), HashVersion: 2, ChainSequence: sequence, Source: "aero-id",
		SourceRegion: "local", PartitionKey: "p", TenantID: "tenant-a", Action: "test",
		OccurredAt: time.Now().UTC().Truncate(time.Microsecond), CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		PayloadHash: digest(raw), PreviousHash: previous}
	fact.RecordHash = auditRecordHash(fact)
	if signatureVersion >= 1 {
		fact.SignatureVersion = 1
		fact.Signature = sign(key, fact.RecordHash)
	}
	return auditLedgerFactToRow(fact, raw)
}

// TestVerifyAuditRowRejectsSignatureVersionDowngrade is the F1 regression: a
// DB writer tampers a signed row, recomputes payload_hash and record_hash,
// re-chains it, strips the HMAC, and sets signature_version = 0. Every
// recomputable control passes; only the monotonic version check rejects it.
func TestVerifyAuditRowRejectsSignatureVersionDowngrade(t *testing.T) {
	key := []byte("unit-test-audit-ledger-signing-key-0123456789abcdef")
	first, version, err := verifyAuditRow(key, "", 0, unitAuditRow(key, 1, "", `{"n":1}`, 1))
	if err != nil {
		t.Fatalf("signed row rejected: %v", err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}
	tampered := unitAuditRow(key, 2, first, `{"n":42}`, 0)
	_, _, err = verifyAuditRow(key, first, version, tampered)
	if err == nil || !strings.Contains(err.Error(), "signature version downgrade") {
		t.Fatalf("downgraded row: err = %v, want signature version downgrade failure", err)
	}
}

// TestVerifyAuditRowAcceptsLegacyUnsignedPrefix: legacy unsigned rows
// (signature_version 0) may precede signed rows (1); the 0,0,1,1 rollout
// prefix must keep verifying under the monotonic check.
func TestVerifyAuditRowAcceptsLegacyUnsignedPrefix(t *testing.T) {
	key := []byte("unit-test-audit-ledger-signing-key-0123456789abcdef")
	previous, version := "", 0
	for i, tc := range []struct {
		payload string
		sigVer  int
		wantVer int
	}{
		{`{"n":1}`, 0, 0},
		{`{"n":2}`, 0, 0},
		{`{"n":3}`, 1, 1},
		{`{"n":4}`, 1, 1},
	} {
		row := unitAuditRow(key, int64(i+1), previous, tc.payload, tc.sigVer)
		next, nextVersion, err := verifyAuditRow(key, previous, version, row)
		if err != nil {
			t.Fatalf("row %d (version %d) rejected: %v", i+1, tc.sigVer, err)
		}
		previous, version = next, nextVersion
		if version != tc.wantVer {
			t.Fatalf("row %d version = %d, want %d", i+1, version, tc.wantVer)
		}
	}
}
