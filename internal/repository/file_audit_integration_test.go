//go:build integration

package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

// TestFileHardDeleteWithAuditAtomicity (storage GC audit): the hard delete of
// purged file rows and the storage.gc.purged fact commit in one transaction —
// crash side (publication insert blocked) rolls both back; commit side yields
// the rows gone + exactly 1 ledger row + 1 pending publication row.
func TestFileHardDeleteWithAuditAtomicity(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "gc-audit-key-0123456789abcdef"})
	fileRepo := repository.NewFileRepository(db, ledger)
	auditRepo, ok := fileRepo.(repository.FileAuditRepository)
	if !ok {
		t.Fatal("file repo must implement FileAuditRepository")
	}

	seedFiles := func() []domain.ID {
		ids := make([]domain.ID, 2)
		for i := range ids {
			ids[i] = idgen.NewID()
			f := &domain.File{ID: ids[i], OwnerUID: "u-1", Name: "f", StorageKey: "k",
				Checksum: idgen.NewID().String()}
			if err := db.Exec(`INSERT INTO files (id, owner_uid, name, size, mime_type, storage_key, storage_type, checksum, visibility, created_at, updated_at)
				VALUES (?, 'u-1', 'f', 1, 'text/plain', 'k', 'local', ?, 0, now(), now())`, ids[i], f.Checksum).Error; err != nil {
				t.Fatal(err)
			}
		}
		return ids
	}
	gcFact := func() *domain.AuditFact {
		return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "legacy:gc",
			TenantID: "legacy", ActorUID: "storage-gc", Action: "storage.gc.purged",
			ResourceType: "file", OccurredAt: time.Now().UTC(), Payload: map[string]any{"rows": 2}}
	}

	// Commit side: rows gone, one ledger + one publication row.
	ids := seedFiles()
	if err := auditRepo.HardDeleteWithAudit(ctx, ids, gcFact()); err != nil {
		t.Fatalf("HardDeleteWithAudit: %v", err)
	}
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM files WHERE id IN ?`, ids).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("files after purge = %d, want 0", rows)
	}
	var ledgerRows, pubRows int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'storage.gc.purged'`).Scan(&ledgerRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.action = 'storage.gc.purged'`).Scan(&pubRows).Error; err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 1 || pubRows != 1 {
		t.Fatalf("ledger/publication rows = %d/%d, want 1/1", ledgerRows, pubRows)
	}

	// Crash side: blocked publication insert rolls the whole purge back.
	db2 := testpg.Start(t)
	testpg.Migrate(t, db2, filepath.Join(testpg.RepoRoot(t), "migrations"))
	installFailPublicationInsertTrigger(t, db2)
	ledger2 := repository.NewAuditLedgerRepository(db2, repository.AuditLedgerConfig{SigningKey: "gc-audit-key-0123456789abcdef"})
	fileRepo2 := repository.NewFileRepository(db2, ledger2).(repository.FileAuditRepository)
	ids2 := seedFilesOn(t, db2)
	if err := fileRepo2.HardDeleteWithAudit(ctx, ids2, gcFact()); err == nil {
		t.Fatal("crash side: expected the blocked publication insert to fail the purge")
	}
	var rows2 int64
	if err := db2.Raw(`SELECT count(*) FROM files WHERE id IN ?`, ids2).Scan(&rows2).Error; err != nil {
		t.Fatal(err)
	}
	if rows2 != 2 {
		t.Fatalf("files after rolled-back purge = %d, want 2 (rows survive)", rows2)
	}
	var ledger2Rows int64
	if err := db2.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'storage.gc.purged'`).Scan(&ledger2Rows).Error; err != nil {
		t.Fatal(err)
	}
	if ledger2Rows != 0 {
		t.Fatalf("ledger rows after rollback = %d, want 0", ledger2Rows)
	}
}

// seedFilesOn inserts file rows on the given db (the crash-side fixture uses
// its own database).
func seedFilesOn(t *testing.T, db *gorm.DB) []domain.ID {
	t.Helper()
	ids := make([]domain.ID, 2)
	for i := range ids {
		ids[i] = idgen.NewID()
		if err := db.Exec(`INSERT INTO files (id, owner_uid, name, size, mime_type, storage_key, storage_type, checksum, visibility, created_at, updated_at)
			VALUES (?, 'u-1', 'f', 1, 'text/plain', 'k', 'local', ?, 0, now(), now())`, ids[i], idgen.NewID().String()).Error; err != nil {
			t.Fatal(err)
		}
	}
	return ids
}
