package service

import (
	"context"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

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
