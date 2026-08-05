package service

import (
	"context"
	"errors"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
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
}

func NewRegionFencingService(directory repository.RegionDirectoryRepository) RegionFencingService {
	return &regionFencingService{directory: directory}
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
	if strings.TrimSpace(targetRegion) == "" {
		return 0, apperr.BadRequest("region.invalid_target", "target region is required")
	}
	newEpoch, err := s.directory.PromoteWithFencing(ctx, tenantID, accountID, strings.TrimSpace(targetRegion), expectedEpoch)
	if err != nil {
		if errors.Is(err, repository.ErrRegionEpochConflict) {
			return 0, apperr.Conflict("region.epoch_conflict", "expected epoch is stale; another promotion won")
		}
		if errors.Is(err, repository.ErrNotFound) {
			return 0, apperr.NotFound("region.directory_not_found", "account has no home-region directory entry")
		}
		return 0, apperr.Internal("region.promotion_failed", "failed to promote the home-region writer").Wrap(err)
	}
	return newEpoch, nil
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
