//go:build integration

package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

// REQ-3 (2026-08-08-internal-comment-audit-swallow-requirements): prove that
// commentRepo.ResolveReportWithAudit commits report resolve + conditional
// comment hide + the anchored audit fact in ONE transaction. The failure leg
// injects an unconfigured ledger — AppendInTx fails deterministically at its
// first statement (audit_ledger.go:131, "audit ledger signing key is not
// configured", before any SQL) — and asserts the final rows are unchanged via
// explicit SELECTs, not just error propagation. This is the atomicity the
// deleted dead-tree flow (two repo calls, then a post-commit `_ = s.audit.Log(`)
// could never provide.

func startCommentAuditRepo(t *testing.T, signingKey string) (*gorm.DB, repository.CommentAuditRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: signingKey})
	repo := repository.NewCommentRepository(db, nil, ledger).(repository.CommentAuditRepository)
	return db, repo
}

// seedOpenReport inserts a normal comment plus an open report against it and
// returns both IDs.
func seedOpenReport(t *testing.T, db *gorm.DB) (commentID, reportID domain.ID) {
	t.Helper()
	commentID = idgen.NewID()
	reportID = idgen.NewID()
	if err := db.Exec(`INSERT INTO comments (id, owner_uid, target_type, target_id, content, created_at, updated_at)
		VALUES (?, 'u-owner', 'file', ?, 'spammy content', now(), now())`, commentID, idgen.NewID()).Error; err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	if err := db.Exec(`INSERT INTO comment_reports (id, comment_id, reporter_uid, reason, status)
		VALUES (?, ?, 'u-reporter', 'spam', ?)`, reportID, commentID, int16(domain.ReportStatusOpen)).Error; err != nil {
		t.Fatalf("seed report: %v", err)
	}
	return commentID, reportID
}

// resolveFact builds the anchored fact the live service would append: EventID
// == reportID (the retry-dedup anchor contract).
func resolveFact(reportID domain.ID) *domain.AuditFact {
	return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "tenant-a:moderator-1",
		TenantID: "tenant-a", EventID: reportID, Action: domain.AuditReportResolve,
		ResourceType: "comment_report", ResourceID: reportID.String(), OccurredAt: time.Now().UTC()}
}

func assertReportState(t *testing.T, db *gorm.DB, reportID domain.ID, wantStatus domain.ReportStatus, wantResolver string, wantResolved bool) {
	t.Helper()
	var status int16
	var resolvedAt *time.Time
	var resolvedBy sql.NullString
	row := db.Raw(`SELECT status, resolved_at, resolved_by FROM comment_reports WHERE id = ?`, reportID).Row()
	if err := row.Scan(&status, &resolvedAt, &resolvedBy); err != nil {
		t.Fatalf("read report state: %v", err)
	}
	if status != int16(wantStatus) {
		t.Fatalf("report status = %d, want %d", status, wantStatus)
	}
	if (resolvedAt != nil) != wantResolved {
		t.Fatalf("report resolved_at set = %v, want %v", resolvedAt != nil, wantResolved)
	}
	if resolvedBy.Valid != wantResolved {
		t.Fatalf("report resolved_by set = %v, want %v", resolvedBy.Valid, wantResolved)
	}
	if wantResolved && resolvedBy.String != wantResolver {
		t.Fatalf("report resolved_by = %q, want %q", resolvedBy.String, wantResolver)
	}
}

func assertCommentState(t *testing.T, db *gorm.DB, commentID domain.ID, want domain.CommentStatus) {
	t.Helper()
	var status int16
	if err := db.Raw(`SELECT status FROM comments WHERE id = ?`, commentID).Scan(&status).Error; err != nil {
		t.Fatalf("read comment state: %v", err)
	}
	if status != int16(want) {
		t.Fatalf("comment status = %d, want %d", status, want)
	}
}

