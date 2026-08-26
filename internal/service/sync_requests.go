package service

import (
	"context"
	"errors"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

func validSyncJobType(value domain.SyncJobType) bool {
	switch value {
	case domain.SyncJobSync, domain.SyncJobReconcile, domain.SyncJobExport, domain.SyncJobErase, domain.SyncJobAuditExport:
		return true
	default:
		return false
	}
}

func syncSummaryRequest(account *domain.Account, job *domain.SyncJob, datasets []domain.AccountDataset) connector.SummaryRequest {
	return connector.SummaryRequest{AccountID: account.ID, CanonicalUID: account.CanonicalUID, ActorUID: job.ActorUID,
		TenantID: job.TenantID, RequestID: job.RequestID, Region: account.HomeRegion, OperationID: job.ID.String(),
		Datasets: datasets, Reconcile: job.JobType == domain.SyncJobReconcile}
}

func eraseRequestForJob(account *domain.Account, job *domain.SyncJob, source connector.Source,
	datasets []domain.AccountDataset) connector.EraseRequest {
	return connector.EraseRequest{AccountID: account.ID, CanonicalUID: account.CanonicalUID, ActorUID: job.ActorUID,
		TenantID: job.TenantID, RequestID: job.RequestID, Region: account.HomeRegion, Datasets: datasets,
		IdempotencyKey: syncEraseIdempotencyKey(job, source), OperationID: job.ID.String()}
}

func syncEraseIdempotencyKey(job *domain.SyncJob, source connector.Source) string {
	if job == nil {
		return ""
	}
	return job.IdempotencyKey + ":" + string(source)
}

func isUnknownRemote(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	remote, ok := connector.IsRemote(err)
	if !ok {
		return false
	}
	return remote.Kind == connector.ErrorTimeout || remote.Kind == connector.ErrorUnavailable ||
		remote.Kind == connector.ErrorUnknown
}

func eraseCallOutcome(err error) (string, bool, bool) {
	if isUnknownRemote(err) {
		return "unknown", true, false
	}
	return "failed", false, true
}
