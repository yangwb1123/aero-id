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

// matrixRepo bundles the six mutation repositories under test plus the ledger.
type matrixRepo struct {
	db        *gorm.DB
	ledger    repository.FullAuditLedgerRepository
	admin     repository.AdminAuditRepository
	ban       repository.BanAuditRepository
	block     repository.BlockRepository // WithAudit surface folded into the interface
	comment   repository.CommentAuditRepository
	message   repository.MessageAuditRepository
	fileShare repository.FileShareAuditRepository
}

func startMatrixRepo(t *testing.T) *matrixRepo {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "matrix-audit-key-0123456789abcdef"})
	m := &matrixRepo{db: db, ledger: ledger}
	// The six audit interfaces are implemented by their own concrete repos,
	// each constructed with the same ledger.
	m.admin = repository.NewAdminRepository(db, ledger).(repository.AdminAuditRepository)
	m.ban = repository.NewBanRepository(db, ledger).(repository.BanAuditRepository)
	m.block = repository.NewBlockRepository(db, ledger)
	m.comment = repository.NewCommentRepository(db, nil, ledger).(repository.CommentAuditRepository)
	m.message = repository.NewMessageRepository(db, nil, ledger).(repository.MessageAuditRepository)
	m.fileShare = repository.NewFileShareRepository(db, ledger).(repository.FileShareAuditRepository)
	return m
}

func matrixFact(eventID domain.ID, action, tenantID string) *domain.AuditFact {
	return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: tenantID + ":matrix",
		EventID: eventID, TenantID: tenantID, ActorUID: "u-admin", Action: action,
		ResourceType: "test", ResourceID: "r-1", OccurredAt: time.Now().UTC(), Payload: map[string]any{}}
}

func countMatrixRows(t *testing.T, db *gorm.DB, eventID domain.ID) (ledgerRows, pubRows int64) {
	t.Helper()
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE event_id = ?`, eventID).Scan(&ledgerRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.event_id = ?`, eventID).Scan(&pubRows).Error; err != nil {
		t.Fatal(err)
	}
	return ledgerRows, pubRows
}

// TestAuditWiringMatrixAdminBanBlockCommentMessageFileShare (T-4 matrix for
// the eight B2-1 conversions): every mutation class is table-driven across
// {crash, commit} — crash side (publication insert blocked) yields an error
// and 0 ledger + 0 publication rows; commit side yields 1/1 with the exact
// event_id/action.
func TestAuditWiringMatrixAdminBanBlockCommentMessageFileShare(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		run    func(m *matrixRepo, eventID domain.ID) error
		action string
	}{
		{"admin grant role", func(m *matrixRepo, eventID domain.ID) error {
			return m.admin.GrantRoleWithAudit(ctx, "u-1", "admin", "u-admin", matrixFact(eventID, domain.AuditAdminGrantRole, "tenant-a"))
		}, domain.AuditAdminGrantRole},
		{"admin revoke role", func(m *matrixRepo, eventID domain.ID) error {
			return m.admin.RevokeRoleWithAudit(ctx, "u-1", "admin", matrixFact(eventID, domain.AuditAdminRevokeRole, "tenant-a"))
		}, domain.AuditAdminRevokeRole},
		{"ban user", func(m *matrixRepo, eventID domain.ID) error {
			return m.ban.BanWithAudit(ctx, "u-1", "u-admin", "spam", nil, matrixFact(eventID, domain.AuditUserBan, "tenant-a"))
		}, domain.AuditUserBan},
		{"unban user", func(m *matrixRepo, eventID domain.ID) error {
			return m.ban.UnbanWithAudit(ctx, "u-1", matrixFact(eventID, domain.AuditUserUnban, "tenant-a"))
		}, domain.AuditUserUnban},
		{"block user", func(m *matrixRepo, eventID domain.ID) error {
			return m.block.BlockWithAudit(ctx, "u-1", "u-2", matrixFact(eventID, domain.AuditUserBlock, "tenant-a"))
		}, domain.AuditUserBlock},
		{"unblock user", func(m *matrixRepo, eventID domain.ID) error {
			return m.block.UnblockWithAudit(ctx, "u-1", "u-2", matrixFact(eventID, domain.AuditUserUnblock, "tenant-a"))
		}, domain.AuditUserUnblock},
		{"comment report resolve", func(m *matrixRepo, eventID domain.ID) error {
			commentID := idgen.NewID()
			reportID := idgen.NewID()
			if err := m.db.Exec(`INSERT INTO comments (id, owner_uid, target_type, target_id, content, created_at, updated_at)
				VALUES (?, 'u-1', 'file', ?, 'x', now(), now())`, commentID, idgen.NewID()).Error; err != nil {
				return err
			}
			if err := m.db.Exec(`INSERT INTO comment_reports (id, comment_id, reporter_uid, reason)
				VALUES (?, ?, 'u-2', 'spam')`, reportID, commentID).Error; err != nil {
				return err
			}
			return m.comment.ResolveReportWithAudit(ctx, reportID, domain.ReportStatusResolved,
				"u-admin", time.Now().UTC(), commentID, matrixFact(eventID, domain.AuditReportResolve, "tenant-a"))
		}, domain.AuditReportResolve},
		{"message delete", func(m *matrixRepo, eventID domain.ID) error {
			return m.message.SoftDeleteWithEventAudit(ctx, idgen.NewID(), nil, matrixFact(eventID, domain.AuditMessageDelete, "tenant-a"))
		}, domain.AuditMessageDelete},
		{"file share grant", func(m *matrixRepo, eventID domain.ID) error {
			facts := []*domain.AuditFact{
				matrixFact(eventID, domain.AuditFileShareGrant, "tenant-a"),
				matrixFact(idgen.NewID(), domain.AuditFileShareGrant, "tenant-a"),
			}
			return m.fileShare.GrantWithAudit(ctx, idgen.NewID(), "u-1", []string{"g-1", "g-2"}, facts)
		}, domain.AuditFileShareGrant},
		{"file share revoke", func(m *matrixRepo, eventID domain.ID) error {
			return m.fileShare.RevokeWithAudit(ctx, idgen.NewID(), "g-1", matrixFact(eventID, domain.AuditFileShareRevoke, "tenant-a"))
		}, domain.AuditFileShareRevoke},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Commit side: exactly 1 ledger row + 1 pending publication row.
			m := startMatrixRepo(t)
			eventID := idgen.NewID()
			if err := tc.run(m, eventID); err != nil {
				t.Fatalf("commit side: %v", err)
			}
			ledgerRows, pubRows := countMatrixRows(t, m.db, eventID)
			if ledgerRows != 1 || pubRows != 1 {
				t.Fatalf("commit side rows = %d/%d, want 1/1", ledgerRows, pubRows)
			}
			var action string
			if err := m.db.Raw(`SELECT action FROM audit_ledger WHERE event_id = ?`, eventID).Scan(&action).Error; err != nil {
				t.Fatal(err)
			}
			if action != tc.action {
				t.Fatalf("commit side action = %q, want %q", action, tc.action)
			}

			// Crash side: publication insert blocked -> error + 0/0 rows.
			m2 := startMatrixRepo(t)
			installFailPublicationInsertTrigger(t, m2.db)
			eventID2 := idgen.NewID()
			if err := tc.run(m2, eventID2); err == nil {
				t.Fatal("crash side: expected an error from the blocked publication insert")
			}
			ledgerRows, pubRows = countMatrixRows(t, m2.db, eventID2)
			if ledgerRows != 0 || pubRows != 0 {
				t.Fatalf("crash side rows = %d/%d, want 0/0 (whole tx rolled back)", ledgerRows, pubRows)
			}
		})
	}
}

