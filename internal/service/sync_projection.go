package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	if errors.Is(err, repository.ErrAccountErased) {
		return "erased", "account.erased", false
	}
	var summaryErr *repository.SummaryApplicationError
	if errors.As(err, &summaryErr) && summaryErr.Kind == repository.SummaryApplyCommitUnknown {
		return "unknown", "projection.commit_unknown", true
	}
	return "projection_error", "projection.write_failed", false
}

//nolint:cyclop // persistence capability, ambiguity classification, and replay resolution are one atomic protocol.
func (s *syncJobService) persistSyncSummary(ctx context.Context, job *domain.SyncJob, account *domain.Account,
	source connector.Source, requested []domain.AccountDataset, summary *connector.Summary) error {
	if summary == nil {
		return &connector.RemoteError{Source: source, Kind: connector.ErrorInvalid, Code: "source.empty_summary"}
	}
	if s.application == nil {
		return repository.ErrSummaryApplicationUnavailable
	}
	request := redactSummaryRequest(summaryApplicationRequest(job, account, source, requested, summary))
	recovery, hasRecovery := s.repo.(repository.SyncJobRecoveryRepository)
	if hasRecovery {
		persistCtx := repository.WithSyncJobWorker(ctx, job.LeaseOwner)
		if err := recovery.SaveSummaryReplay(persistCtx, repository.SummaryReplayInput{JobID: job.ID,
			Source: string(source), ExecutionAttempt: job.Attempt, Request: request}); err != nil {
			if errors.Is(err, repository.ErrSyncReplayConflict) || errors.Is(err, repository.ErrSyncReplayStateInvalid) {
				return err
			}
			// A transaction error may have happened after the insert committed.
			// Do not apply a projection without knowing whether the replay row
			// exists; recovery will inspect the durable row and fail closed if it
			// is absent.
			return &repository.SummaryApplicationError{Kind: repository.SummaryApplyCommitUnknown, Cause: err}
		}
	}
	if err := s.checkSyncExecution(ctx, job, false); err != nil {
		return err
	}
	if err := s.applySyncSummary(ctx, job, request, hasRecovery); err != nil {
		return err
	}
	if hasRecovery {
		_, digest, digestErr := summaryRequestDigest(request)
		if digestErr != nil {
			return digestErr
		}
		if err := recovery.ResolveSummaryReplay(repository.WithSyncJobWorker(ctx, job.LeaseOwner), job.ID,
			string(source), job.Attempt, digest); err != nil {
			// Projection application already returned success, but the durable
			// replay receipt is unresolved. Treat the combined outcome as unknown
			// so the recovery worker, not a fresh source read, resolves it.
			return &repository.SummaryApplicationError{Kind: repository.SummaryApplyCommitUnknown, Cause: err}
		}
	}
	return nil
}

func (s *syncJobService) applySyncSummary(ctx context.Context, job *domain.SyncJob, request domain.SummaryApplicationRequest, hasRecovery bool) error {
	_, err := s.application.ApplySummary(ctx, request)
	var applyErr *repository.SummaryApplicationError
	if hasRecovery && errors.As(err, &applyErr) && applyErr.Kind == repository.SummaryApplyCommitUnknown {
		if stateRepo, ok := s.repo.(repository.SummaryReplayStateRepository); ok {
			markCtx := syncRecoveryContext(ctx, job.LeaseOwner)
			if stateErr := stateRepo.MarkSummaryReplayUnknown(markCtx, job.ID, request.Source, job.Attempt); stateErr != nil {
				return errors.Join(err, stateErr)
			}
		}
	}
	return err
}

func summaryApplicationRequest(job *domain.SyncJob, account *domain.Account, source connector.Source,
	requested []domain.AccountDataset, summary *connector.Summary) domain.SummaryApplicationRequest {
	accountID := job.AccountID
	if accountID == (domain.ID{}) && account != nil {
		accountID = account.ID
	}
	return domain.SummaryApplicationRequest{AccountID: accountID, Source: string(source), SourceRegion: summary.SourceRegion,
		Version: summary.Version, RequestedDatasets: append([]domain.AccountDataset(nil), requested...), Coverage: summary.Coverage,
		Complete: summary.Complete, SkippedDatasets: append([]string(nil), summary.SkippedDatasets...),
		Reconcile: job.JobType == domain.SyncJobReconcile, Snapshots: summary.Snapshots, Sources: summary.Sources, Memberships: summary.Memberships}
}

func summaryRequestDigest(request domain.SummaryApplicationRequest) ([]byte, string, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}
