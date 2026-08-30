package service

import (
	"context"
	"errors"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

func summaryIsComplete(summary *connector.Summary) bool {
	return summary != nil && summary.Complete && len(summary.SkippedDatasets) == 0
}

func syncSummaryStatus(summary *connector.Summary) (string, bool) {
	if !summaryIsComplete(summary) {
		return "partial", true
	}
	return "synced", false
}

func summaryPersistenceStatus(err error) (string, string, bool) {
	var summaryErr *repository.SummaryApplicationError
	if errors.As(err, &summaryErr) && summaryErr.Kind == repository.SummaryApplyCommitUnknown {
		return "unknown", "projection.commit_unknown", true
	}
	return "projection_error", "projection.write_failed", false
}

func (s *syncJobService) persistSyncSummary(ctx context.Context, job *domain.SyncJob, account *domain.Account,
	source connector.Source, requested []domain.AccountDataset, summary *connector.Summary) error {
	if summary == nil {
		return &connector.RemoteError{Source: source, Kind: connector.ErrorInvalid, Code: "source.empty_summary"}
	}
	if s.application == nil {
		return repository.ErrSummaryApplicationUnavailable
	}
	// The job's tenant-scoped account ID is the lookup key for bound sources;
	// never let a stale caller-held account redirect projection writes.
	accountID := job.AccountID
	if accountID == (domain.ID{}) && account != nil {
		accountID = account.ID
	}
	_, err := s.application.ApplySummary(ctx, domain.SummaryApplicationRequest{
		AccountID: accountID, Source: string(source), SourceRegion: summary.SourceRegion, Version: summary.Version,
		RequestedDatasets: append([]domain.AccountDataset(nil), requested...), Coverage: summary.Coverage,
		Complete: summary.Complete, SkippedDatasets: append([]string(nil), summary.SkippedDatasets...),
		Reconcile: job.JobType == domain.SyncJobReconcile, Snapshots: summary.Snapshots,
		Sources: summary.Sources, Memberships: summary.Memberships,
	})
	return err
}
