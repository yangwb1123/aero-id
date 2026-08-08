package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// CommentAuditRepository is the in-transaction audit surface for moderation
// mutations (B2-1): report resolution plus its conditional comment hide plus
// the audit fact commit in one gorm transaction.
type CommentAuditRepository interface {
	// ResolveReportWithAudit transitions a report to a terminal status and,
	// when hideCommentID is non-zero, hides that comment in the same
	// transaction; the audit fact is appended last. A report update that
	// affects 0 rows returns ErrNotFound (same as the plain ResolveReport) and
	// appends no fact.
	ResolveReportWithAudit(ctx context.Context, reportID domain.ID, status domain.ReportStatus,
		resolverUID string, at time.Time, hideCommentID domain.ID, fact *domain.AuditFact) error
}

// ResolveReportWithAudit merges the legacy two-call service flow (report update
// + conditional comment hide) into one transaction with the fact: a failing
// conditional hide now rolls the resolve back with it (intended atomicity
// change, design REQ-3.4). Report-not-found still wins when both fail.
func (r *commentRepo) ResolveReportWithAudit(ctx context.Context, reportID domain.ID, status domain.ReportStatus,
	resolverUID string, at time.Time, hideCommentID domain.ID, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&commentReportRow{}).Where("id = ?", reportID).
			Updates(map[string]any{"status": int16(status), "resolved_at": at, "resolved_by": resolverUID})
		if res.Error != nil {
			return fmt.Errorf("resolve comment report: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		if hideCommentID != (domain.ID{}) {
			if err := tx.Model(&commentRow{}).Where("id = ?", hideCommentID).
				UpdateColumn("status", int16(domain.CommentStatusHidden)).Error; err != nil {
				return fmt.Errorf("set comment status: %w", err)
			}
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append comment audit: %w", err)
		}
		return nil
	})
}