// TestResolveReportWithAuditRollsBackOnFactAppendFailure (REQ-3 failure leg):
// an append failure must roll the resolve and the hide back with it — report
// still open, comment still visible, zero ledger and zero publication rows.
func TestResolveReportWithAuditRollsBackOnFactAppendFailure(t *testing.T) {
	ctx := context.Background()
	db, repo := startCommentAuditRepo(t, "")
	commentID, reportID := seedOpenReport(t, db)

	err := repo.ResolveReportWithAudit(ctx, reportID, domain.ReportStatusResolved, "u-admin",
		time.Now().UTC(), commentID, resolveFact(reportID))
	if err == nil {
		t.Fatal("ResolveReportWithAudit with an unconfigured ledger: err = nil, want append failure")
	}
	if !strings.Contains(err.Error(), "signing key is not configured") {
		t.Fatalf("err = %v, want the empty-signing-key injection to fire", err)
	}
	assertReportState(t, db, reportID, domain.ReportStatusOpen, "", false)
	assertCommentState(t, db, commentID, domain.CommentStatusNormal)
	if n := countAuditLedgerRows(t, db, reportID); n != 0 {
		t.Fatalf("ledger rows = %d, want 0 (fact must not commit without the domain rows)", n)
	}
	if n := countAuditPublicationRows(t, db, reportID); n != 0 {
		t.Fatalf("publication rows = %d, want 0", n)
	}
}

// TestResolveReportWithAuditCommitsResolveHideFactInOneTx (REQ-3 happy leg):
// report terminal, comment hidden, exactly one ledger row + one publication
// row whose EventID is the report ID (anchor preserved).
func TestResolveReportWithAuditCommitsResolveHideFactInOneTx(t *testing.T) {
	ctx := context.Background()
	db, repo := startCommentAuditRepo(t, "comment-audit-key-0123456789abcdef")
	commentID, reportID := seedOpenReport(t, db)

	if err := repo.ResolveReportWithAudit(ctx, reportID, domain.ReportStatusResolved, "u-admin",
		time.Now().UTC(), commentID, resolveFact(reportID)); err != nil {
		t.Fatalf("ResolveReportWithAudit: %v", err)
	}
	assertReportState(t, db, reportID, domain.ReportStatusResolved, "u-admin", true)
	assertCommentState(t, db, commentID, domain.CommentStatusHidden)
	if n := countAuditLedgerRows(t, db, reportID); n != 1 {
		t.Fatalf("ledger rows = %d, want exactly 1", n)
	}
	if n := countAuditPublicationRows(t, db, reportID); n != 1 {
		t.Fatalf("publication rows = %d, want exactly 1 (one publication per fact)", n)
	}
	var action, resourceID string
	row := db.Raw(`SELECT action, resource_id FROM audit_ledger WHERE event_id = ?`, reportID).Row()
	if err := row.Scan(&action, &resourceID); err != nil {
		t.Fatalf("read ledger row: %v", err)
	}
	if action != domain.AuditReportResolve || resourceID != reportID.String() {
		t.Fatalf("ledger row = %s/%s, want %s/%s (anchor preserved)", action, resourceID,
			domain.AuditReportResolve, reportID.String())
	}
}

// TestResolveReportWithAuditNotFoundAppendsNoFact (REQ-3 not-found leg): an
// unknown reportID returns ErrNotFound and appends no fact and hides nothing.
func TestResolveReportWithAuditNotFoundAppendsNoFact(t *testing.T) {
	ctx := context.Background()
	db, repo := startCommentAuditRepo(t, "comment-audit-key-0123456789abcdef")
	commentID, _ := seedOpenReport(t, db)
	missingID := idgen.NewID()

	err := repo.ResolveReportWithAudit(ctx, missingID, domain.ReportStatusResolved, "u-admin",
		time.Now().UTC(), commentID, resolveFact(missingID))
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	assertCommentState(t, db, commentID, domain.CommentStatusNormal)
	if n := countAuditLedgerRows(t, db, missingID); n != 0 {
		t.Fatalf("ledger rows = %d, want 0", n)
	}
	if n := countAuditPublicationRows(t, db, missingID); n != 0 {
		t.Fatalf("publication rows = %d, want 0", n)
	}
}
