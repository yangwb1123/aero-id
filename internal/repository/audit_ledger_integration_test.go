//go:build integration

package repository

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository/testpg"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const auditLedgerTestKey = "integration-test-audit-ledger-signing-key-0123456789abcdef"

func startAuditLedger(t *testing.T, key string) (*gorm.DB, AuditLedgerRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return db, NewAuditLedgerRepository(db, AuditLedgerConfig{SigningKey: key})
}

func appendAuditFacts(t *testing.T, repo AuditLedgerRepository, partition string, payloads ...map[string]any) {
	t.Helper()
	for _, payload := range payloads {
		fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: partition,
			TenantID: "tenant-a", Action: "test.action", OccurredAt: time.Now().UTC(),
			Payload: payload, EventID: idgen.NewID()}
		if err := repo.Append(t.Context(), fact); err != nil {
			t.Fatal(err)
		}
	}
}

// disableAuditLedgerMutationTrigger lets tests simulate tampering; migration
// 000034 otherwise blocks UPDATE/DELETE on the append-only ledger.
func disableAuditLedgerMutationTrigger(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("ALTER TABLE audit_ledger DISABLE TRIGGER audit_ledger_append_only").Error; err != nil {
		t.Fatal(err)
	}
}

// TestAuditLedgerVerifyChainRoundTripsAdversarialPayloads: unmodified chains
// containing integers above 2^53, exponent literals, and non-bytewise key
// orders must verify; a float64 re-marshal of the stored bytes rejects them.
func TestAuditLedgerVerifyChainRoundTripsAdversarialPayloads(t *testing.T) {
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	appendAuditFacts(t, repo, "roundtrip",
		map[string]any{"n": int64(9007199254740993)},
		map[string]any{"n": float64(1e21)},
		map[string]any{"n": float64(1e20)},
		map[string]any{"n": float64(1e19)},
		map[string]any{"n": float64(1e-7)},
		map[string]any{"b": 1, "aa": 2},
		map[string]any{"n": float64(1234567)},
		map[string]any{"n": 1.0},
		map[string]any{"n": int64(-9007199254740993)},
		map[string]any{"outer": map[string]any{"inner": []any{int64(1), float64(2.5), "x", true, nil, map[string]any{"k": int64(-7)}}}},
	)
	if err := repo.VerifyChain(t.Context(), "aero-id", "roundtrip"); err != nil {
		t.Fatalf("unmodified chain rejected: %v", err)
	}
	var signed int64
	if err := db.Raw("SELECT count(*) FROM audit_ledger WHERE partition_key = ? AND signature_version = 1 AND signature != ''", "roundtrip").Scan(&signed).Error; err != nil {
		t.Fatal(err)
	}
	if signed != 10 {
		t.Fatalf("signed rows = %d, want 10", signed)
	}
}

// TestAuditLedgerVerifyChainDetectsPayloadTampering: flipping a stored payload
// without recomputing its hash must fail payload verification.
func TestAuditLedgerVerifyChainDetectsPayloadTampering(t *testing.T) {
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	appendAuditFacts(t, repo, "tamper-payload", map[string]any{"n": int64(1)}, map[string]any{"n": int64(2)})
	disableAuditLedgerMutationTrigger(t, db)
	if err := db.Exec("UPDATE audit_ledger SET payload = '{\"n\": 999}'::jsonb WHERE partition_key = 'tamper-payload' AND chain_sequence = 1").Error; err != nil {
		t.Fatal(err)
	}
	err := repo.VerifyChain(t.Context(), "aero-id", "tamper-payload")
	if err == nil || !strings.Contains(err.Error(), "payload verification failed") {
		t.Fatalf("tampered payload: err = %v, want payload verification failure", err)
	}
}

