package service

import (
	"context"
	"errors"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

func (s *accountService) ensure(ctx context.Context) (*domain.Account, error) {
	return s.ensureMode(ctx, false)
}

func (s *accountService) ensureWrite(ctx context.Context) (*domain.Account, error) {
	return s.ensureMode(ctx, true)
}

func (s *accountService) ensureMode(ctx context.Context, write bool) (*domain.Account, error) {
	tenantID, issuer, subject, err := s.verifiedAccountIdentity(ctx)
	if err != nil {
		return nil, err
	}
	homeRegion := s.region
	if s.home != nil {
		homeRegion = s.home.Resolve(issuer, subject)
	}
	account, findErr := s.repo.FindByIdentity(ctx, tenantID, issuer, subject)
	if findErr == nil {
		return s.readyExistingAccount(ctx, tenantID, account, homeRegion, write)
	}
	if !errors.Is(findErr, repository.ErrNotFound) && !errors.Is(findErr, gorm.ErrRecordNotFound) {
		return nil, mapAccountError(findErr, "account.lookup_failed", "failed to resolve account")
	}
	if homeRegion != s.region {
		return nil, apperr.Conflict("account.home_region_required", "account write must be routed to its home region")
	}
	account, err = s.provisionAccount(ctx, tenantID, issuer, subject, homeRegion)
	if err != nil {
		return nil, err
	}
	if s.directory != nil {
		// Record the durable home-region assignment for the new account.
		if err := s.directory.EnsureDirectory(ctx, tenantID, account.ID, homeRegion, s.homeVersion); err != nil {
			return nil, apperr.Internal("region.directory_write_failed", "failed to record the home-region directory").Wrap(err)
		}
	}
	return account, nil
}

// readyExistingAccount validates and fences an already-provisioned account
// before the caller may use it (P6-02): the local region must still hold the
// writer lease for writes, and any pending quota completion is replayed.
func (s *accountService) readyExistingAccount(ctx context.Context, tenantID string,
	account *domain.Account, homeRegion string, write bool) (*domain.Account, error) {
	account, err := validateExistingAccount(account, homeRegion, s.region, write)
	if err != nil {
		return nil, err
	}
	if write {
		// P6-02: durable fencing guard — the local region must still hold
		// the account's writer lease before any mutation commits.
		if err := s.verifyAccountWriteFence(ctx, account); err != nil {
			return nil, err
		}
	}
	if err := s.completePendingAccountQuota(ctx, tenantID, account); err != nil {
		return nil, err
	}
	return account, nil
}

func (s *accountService) verifiedAccountIdentity(ctx context.Context) (string, string, string, error) {
	if _, err := mustActor(ctx); err != nil {
		return "", "", "", err
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return "", "", "", err
	}
	identity, ok := IdentityFrom(ctx)
	if !ok || strings.TrimSpace(identity.Issuer) == "" || strings.TrimSpace(identity.Subject) == "" {
		return "", "", "", apperr.Unauthorized("auth.identity_claims_required", "verified issuer and subject are required")
	}
	if s.issuer != "" && identity.Issuer != s.issuer {
		return "", "", "", apperr.Unauthorized("auth.issuer_mismatch", "token issuer is not accepted")
	}
	return tenantID, identity.Issuer, identity.Subject, nil
}

func validateExistingAccount(account *domain.Account, homeRegion, localRegion string, write bool) (*domain.Account, error) {
	if account.Status == domain.AccountErased {
		return nil, apperr.Forbidden("account.erased", "account is erased")
	}
	if write && homeRegion != localRegion {
		return nil, apperr.Conflict("account.home_region_required", "account write must be routed to its home region")
	}
	return account, nil
}

func (s *accountService) provisionAccount(ctx context.Context, tenantID, issuer, subject, homeRegion string) (*domain.Account, error) {
	if s.quota == nil {
		return s.provisionUnmeteredAccount(ctx, tenantID, issuer, subject, homeRegion)
	}
	quotaRepo, ok := s.repo.(repository.AccountQuotaRepository)
	if !ok {
		return nil, apperr.Unavailable("billing.saga_unavailable", "account quota persistence is unavailable")
	}
	reservationID, err := s.quota.ReserveAccount(ctx, tenantID, issuer, subject)
	if err != nil {
		return nil, err
	}
	account, err := s.provisionAccountWithAudit(ctx, tenantID, issuer, subject, homeRegion, reservationID)
	if err != nil {
		return nil, err
	}
	if err := s.commitAccountQuota(ctx, quotaRepo, tenantID, account, reservationID); err != nil {
		return nil, err
	}
	return account, nil
}

func (s *accountService) provisionUnmeteredAccount(ctx context.Context, tenantID, issuer, subject,
	homeRegion string) (*domain.Account, error) {
	return s.provisionAccountWithAudit(ctx, tenantID, issuer, subject, homeRegion, "")
}

// provisionAccountWithAudit provisions the account and appends the
// account.provisioned fact in the same transaction (A3/A5): the service
// pre-assigns the account ID so the fact (EventID = accountID) is known before
// the tx opens; the repo appends only when this call created the account (A4),
// so retried/concurrent ensures never roll back their own last_seen_at touch.
// ErrAuditDuplicate maps to success (defense-in-depth; unreachable under A4
// gating). A repo that does not implement the audit surface is a test-wiring
// error and fails closed — never a silent plain write.
func (s *accountService) provisionAccountWithAudit(ctx context.Context, tenantID, issuer, subject,
	homeRegion, reservationID string) (*domain.Account, error) {
	if s.audit == nil {
		if reservationID == "" {
			return s.repo.EnsureByIdentity(ctx, tenantID, issuer, subject, subject, homeRegion)
		}
		quotaRepo, ok := s.repo.(repository.AccountQuotaRepository)
		if !ok {
			return nil, apperr.Unavailable("billing.saga_unavailable", "account quota persistence is unavailable")
		}
		return quotaRepo.EnsureByIdentityWithQuota(ctx, tenantID, issuer, subject, subject, homeRegion, reservationID)
	}
	repo, ok := s.repo.(repository.AccountProvisionAuditRepository)
	if !ok {
		return nil, apperr.Internal("account.audit_unavailable", "account provision audit persistence is unavailable")
	}
	accountID := idgen.NewID()
	fact := buildAuditFact(ctx, AuditFactInput{Source: "aero-id", EventID: accountID, ActorUID: subject,
		AccountID: accountID, TenantID: tenantID, Action: "account.provisioned", ResourceType: "account",
		ResourceID: accountID.String(), Payload: map[string]any{"home_region": homeRegion}})
	var (
		account *domain.Account
		err     error
	)
	if reservationID == "" {
		account, err = repo.EnsureByIdentityWithAudit(ctx, tenantID, issuer, subject, subject, homeRegion, accountID, fact)
	} else {
		account, err = repo.EnsureByIdentityWithQuotaWithAudit(ctx, tenantID, issuer, subject, subject, homeRegion, reservationID, accountID, fact)
	}
	if err != nil {
		if errors.Is(err, repository.ErrAuditDuplicate) {
			return account, nil
		}
		return nil, mapAccountError(err, "account.ensure_failed", "failed to resolve account")
	}
	return account, nil
}

func (s *accountService) completePendingAccountQuota(ctx context.Context, tenantID string, account *domain.Account) error {
	if s.quota == nil {
		return nil
	}
	quotaRepo, ok := s.repo.(repository.AccountQuotaRepository)
	if !ok {
		return apperr.Unavailable("billing.saga_unavailable", "account quota persistence is unavailable")
	}
	reservationID, err := quotaRepo.PendingAccountQuota(ctx, tenantID, account.ID)
	if errors.Is(err, repository.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return mapAccountError(err, "billing.saga_lookup_failed", "failed to resolve account quota state")
	}
	return s.commitAccountQuota(ctx, quotaRepo, tenantID, account, reservationID)
}

func (s *accountService) commitAccountQuota(ctx context.Context, quotaRepo repository.AccountQuotaRepository,
	tenantID string, account *domain.Account, reservationID string) error {
	if err := s.quota.CommitAccount(ctx, tenantID, reservationID, account.ID); err != nil {
		return err
	}
	if err := quotaRepo.MarkAccountQuotaCommitted(ctx, tenantID, account.ID, reservationID); err != nil {
		return mapAccountError(err, "billing.saga_commit_failed", "failed to persist account quota state")
	}
	return nil
}