// TestAuditWiringMatrixFileShareGrantMulti (T-4): an N-grantee grant commits
// N grant rows + N ledger rows + N publication rows atomically; a crash rolls
// all of them back.
func TestAuditWiringMatrixFileShareGrantMulti(t *testing.T) {
	ctx := context.Background()
	m := startMatrixRepo(t)
	fileID := idgen.NewID()
	eventIDs := []domain.ID{idgen.NewID(), idgen.NewID(), idgen.NewID()}
	facts := make([]*domain.AuditFact, 0, 3)
	for _, e := range eventIDs {
		facts = append(facts, matrixFact(e, domain.AuditFileShareGrant, "tenant-a"))
	}
	if err := m.fileShare.GrantWithAudit(ctx, fileID, "u-1", []string{"g-1", "g-2", "g-3"}, facts); err != nil {
		t.Fatal(err)
	}
	var grants int64
	if err := m.db.Raw(`SELECT count(*) FROM file_shares WHERE file_id = ?`, fileID).Scan(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if grants != 3 {
		t.Fatalf("grant rows = %d, want 3", grants)
	}
	for _, e := range eventIDs {
		ledgerRows, pubRows := countMatrixRows(t, m.db, e)
		if ledgerRows != 1 || pubRows != 1 {
			t.Fatalf("event %s rows = %d/%d, want 1/1", e, ledgerRows, pubRows)
		}
	}

	// Crash rolls every row back.
	m2 := startMatrixRepo(t)
	installFailPublicationInsertTrigger(t, m2.db)
	fileID2 := idgen.NewID()
	eventID := idgen.NewID()
	facts2 := []*domain.AuditFact{
		matrixFact(eventID, domain.AuditFileShareGrant, "tenant-a"),
		matrixFact(idgen.NewID(), domain.AuditFileShareGrant, "tenant-a"),
	}
	if err := m2.fileShare.GrantWithAudit(ctx, fileID2, "u-1", []string{"g-1", "g-2"}, facts2); err == nil {
		t.Fatal("expected the blocked publication insert to fail the grant")
	}
	var grants2 int64
	if err := m2.db.Raw(`SELECT count(*) FROM file_shares WHERE file_id = ?`, fileID2).Scan(&grants2).Error; err != nil {
		t.Fatal(err)
	}
	if grants2 != 0 {
		t.Fatalf("grant rows after crash = %d, want 0", grants2)
	}
}