// TestAuditLedgerVerifyChainDetectsChainAndSignatureTampering: breaking the
// previous-hash linkage or corrupting a signed row's HMAC must fail.
func TestAuditLedgerVerifyChainDetectsChainAndSignatureTampering(t *testing.T) {
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	appendAuditFacts(t, repo, "tamper-chain", map[string]any{"n": int64(1)}, map[string]any{"n": int64(2)})
	disableAuditLedgerMutationTrigger(t, db)
	if err := db.Exec("UPDATE audit_ledger SET previous_hash = 'deadbeef' WHERE partition_key = 'tamper-chain' AND chain_sequence = 2").Error; err != nil {
		t.Fatal(err)
	}
	err := repo.VerifyChain(t.Context(), "aero-id", "tamper-chain")
	if err == nil || !strings.Contains(err.Error(), "chain verification failed") {
		t.Fatalf("tampered previous_hash: err = %v, want chain verification failure", err)
	}
	if err := db.Exec("UPDATE audit_ledger SET previous_hash = (SELECT record_hash FROM audit_ledger WHERE partition_key = 'tamper-chain' AND chain_sequence = 1) WHERE partition_key = 'tamper-chain' AND chain_sequence = 2").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE audit_ledger SET signature = 'deadbeef' WHERE partition_key = 'tamper-chain' AND chain_sequence = 2").Error; err != nil {
		t.Fatal(err)
	}
	err = repo.VerifyChain(t.Context(), "aero-id", "tamper-chain")
	if err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("tampered signature: err = %v, want signature verification failure", err)
	}
}

// TestAuditLedgerVerifyChainDetectsRehashedPayloadTampering: recomputing the
// payload hash for a flipped payload still fails, because record_hash binds it.
func TestAuditLedgerVerifyChainDetectsRehashedPayloadTampering(t *testing.T) {
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	appendAuditFacts(t, repo, "tamper-rehash", map[string]any{"n": int64(1)})
	disableAuditLedgerMutationTrigger(t, db)
	flipped := []byte(`{"n":42}`)
	if err := db.Exec("UPDATE audit_ledger SET payload = '{\"n\": 42}'::jsonb, payload_hash = ? WHERE partition_key = 'tamper-rehash' AND chain_sequence = 1", digest(flipped)).Error; err != nil {
		t.Fatal(err)
	}
	err := repo.VerifyChain(t.Context(), "aero-id", "tamper-rehash")
	if err == nil || !strings.Contains(err.Error(), "chain verification failed") {
		t.Fatalf("rehashed tampered payload: err = %v, want chain verification failure", err)
	}
}

// TestAuditLedgerLegacyUnsignedRowsVerifyByHashChain: rows written before a
// signing key existed (signature_version 0) verify via the hash chain alone
// and still fail when tampered.
func TestAuditLedgerLegacyUnsignedRowsVerifyByHashChain(t *testing.T) {
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	legacy := &auditLedgerRepo{db: db}
	fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "legacy",
		TenantID: "tenant-a", Action: "test.action", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"n": int64(1)}, EventID: idgen.NewID()}
	payload, err := json.Marshal(fact.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.appendInTx(db, fact, payload); err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyChain(t.Context(), "aero-id", "legacy"); err != nil {
		t.Fatalf("legacy unsigned chain rejected: %v", err)
	}
	disableAuditLedgerMutationTrigger(t, db)
	if err := db.Exec("UPDATE audit_ledger SET payload = '{\"n\": 7}'::jsonb WHERE partition_key = 'legacy'").Error; err != nil {
		t.Fatal(err)
	}
	err = repo.VerifyChain(t.Context(), "aero-id", "legacy")
	if err == nil || !strings.Contains(err.Error(), "payload verification failed") {
		t.Fatalf("tampered legacy row: err = %v, want payload verification failure", err)
	}
}

