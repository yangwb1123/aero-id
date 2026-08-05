package service

import (
	"context"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

//nolint:cyclop // source projection persistence applies version, freshness, and partial-result rules together.
func (s *syncJobService) persistSyncSummary(ctx context.Context, job *domain.SyncJob, account *domain.Account,
	source connector.Source, requested []domain.AccountDataset, summary *connector.Summary) error {
	if summary == nil {
		return &connector.RemoteError{Source: source, Kind: connector.ErrorInvalid, Code: "source.empty_summary"}
	}
	for _, snapshot := range summary.Snapshots {
		if snapshot == nil {
			continue
		}
		snapshot.AccountID = job.AccountID
		if err := s.snapshots.UpsertSnapshot(ctx, snapshot); err != nil {
			return err
		}
	}
	if job.JobType == domain.SyncJobReconcile && summary.Complete {
		if err := markReconciledSnapshots(ctx, s.snapshots, account.ID, string(source), requested,
			summary.Snapshots, summary.Version); err != nil {
			return err
		}
	}
	for _, sourceRef := range summary.Sources {
		if sourceRef == nil {
			continue
		}
		sourceRef.AccountID = job.AccountID
		if err := s.accounts.UpsertSource(ctx, sourceRef); err != nil {
			return err
		}
	}
	for _, membership := range summary.Memberships {
		if membership == nil {
			continue
		}
		membership.AccountID = job.AccountID
		if err := s.accounts.UpsertMembership(ctx, membership); err != nil {
			return err
		}
	}
	if job.JobType == domain.SyncJobReconcile && summary.Complete {
		return markReconciledReferences(ctx, s.accountRepo, account.ID, string(source),
			summary.Sources, summary.Memberships, summary.Version)
	}
	return nil
}
