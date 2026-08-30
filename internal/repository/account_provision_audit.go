package repository

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// AccountProvisionAuditRepository is the in-transaction audit surface for
// account provisioning. accountID is pre-assigned by the service and threaded
// into the candidate account row so the audit fact (EventID = accountID) is
// known before the transaction opens; the fact is appended ONLY when this call
// actually created the account (bindAccountIdentity RowsAffected == 1). Repeat
// or concurrent losers take the existing-identity path (last_seen_at touch,
// candidate deleted) and append nothing, so a retried or racing provision can
// never roll back its own touch with ErrAuditDuplicate.
type AccountProvisionAuditRepository interface {
	EnsureByIdentityWithAudit(ctx context.Context, tenantID, issuer, subject, canonicalUID, homeRegion string,
		accountID domain.ID, fact *domain.AuditFact) (*domain.Account, error)
	EnsureByIdentityWithQuotaWithAudit(ctx context.Context, tenantID, issuer, subject, canonicalUID, homeRegion,
		reservationID string, accountID domain.ID, fact *domain.AuditFact) (*domain.Account, error)
}

// EnsureByIdentityWithAudit provisions an account and appends the
// account.provisioned fact in the same transaction. The plain
// EnsureByIdentity keeps its exact behavior (zero accountID generates one).
func (r *accountRepo) EnsureByIdentityWithAudit(ctx context.Context, tenantID, issuer, subject,
	canonicalUID, homeRegion string, accountID domain.ID, fact *domain.AuditFact) (*domain.Account, error) {
	input, err := normalizeAccountIdentity(tenantID, issuer, subject, canonicalUID, homeRegion)
	if err != nil {
		return nil, err
	}
	var result *domain.Account
	var callbackErr error
	commitErr := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var created bool
		result, created, callbackErr = ensureAccountIdentityTx(tx, input, accountID)
		if callbackErr != nil {
			return callbackErr
		}
		if created {
			if callbackErr = r.audit.AppendInTx(ctx, tx, fact); callbackErr != nil {
				return fmt.Errorf("append account provision audit: %w", callbackErr)
			}
		}
		return nil
	})
	return result, transactionOutcome(commitErr, callbackErr)
}

// EnsureByIdentityWithQuotaWithAudit is the metered variant (A3): the quota
// reservation row shares the same transaction as the account row and the fact.
func (r *accountRepo) EnsureByIdentityWithQuotaWithAudit(ctx context.Context, tenantID, issuer, subject,
	canonicalUID, homeRegion, reservationID string, accountID domain.ID, fact *domain.AuditFact) (*domain.Account, error) {
	input, err := normalizeAccountIdentity(tenantID, issuer, subject, canonicalUID, homeRegion)
	reservationID = strings.TrimSpace(reservationID)
	if err != nil || reservationID == "" {
		return nil, fmt.Errorf("account quota identity fields must not be blank")
	}
	var result *domain.Account
	var callbackErr error
	commitErr := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var created bool
		result, created, callbackErr = ensureAccountIdentityTx(tx, input, accountID)
		if callbackErr != nil {
			return callbackErr
		}
		if callbackErr = ensureAccountQuotaTx(tx, input.tenantID, result.ID, reservationID); callbackErr != nil {
			return callbackErr
		}
		if callbackErr = markQuotaSagaPersistedTx(tx, input.tenantID, reservationID, result.ID); callbackErr != nil {
			return callbackErr
		}
		if created {
			if callbackErr = r.audit.AppendInTx(ctx, tx, fact); callbackErr != nil {
				return fmt.Errorf("append account provision audit: %w", callbackErr)
			}
		}
		return nil
	})
	return result, transactionOutcome(commitErr, callbackErr)
}
