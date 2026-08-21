package service

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// RegionFencingService enforces the durable home-region writer lease (P6-02).
// Writes are refused unless the local region still holds the fenced epoch;
// promotion uses compare-and-swap so two regions can never both commit.
type RegionFencingService interface {
	// Directory returns the account's durable home-region entry.
	Directory(ctx context.Context, tenantID string, accountID domain.ID) (*repository.RegionDirectory, error)
	// Promote moves the writer lease to targetRegion with a fencing epoch CAS.
	Promote(ctx context.Context, tenantID string, accountID domain.ID, targetRegion string, expectedEpoch int64) (int64, error)
	// RegionEpoch reads the durable epoch of a region.
	RegionEpoch(ctx context.Context, region string) (int64, error)
}

type regionFencingService struct {
	directory repository.RegionDirectoryRepository
	audit     AuditLogger // nil => audit inactive
	regions   map[string]struct{}
}

func NewRegionFencingService(directory repository.RegionDirectoryRepository, opts ...func(*regionFencingService)) RegionFencingService {
	s := &regionFencingService{directory: directory}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WithRegionFencingAuditLogger wires an AuditLogger into the region fencing
// service; when wired, Promote routes through the in-tx audited repository arm.
func WithRegionFencingAuditLogger(a AuditLogger) func(*regionFencingService) {
	return func(s *regionFencingService) { s.audit = a }
}

// WithRegionFencingAllowedRegions prevents an administrative promotion from
// creating a directory entry for a typo or an unconfigured residency region.
// An empty set preserves compatibility for isolated unit callers; production
// composition always supplies the validated account region list.
func WithRegionFencingAllowedRegions(regions []string) func(*regionFencingService) {
	return func(s *regionFencingService) {
		s.regions = make(map[string]struct{}, len(regions))
		for _, region := range regions {
			if value := strings.TrimSpace(region); value != "" {
				s.regions[value] = struct{}{}
			}
		}
	}
}

func (s *regionFencingService) Directory(ctx context.Context, tenantID string, accountID domain.ID) (*repository.RegionDirectory, error) {
	entry, err := s.directory.GetDirectory(ctx, tenantID, accountID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("region.directory_not_found", "account has no home-region directory entry")
		}
		return nil, apperr.Internal("region.directory_lookup_failed", "failed to read the region directory").Wrap(err)
	}
	return entry, nil
}

func (s *regionFencingService) Promote(ctx context.Context, tenantID string, accountID domain.ID,
	targetRegion string, expectedEpoch int64) (int64, error) {
	targetRegion = strings.TrimSpace(targetRegion)
	if targetRegion == "" {
		return 0, apperr.BadRequest("region.invalid_target", "target region is required")
	}
	if len(s.regions) > 0 {
		if _, ok := s.regions[targetRegion]; !ok {
			return 0, apperr.BadRequest("region.invalid_target", "target region is not configured")
		}
	}
	// Security-relevant admin op: with an auditor wired, the promotion CAS and
	// its fact commit in ONE transaction (in-tx model, ban/file-share
	// precedent). A post-commit standalone audit would leave an unrecoverable
	// residual — the fencing epoch is consumed by the CAS, so a retry would
	// Conflict. On audit failure the promotion rolls back and a retry with the
	// same expectedEpoch is safe.
	if s.audit != nil {
		repo, ok := s.directory.(repository.RegionDirectoryAuditRepository)
		if !ok {
			return 0, apperr.Internal("region.audit_unavailable", "region promotion audit persistence is unavailable")
		}
		newEpoch, err := repo.PromoteWithFencingWithAudit(ctx, tenantID, accountID, targetRegion, expectedEpoch,
			buildLegacyAuditFact(ctx, actorFromContext(ctx), domain.AuditRegionPromote, "region_directory",
				accountID.String(), "", map[string]any{"target_region": targetRegion, "expected_epoch": expectedEpoch},
				regionPromoteEventID(tenantID, accountID, targetRegion, expectedEpoch)))
		if err != nil {
			return 0, mapRegionPromotionError(err)
		}
		return newEpoch, nil
	}
	newEpoch, err := s.directory.PromoteWithFencing(ctx, tenantID, accountID, targetRegion, expectedEpoch)
	if err != nil {
		return 0, mapRegionPromotionError(err)
	}
	return newEpoch, nil
}