// TestAuditLedgerVerifyChainRejectsSignatureVersionDowngrade: a DB writer who
// tampers a signed row, re-chains it, strips the HMAC, and sets
// signature_version = 0 must fail verification. This is the F1 probe: without
// the monotonic version check the downgrade strips the only key-bound control.
func TestAuditLedgerVerifyChainRejectsSignatureVersionDowngrade(t *testing.T) {
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	appendAuditFacts(t, repo, "downgrade", map[string]any{"n": int64(1)}, map[string]any{"n": int64(2)})
	disableAuditLedgerMutationTrigger(t, db)
	var row auditLedgerRow
	if err := db.Where("partition_key = ? AND chain_sequence = ?", "downgrade", 2).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	fact := auditLedgerRowToFact(&row)
	payload, err := json.Marshal(map[string]any{"n": int64(42)})
	if err != nil {
		t.Fatal(err)
	}
	fact.Payload = map[string]any{"n": int64(42)}
	fact.PayloadHash = digest(payload)
	fact.RecordHash = auditRecordHash(fact)
	fact.SignatureVersion = 0
	fact.Signature = ""
	if err := db.Model(&auditLedgerRow{}).Where("id = ?", row.ID).Updates(map[string]any{
		"payload": datatypes.JSON(payload), "payload_hash": fact.PayloadHash, "record_hash": fact.RecordHash,
		"signature_version": fact.SignatureVersion, "signature": fact.Signature,
	}).Error; err != nil {
		t.Fatal(err)
	}
	err = repo.VerifyChain(t.Context(), "aero-id", "downgrade")
	if err == nil || !strings.Contains(err.Error(), "signature version downgrade") {
		t.Fatalf("downgraded chain: err = %v, want signature version downgrade failure", err)
	}
}

// TestAuditLedgerVerifyChainAcceptsLegacyUnsignedPrefix: chains whose legacy
// unsigned rows (signature_version 0) precede signed rows (1) must keep
// verifying (0,0,1,1 prefix compatibility preserved by the monotonic check).
func TestAuditLedgerVerifyChainAcceptsLegacyUnsignedPrefix(t *testing.T) {
	db, repo := startAuditLedger(t, auditLedgerTestKey)
	legacy := &auditLedgerRepo{db: db}
	appendLegacy := func(payload map[string]any) {
		fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "mixed-prefix",
			TenantID: "tenant-a", Action: "test.action", OccurredAt: time.Now().UTC(),
			Payload: payload, EventID: idgen.NewID()}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := legacy.appendInTx(db, fact, raw); err != nil {
			t.Fatal(err)
		}
	}
	appendLegacy(map[string]any{"n": int64(1)})
	appendLegacy(map[string]any{"n": int64(2)})
	appendAuditFacts(t, repo, "mixed-prefix", map[string]any{"n": int64(3)}, map[string]any{"n": int64(4)})
	if err := repo.VerifyChain(t.Context(), "aero-id", "mixed-prefix"); err != nil {
		t.Fatalf("legacy 0,0 then signed 1,1 prefix rejected: %v", err)
	}
}

// TestAuditLedgerAppendAndVerifyFailClosedWithoutKey: with an empty key the
// repository refuses both operations against a real database and writes nothing.
func TestAuditLedgerAppendAndVerifyFailClosedWithoutKey(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	repo := NewAuditLedgerRepository(db, AuditLedgerConfig{})
	if err := repo.Append(t.Context(), &domain.AuditFact{Source: "aero-id", SourceRegion: "local",
		PartitionKey: "failclosed", TenantID: "tenant-a", Action: "test.action", OccurredAt: time.Now().UTC()}); err == nil ||
		!strings.Contains(err.Error(), "signing key is not configured") {
		t.Fatalf("append without key = %v, want signing-key error", err)
	}
	if err := repo.VerifyChain(t.Context(), "aero-id", "failclosed"); err == nil ||
		!strings.Contains(err.Error(), "signing key is not configured") {
		t.Fatalf("verify without key = %v, want signing-key error", err)
	}
	var rows int64
	db.Model(&auditLedgerRow{}).Count(&rows)
	if rows != 0 {
		t.Fatalf("fail-closed append wrote %d rows", rows)
	}
}
