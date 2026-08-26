package service

import (
	"context"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

const (
	auditExportContinuationRequestKey = "audit_export_continuation_job_id"
	auditExportContinuationCursorKey  = "_audit_export_next_cursor"
)

func auditExportContinuationKey(parentID domain.ID) string {
	return "audit-export-continuation:" + parentID.String()
}

func (s *syncJobService) loadAuditExportContinuation(ctx context.Context, parentID domain.ID,
	actor, tenant string, accountID domain.ID) (*domain.SyncJob, error) {
	parent, err := s.repo.Get(ctx, parentID)
	if err != nil || !validAuditExportParent(parent, actor, tenant, accountID) {
		return nil, apperr.BadRequest("sync.invalid_continuation", "audit export continuation is invalid")
	}
	if _, ok := auditExportContinuationCursor(parent); !ok {
		return nil, apperr.Conflict("sync.continuation_exhausted", "audit export has no remaining window")
	}
	return parent, nil
}

// validAuditExportParent accepts completed windows and bounded partial windows
// that retained a continuation cursor after artifact delivery was unavailable.
func validAuditExportParent(parent *domain.SyncJob, actor, tenant string, accountID domain.ID) bool {
	return parent != nil && parent.JobType == domain.SyncJobAuditExport &&
		(parent.Status == domain.SyncJobCompleted || parent.Status == domain.SyncJobPartiallyComplete) &&
		parent.ActorUID == actor && parent.TenantID == tenant && parent.AccountID == accountID
}

func auditExportContinuationCursor(job *domain.SyncJob) (string, bool) {
	if job == nil || job.Result == nil {
		return "", false
	}
	cursor, ok := job.Result[auditExportContinuationCursorKey].(string)
	cursor = strings.TrimSpace(cursor)
	return cursor, ok && cursor != ""
}

func auditExportJobReferences(job *domain.SyncJob, parentID domain.ID) bool {
	if job == nil || job.Request == nil {
		return false
	}
	raw, ok := job.Request[auditExportContinuationRequestKey].(string)
	if !ok {
		return false
	}
	id, err := idgen.Parse(raw)
	return err == nil && id == parentID
}

func (s *syncJobService) auditExportStartCursor(ctx context.Context, job *domain.SyncJob) (string, error) {
	if job == nil || job.Request == nil {
		return "", nil
	}
	raw, ok := job.Request[auditExportContinuationRequestKey].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return "", apperr.Internal("audit_export.invalid_continuation", "audit export continuation state is invalid")
	}
	parentID, err := idgen.Parse(raw)
	if err != nil {
		return "", apperr.Internal("audit_export.invalid_continuation", "audit export continuation state is invalid")
	}
	parent, err := s.loadAuditExportContinuation(ctx, parentID, job.ActorUID, job.TenantID, job.AccountID)
	if err != nil {
		return "", err
	}
	cursor, ok := auditExportContinuationCursor(parent)
	if !ok {
		return "", apperr.Internal("audit_export.invalid_continuation", "audit export continuation state is invalid")
	}
	return cursor, nil
}