// mapRegionPromotionError translates repository promotion errors to the
// Conflict/NotFound/Internal AppErrors shared by both the audited and the
// plain promotion arms. The in-tx audit append failure surfaces as Internal
// "region.promotion_failed" (the tx rolled back, so the promotion genuinely
// failed — ban/file-share precedent).
func mapRegionPromotionError(err error) error {
	if errors.Is(err, repository.ErrRegionEpochConflict) {
		return apperr.Conflict("region.epoch_conflict", "expected epoch is stale; another promotion won")
	}
	if errors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("region.directory_not_found", "account has no home-region directory entry")
	}
	return apperr.Internal("region.promotion_failed", "failed to promote the home-region writer").Wrap(err)
}

// regionPromoteEventID derives the deterministic EventID of one promotion
// attempt: the CAS guarantees each expectedEpoch is consumed at most once, so
// successive promotions never collide, while a retry of the same attempt
// re-derives the same ID (ledger dedup via (tenant, source, event_id)).
func regionPromoteEventID(tenantID string, accountID domain.ID, targetRegion string, expectedEpoch int64) domain.ID {
	return idgen.Anchor("audit", "region.promote", tenantID, accountID.String(), targetRegion,
		strconv.FormatInt(expectedEpoch, 10))
}

func (s *regionFencingService) RegionEpoch(ctx context.Context, region string) (int64, error) {
	epoch, err := s.directory.RegionEpoch(ctx, region)
	if err != nil {
		return 0, apperr.Internal("region.epoch_lookup_failed", "failed to read the region epoch").Wrap(err)
	}
	return epoch, nil
}

// verifyAccountWriteFence is the write-path guard. It fails closed when the
// local region can no longer prove its writer lease:
//   - directory entry missing  -> lazy-ensure with the account's home region
//     (single-region compatibility for accounts created before fencing); the
//     existing home-region equality check still applies.
//   - directory home != local  -> home_region_required conflict.
//   - local epoch != fenced    -> region.write_fenced (a promotion fenced us out).
func (s *accountService) verifyAccountWriteFence(ctx context.Context, account *domain.Account) error {
	if s.directory == nil {
		return nil // fencing disabled in this deployment
	}
	entry, err := s.directory.GetDirectory(ctx, account.TenantID, account.ID)
	if errors.Is(err, repository.ErrNotFound) {
		if account.HomeRegion != s.region {
			return apperr.Conflict("account.home_region_required", "account write must be routed to its home region")
		}
		return s.directory.EnsureDirectory(ctx, account.TenantID, account.ID, account.HomeRegion, s.homeVersion)
	}
	if err != nil {
		return apperr.Internal("region.directory_lookup_failed", "failed to verify the home-region writer").Wrap(err)
	}
	if entry.HomeRegion != s.region {
		return apperr.Conflict("account.home_region_required", "account write must be routed to its home region")
	}
	localEpoch, err := s.directory.RegionEpoch(ctx, s.region)
	if err != nil {
		return apperr.Internal("region.epoch_lookup_failed", "failed to verify the region epoch").Wrap(err)
	}
	if localEpoch != entry.FencedEpoch {
		return apperr.Forbidden("region.write_fenced",
			"the local region no longer holds the account writer lease")
	}
	return nil
}

// WithAccountRegionDirectory wires the durable fencing guard into the account
// service write path.
func WithAccountRegionDirectory(directory repository.RegionDirectoryRepository, resolverVersion string) AccountOption {
	return func(s *accountService) {
		s.directory = directory
		if strings.TrimSpace(resolverVersion) != "" {
			s.homeVersion = strings.TrimSpace(resolverVersion)
		}
	}
}
