package service

import (
	"context"
	"errors"
	"strings"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

// LegacyBackfillTenant is the sentinel tenant used by migration 000048 for
// pre-tenant legacy data. Legacy tables carry no tenant_id, so shadow identities
// for their uids are scoped to this tenant until an authoritative Snaplink
// tenant export replaces it.
const LegacyBackfillTenant = "legacy"

// DefaultBackfillBatch caps a single backfill run so the union scan stays
// bounded; callers loop until Completed is true.
const DefaultBackfillBatch = 500
const MaxBackfillBatch = 1000

// BackfillResult reports one backfill run (P1-07). The backfill is shadow-only:
// it creates account identities for legacy uids and never mutates legacy rows
// or legacy read semantics.
type BackfillResult struct {
	Scanned   int64  `json:"scanned"`  // uids considered in this run
	Created   int64  `json:"created"`  // accounts+identities newly created
	Existing  int64  `json:"existing"` // uids already mapped to an account
	Skipped   int64  `json:"skipped"`  // uids skipped (invalid / region mismatch)
	Total     int64  `json:"total"`    // distinct legacy uids overall (approx)
	Tenant    string `json:"tenant"`
	Issuer    string `json:"issuer"`
	Completed bool   `json:"completed"` // false => more uids remain; run again
}

// AccountBackfillService performs the one-shot legacy-uid shadow backfill.
type AccountBackfillService interface {
	BackfillLegacyIdentities(ctx context.Context, limit int) (*BackfillResult, error)
}

// WithAccountBackfillTenant sets the tenant used for shadow identities created
// from legacy uids. Defaults to the 000048 sentinel (LegacyBackfillTenant).
func WithAccountBackfillTenant(tenant string) AccountOption {
	return func(s *accountService) {
		if strings.TrimSpace(tenant) != "" {
			s.backfillTenant = strings.TrimSpace(tenant)
		}
	}
}

func (s *accountService) BackfillLegacyIdentities(ctx context.Context, limit int) (*BackfillResult, error) {
	backfill, ok := s.repo.(repository.LegacyBackfillRepository)
	if !ok {
		return nil, mapAccountError(errors.New("repository does not support legacy backfill"),
			"backfill.unavailable", "legacy backfill is not available")
	}
	if limit <= 0 {
		limit = DefaultBackfillBatch
	}
	if limit > MaxBackfillBatch {
		return nil, apperr.BadRequest("backfill.limit_invalid", "limit exceeds the maximum batch size")
	}
	if err := s.recordBackfillRequest(ctx, limit); err != nil {
		return nil, err
	}
	total, err := backfill.CountLegacyUIDs(ctx)
	if err != nil {
		return nil, mapAccountError(err, "backfill.count_failed", "failed to count legacy uids")
	}
	uids, err := backfill.ListLegacyUIDs(ctx, s.backfillTenant, s.issuer, limit)
	if err != nil {
		return nil, mapAccountError(err, "backfill.scan_failed", "failed to scan legacy uids")
	}
	result := &BackfillResult{Total: total, Tenant: s.backfillTenant, Issuer: s.issuer}
	for _, uid := range uids {
		s.backfillOne(ctx, uid, result)
	}
	result.Completed = int64(len(uids)) < int64(limit)
	return result, nil
}

func (s *accountService) recordBackfillRequest(ctx context.Context, limit int) error {
	if s.audit == nil {
		return nil // unit-test compatibility; composition wiring forbids this
	}
	// audit-site: standalone-fact — records every administrative batch intent,
	// including runs that discover no identities and create no account facts.
	if err := s.audit.Record(ctx, AuditFactInput{Source: "aero-id", Action: "account.legacy_backfill.requested",
		ResourceType: "legacy_identity_batch", ResourceID: s.backfillTenant,
		Payload: map[string]any{"issuer": s.issuer, "limit": limit}}); err != nil {
		return apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)
	}
	return nil
}

// backfillOne resolves one legacy uid into a shadow account identity. Each
// failure mode is counted, never fatal: the run is idempotent and resumable.
func (s *accountService) backfillOne(ctx context.Context, uid string, result *BackfillResult) {
	result.Scanned++
	if strings.TrimSpace(uid) == "" {
		result.Skipped++
		return
	}
	existing, findErr := s.repo.FindByIdentity(ctx, s.backfillTenant, s.issuer, uid)
	if findErr == nil && existing != nil {
		result.Existing++
		return
	}
	if findErr != nil && !errors.Is(findErr, repository.ErrNotFound) && !errors.Is(findErr, gorm.ErrRecordNotFound) {
		result.Skipped++
		return
	}
	homeRegion := s.region
	if s.home != nil {
		homeRegion = s.home.Resolve(s.issuer, uid)
	}
	// A5: the backfill is a state-changing administrative path; it must record
	// an account.provisioned fact for every shadow account it creates (same
	// in-tx append and A4 created-signal gating as the provision path).
	if err := s.ensureBackfillAccount(ctx, uid, homeRegion); err != nil {
		result.Skipped++
		return
	}
	result.Created++
}

// ensureBackfillAccount provisions one shadow account with its audit fact in
// the same transaction (A5). Failures are counted, never fatal: the run is
// idempotent and resumable.
func (s *accountService) ensureBackfillAccount(ctx context.Context, uid, homeRegion string) error {
	// Unconditional: nil audit logger is a construction error (REQ-2); the
	// plain EnsureByIdentity branch was deleted.
	repo, ok := s.repo.(repository.AccountProvisionAuditRepository)
	if !ok {
		return repository.ErrNotFound
	}
	accountID := idgen.NewID()
	fact := buildAuditFact(ctx, AuditFactInput{Source: "aero-id", EventID: accountID, ActorUID: uid,
		AccountID: accountID, TenantID: s.backfillTenant, Action: "account.provisioned", ResourceType: "account",
		ResourceID: accountID.String(), Payload: map[string]any{"home_region": homeRegion}})
	_, err := repo.EnsureByIdentityWithAudit(ctx, s.backfillTenant, s.issuer, uid, uid, homeRegion, accountID, fact)
	return err
}
