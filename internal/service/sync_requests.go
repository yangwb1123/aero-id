package service

import (
	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

func validSyncJobType(value domain.SyncJobType) bool {
	switch value {
	case domain.SyncJobSync, domain.SyncJobReconcile, domain.SyncJobExport, domain.SyncJobErase:
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
		TenantID: job.TenantID, RequestID: job.RequestID, Datasets: datasets,
		IdempotencyKey: job.IdempotencyKey + ":" + string(source), OperationID: job.ID.String()}
}

func isUnknownRemote(err error) bool {
	remote, ok := connector.IsRemote(err)
	if !ok {
		return false
	}
	return remote.Kind == connector.ErrorTimeout || remote.Kind == connector.ErrorUnavailable ||
		remote.Kind == connector.ErrorUnknown
}
