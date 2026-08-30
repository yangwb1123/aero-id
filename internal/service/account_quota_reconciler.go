package service

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

const accountQuotaWorkerLease = time.Minute

// StartAccountQuotaReconciler repairs both durable release intents and local
// pending quota rows. It is deliberately driven only by database state: a
// process crash between a source call and its acknowledgement is therefore a
// normal restart case, not a permanently stuck account.
func (s *accountService) StartAccountQuotaReconciler(ctx context.Context, interval time.Duration) func() {
	work, workOK := s.quota.(AccountQuotaWorkCoordinator)
	pending, pendingOK := s.repo.(repository.PendingAccountQuotaRepository)
	if !workOK && !pendingOK {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = time.Second
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(done)
		for {
			select {
			case <-ticker.C:
				s.reconcileQuota(workerCtx, work, pending)
			case <-workerCtx.Done():
				return
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (s *accountService) reconcileQuota(ctx context.Context, work AccountQuotaWorkCoordinator,
	pending repository.PendingAccountQuotaRepository) {
	if work != nil {
		s.reconcileSagaWork(ctx, work)
	}
	if pending != nil {
		s.reconcilePendingQuotaRows(ctx, pending)
	}
}

func (s *accountService) reconcileSagaWork(ctx context.Context, work AccountQuotaWorkCoordinator) {
	for i := 0; i < 8; i++ {
		attempt, err := work.ClaimAccountQuotaWork(ctx, "account-quota-reconciler", accountQuotaWorkerLease)
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, repository.ErrNotFound) || err != nil {
			return
		}
		s.reconcileQuotaAttempt(ctx, work, attempt)
	}
}

func (s *accountService) reconcilePendingQuotaRows(ctx context.Context, pending repository.PendingAccountQuotaRepository) {
	rows, err := pending.ListPendingAccountQuotas(ctx, 32)
	if err != nil {
		return
	}
	quotaRepo, ok := s.repo.(repository.AccountQuotaRepository)
	if !ok {
		return
	}
	for _, row := range rows {
		account, getErr := s.repo.GetByID(ctx, row.TenantID, row.AccountID)
		if getErr == nil {
			_ = s.commitAccountQuota(ctx, quotaRepo, row.TenantID, account, row.ReservationID)
		}
	}
}

func (s *accountService) reconcileQuotaAttempt(ctx context.Context, work AccountQuotaWorkCoordinator,
	attempt *repository.AccountQuotaSaga) {
	if attempt == nil {
		return
	}
	if attempt.State == repository.QuotaSagaReleasePending {
		s.finishReleasePending(ctx, work, attempt)
		return
	}
	account, err := s.repo.FindByIdentity(ctx, attempt.TenantID, attempt.Issuer, attempt.Subject)
	if err == nil && account != nil {
		s.reconcileExistingQuota(ctx, work, attempt, account)
		return
	}
	if !errors.Is(err, repository.ErrNotFound) && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.finishQuotaWork(ctx, work, attempt, false, err)
		return
	}
	s.reconcileMissingQuota(ctx, work, attempt)
}

func (s *accountService) finishReleasePending(ctx context.Context, work AccountQuotaWorkCoordinator,
	attempt *repository.AccountQuotaSaga) {
	err := s.releaseQuota(ctx, attempt.TenantID, attempt.ReservationID)
	s.finishQuotaWork(ctx, work, attempt, err == nil, err)
}

func (s *accountService) reconcileExistingQuota(ctx context.Context, work AccountQuotaWorkCoordinator,
	attempt *repository.AccountQuotaSaga, account *domain.Account) {
	quotaRepo, ok := s.repo.(repository.AccountQuotaRepository)
	if !ok {
		return
	}
	reservationID, pendingErr := quotaRepo.PendingAccountQuota(ctx, attempt.TenantID, account.ID)
	if pendingErr == nil && reservationID == attempt.ReservationID {
		_ = s.commitAccountQuota(ctx, quotaRepo, attempt.TenantID, account, reservationID)
		return
	}
	if s.reconcileCommittedQuota(ctx, attempt, account, pendingErr) {
		return
	}
	// An account is evidence against compensation, even if the quota lookup is
	// temporarily unavailable. Leave the fenced attempt for a later pass.
	s.finishQuotaWork(ctx, work, attempt, false, pendingErr)
}

func (s *accountService) reconcileCommittedQuota(ctx context.Context, attempt *repository.AccountQuotaSaga,
	account *domain.Account, pendingErr error) bool {
	if !errors.Is(pendingErr, repository.ErrNotFound) && !errors.Is(pendingErr, gorm.ErrRecordNotFound) {
		return false
	}
	statusRepo, ok := s.repo.(repository.AccountQuotaStatusRepository)
	if !ok {
		return false
	}
	status, err := statusRepo.AccountQuotaStatus(ctx, attempt.TenantID, account.ID)
	if err != nil {
		return false
	}
	if coordinator, ok := s.quota.(AccountQuotaSagaCoordinator); ok {
		switch status {
		case "committed":
			_ = coordinator.MarkAccountQuotaCommitted(ctx, attempt.TenantID, attempt.ReservationID)
			return true
		case "expired":
			_ = coordinator.MarkAccountQuotaExpired(ctx, attempt.TenantID, attempt.ReservationID)
			return true
		}
	}
	return false
}

func (s *accountService) reconcileMissingQuota(ctx context.Context, work AccountQuotaWorkCoordinator,
	attempt *repository.AccountQuotaSaga) {
	coordinator, ok := s.quota.(AccountQuotaSagaCoordinator)
	if !ok {
		return
	}
	scheduled, err := coordinator.ScheduleAccountQuotaReleaseOwned(ctx, attempt.ID, attempt.LeaseGeneration)
	if err != nil || !scheduled {
		return
	}
	releaseErr := s.releaseQuota(ctx, attempt.TenantID, attempt.ReservationID)
	s.finishQuotaWork(ctx, work, attempt, releaseErr == nil, releaseErr)
}

func (s *accountService) finishQuotaWork(ctx context.Context, work AccountQuotaWorkCoordinator,
	attempt *repository.AccountQuotaSaga, released bool, workErr error) {
	_ = work.FinishAccountQuotaWork(context.WithoutCancel(ctx), attempt.LeaseOwner, attempt.ID,
		attempt.LeaseGeneration, released, workErr)
}

func (s *accountService) releaseQuota(ctx context.Context, tenantID, reservationID string) error {
	releaseCtx, cancel := quotaCompensationContext(ctx, s.quotaReleaseTimeout)
	defer cancel()
	return s.quota.ReleaseAccount(releaseCtx, tenantID, reservationID)
}
