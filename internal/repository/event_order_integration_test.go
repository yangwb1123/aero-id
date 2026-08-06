//go:build integration

package repository_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func errorsIsTenantMismatch(err error) bool {
	return errors.Is(err, repository.ErrEventTenantMismatch)
}

// TestEventReceiptOutOfOrderKeepsNewerVersion is a release-gate scenario:
// delivering v3 then an older v2 must NOT roll back the projection, and the
// duplicate/out-of-order events must leave exactly one durable receipt each.
func TestEventReceiptOutOfOrderKeepsNewerVersion(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "u-ooo", "u-ooo", "local")
	if err != nil {
		t.Fatal(err)
	}
	snapshots := repository.NewAccountSnapshotRepository(db)
	receipts := repository.NewEventReceiptRepository(db, ledger)

	apply := func(version int64, usage float64) repository.EventApplyStatus {
		t.Helper()
		envelope := eventStoreEnvelope(account.ID, "aero-vault.buckets", version, time.Now().UTC())
		envelope.Payload["data"] = map[string]any{"usage_bytes": usage}
		mutation, err := envelope.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		result, err := receipts.ApplySnapshotEvent(t.Context(), envelope, mutation)
		if err != nil {
			t.Fatalf("apply v%d: %v", version, err)
		}
		return result.Status
	}

	// Deliver v3, then an older v2 (out-of-order), then the exact v3 duplicate.
	v3 := eventStoreEnvelope(account.ID, "aero-vault.buckets", 3, time.Now().UTC())
	v3.Payload["data"] = map[string]any{"usage_bytes": float64(30)}
	mutation3, err := v3.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := receipts.ApplySnapshotEvent(t.Context(), v3, mutation3); err != nil || result.Status != repository.EventApplied {
		t.Fatalf("v3 = %v, %v; want applied", result, err)
	}
	if status := apply(2, 20); status != repository.EventOutOfOrder {
		t.Fatalf("v2 after v3 status = %s, want out_of_order", status)
	}
	if result, err := receipts.ApplySnapshotEvent(t.Context(), v3, mutation3); err != nil || result.Status != repository.EventDuplicate {
		t.Fatalf("duplicate v3 = %v, %v; want duplicate", result, err)
	}

	snapshot, err := snapshots.GetSnapshot(t.Context(), account.ID, "aero-vault", "aero-vault.buckets")
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	if snapshot.Version != 3 {
		t.Fatalf("snapshot version = %d, want 3 (no rollback)", snapshot.Version)
	}
	usage, ok := numericValue(snapshot.Data["usage_bytes"])
	if !ok || usage != 30 {
		t.Errorf("snapshot data = %v, want usage_bytes 30", snapshot.Data)
	}
}

// numericValue normalizes JSONB numbers (float64/int) for comparisons.
func numericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// TestEventReceiptTenantIsolation: the same event delivered to a different
// tenant must be rejected, not applied to the other tenant's projection.
func TestEventReceiptTenantIsolation(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	tenantA, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "u-iso", "u-iso", "local")
	if err != nil {
		t.Fatal(err)
	}
	receipts := repository.NewEventReceiptRepository(db, ledger)

	envelope := eventStoreEnvelope(tenantA.ID, "aero-vault.buckets", 1, time.Now().UTC())
	envelope.TenantID = "tenant-b" // spoofed tenant
	mutation, err := envelope.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, applyErr := receipts.ApplySnapshotEvent(t.Context(), envelope, mutation); applyErr == nil {
		t.Fatal("cross-tenant event apply must fail")
	} else if !errorsIsTenantMismatch(applyErr) {
		t.Errorf("error = %v, want tenant mismatch", applyErr)
	}
}
