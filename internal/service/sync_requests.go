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

func syncSummaryRequest(ctx context.Context, accounts connector.AuthoritativeAccountLoader, account *domain.Account,
	job *domain.SyncJob, source connector.Source, datasets []domain.AccountDataset) (connector.SummaryRequest, error) {
	var empty connector.SummaryRequest
	if job == nil {
		return empty, errors.New("sync job is required")
	}
	if source == connector.SourceAeroIM || source == connector.SourceAeroVault {
		return connector.NewTrustedSummaryRequest(ctx, accounts, connector.TrustedSummaryRequestInput{
			Source: source, TenantID: job.TenantID, AccountID: job.AccountID, ActorUID: job.ActorUID,
			RequestID: job.RequestID, OperationID: job.ID.String(), Datasets: datasets,
			Reconcile: job.JobType == domain.SyncJobReconcile,
		})
	}
	// Non-bound sources retain their compatibility request shape. They are not
	// allowed to opt bound sources out of authoritative account loading.
	if account == nil {
		return empty, errors.New("sync account is required")
	}
	return compatibilitySummaryRequest(source, account, job.ActorUID, job.TenantID, job.RequestID,
		job.ID.String(), datasets, job.JobType == domain.SyncJobReconcile)
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
