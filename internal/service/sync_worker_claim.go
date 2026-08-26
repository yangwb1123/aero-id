package service

import (
	"context"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

// Source calls are bounded at five minutes by the connector contract. Keep a
// finish/persistence margin in the durable job lease so a healthy worker is
// not reclaimed while recording the source outcome.
const (
	syncRemoteTimeout = 5 * time.Minute
	syncLeaseDuration = 6 * time.Minute
)

func syncRemoteContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, syncRemoteTimeout)
}

func (s *syncJobService) claimNext(ctx context.Context, worker string, lease time.Duration) (*domain.SyncJob, error) {
	if regional, ok := s.repo.(repository.RegionalSyncJobWorkerRepository); ok {
		return regional.ClaimNextForRegion(ctx, worker, lease, s.region)
	}
	return s.repo.ClaimNext(ctx, worker, lease)
}

func (s *syncJobService) claimUnknown(ctx context.Context, worker string, lease time.Duration) (*domain.SyncJob, error) {
	if regional, ok := s.repo.(repository.RegionalSyncJobWorkerRepository); ok {
		return regional.ClaimUnknownNextForRegion(ctx, worker, lease, s.region)
	}
	return s.repo.ClaimUnknownNext(ctx, worker, lease)
}

func (s *syncJobService) claimUnknownNext(ctx context.Context, worker string, lease time.Duration) (*domain.SyncJob, error) {
	return s.claimUnknown(ctx, worker, lease)
}
