package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/aero/aero-id/internal/connector/billing"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

type BillingGateway interface {
	CurrentEntitlement(context.Context, string) (*domain.EntitlementProjection, error)
	Reserve(context.Context, string, billing.ReserveCommand) (*billing.Reservation, error)
	Commit(context.Context, string, string, string, string, map[string]string) error
	Release(context.Context, string, string, string) error
}

type AccountQuota interface {
	ReserveAccount(context.Context, string, string, string) (string, error)
	CommitAccount(context.Context, string, string, domain.ID) error
	ReleaseAccount(context.Context, string, string) error
}

// AccountQuotaSagaCoordinator is the durable local fence around a remote
// reservation. Implementations must change state with a conditional update;
// a compensation request that loses the fence must never call Release.
type AccountQuotaSagaCoordinator interface {
	ScheduleAccountQuotaRelease(context.Context, string, string) (bool, error)
	ScheduleAccountQuotaReleaseOwned(context.Context, domain.ID, int64) (bool, error)
	FinishAccountQuotaRelease(context.Context, string, string, bool, error) error
	MarkAccountQuotaPersisted(context.Context, string, string, domain.ID) error
	MarkAccountQuotaCommitted(context.Context, string, string) error
	MarkAccountQuotaExpired(context.Context, string, string) error
}

type AccountQuotaWorkCoordinator interface {
	ClaimAccountQuotaWork(context.Context, string, time.Duration) (*repository.AccountQuotaSaga, error)
	FinishAccountQuotaWork(context.Context, string, domain.ID, int64, bool, error) error
}

type BillingConfig struct {
	MaxProjectionStaleness time.Duration
	AccountFeature         string
	UserDimension          string
	ReservationTTL         time.Duration
}

type billingService struct {
	repo     repository.EntitlementRepository
	gateway  BillingGateway
	sagaRepo repository.AccountQuotaSagaRepository
	cfg      BillingConfig
	now      func() time.Time
}

func NewBillingService(repo repository.EntitlementRepository, gateway BillingGateway, cfg BillingConfig) *billingService {
	return newBillingService(repo, gateway, nil, cfg)
}

func NewBillingServiceWithSaga(repo repository.EntitlementRepository, gateway BillingGateway,
	sagaRepo repository.AccountQuotaSagaRepository, cfg BillingConfig) *billingService {
	return newBillingService(repo, gateway, sagaRepo, cfg)
}

func newBillingService(repo repository.EntitlementRepository, gateway BillingGateway,
	sagaRepo repository.AccountQuotaSagaRepository, cfg BillingConfig) *billingService {
	if cfg.MaxProjectionStaleness <= 0 {
		cfg.MaxProjectionStaleness = 15 * time.Minute
	}
	if strings.TrimSpace(cfg.UserDimension) == "" {
		cfg.UserDimension = "users"
	}
	if strings.TrimSpace(cfg.AccountFeature) == "" {
		cfg.AccountFeature = "account"
	}
	if cfg.ReservationTTL <= 0 {
		cfg.ReservationTTL = 5 * time.Minute
	}
	return &billingService{repo: repo, gateway: gateway, sagaRepo: sagaRepo, cfg: cfg, now: func() time.Time { return time.Now().UTC() }}
}

func (service *billingService) CurrentEntitlement(ctx context.Context, tenantID string) (*domain.EntitlementProjection, error) {
	if strings.TrimSpace(tenantID) == "" || service.repo == nil || service.gateway == nil {
		return nil, billingUnavailable(nil)
	}
	projection, err := service.gateway.CurrentEntitlement(ctx, tenantID)
	if err == nil && projection.Validate(tenantID) == nil {
		projection.FetchedAt = service.now()
		if saveErr := service.repo.SaveEntitlement(ctx, projection); saveErr != nil {
			return nil, billingUnavailable(saveErr)
		}
		stored, loadErr := service.repo.GetEntitlement(ctx, tenantID)
		if loadErr != nil {
			return nil, billingUnavailable(loadErr)
		}
		return stored, nil
	}
	return service.cachedEntitlement(ctx, tenantID, err)
}

func (service *billingService) cachedEntitlement(ctx context.Context, tenantID string, centralErr error) (*domain.EntitlementProjection, error) {
	projection, err := service.repo.GetEntitlement(ctx, tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, billingUnavailable(centralErr)
		}
		return nil, billingUnavailable(err)
	}
	if projection.FetchedAt.IsZero() || service.now().Sub(projection.FetchedAt) > service.cfg.MaxProjectionStaleness {
		return nil, billingUnavailable(centralErr)
	}
	return projection, nil
}

func (service *billingService) ReserveAccount(ctx context.Context, tenantID, issuer, subject string) (string, error) {
	projection, err := service.CurrentEntitlement(ctx, tenantID)
	if err != nil {
		return "", err
	}
	if !projection.FeatureEnabled(service.cfg.AccountFeature, service.now()) {
		return "", apperr.Forbidden("billing.feature_disabled", "tenant account feature is not enabled")
	}
	grant, ok := projection.Limit(service.cfg.UserDimension, service.now())
	if !ok {
		return "", apperr.Forbidden("billing.entitlement_inactive", "tenant has no active user entitlement")
	}
	if grantExhausted(grant) {
		return "", apperr.Conflict("billing.quota_exceeded", "tenant user quota is exhausted")
	}
	stable := billingStableID(tenantID, issuer, subject)
	reservationID := "usr-res-" + stable
	if service.sagaRepo != nil {
		attempt, err := service.sagaRepo.BeginAccountQuotaAttempt(ctx, tenantID, issuer, subject, reservationID)
		if err != nil {
			if errors.Is(err, repository.ErrQuotaAttemptActive) {
				return "", billingUnavailable(err)
			}
			return "", billingUnavailable(err)
		}
		reservationID = attempt.ReservationID
	}
	command := billing.ReserveCommand{ID: reservationID, Dimension: service.cfg.UserDimension, Quantity: 1,
		TTLSeconds: int64(service.cfg.ReservationTTL / time.Second), IdempotencyKey: "usr-reserve-" + strings.TrimPrefix(reservationID, "usr-res-")}
	if _, err := service.gateway.Reserve(ctx, tenantID, command); err != nil {
		return "", mapBillingError(err)
	}
	return reservationID, nil
}

func (service *billingService) CommitAccount(ctx context.Context, tenantID, reservationID string, accountID domain.ID) error {
	stable := strings.TrimPrefix(reservationID, "usr-res-")
	err := service.gateway.Commit(ctx, tenantID, reservationID, "usr-commit-"+stable, "usr-fact-"+stable,
		map[string]string{"account_id": accountID.String()})
	return mapBillingError(err)
}

func (service *billingService) ReleaseAccount(ctx context.Context, tenantID, reservationID string) error {
	if !validReservationID(reservationID) {
		return apperr.Internal("billing.reservation_invalid", "billing reservation identity is invalid")
	}
	return mapBillingError(service.gateway.Release(ctx, tenantID, reservationID, releaseKey(reservationID)))
}

func (service *billingService) MarkAccountQuotaPersisted(ctx context.Context, tenantID, reservationID string, accountID domain.ID) error {
	if service.sagaRepo == nil {
		return nil
	}
	return service.sagaRepo.MarkAccountQuotaPersisted(ctx, tenantID, reservationID, accountID)
}

func (service *billingService) MarkAccountQuotaCommitted(ctx context.Context, tenantID, reservationID string) error {
	if service.sagaRepo == nil {
		return nil
	}
	return service.sagaRepo.MarkAccountQuotaSagaCommitted(ctx, tenantID, reservationID)
}

func (service *billingService) MarkAccountQuotaExpired(ctx context.Context, tenantID, reservationID string) error {
	if service.sagaRepo == nil {
		return nil
	}
	return service.sagaRepo.MarkAccountQuotaSagaExpired(ctx, tenantID, reservationID)
}

func (service *billingService) ClaimAccountQuotaWork(ctx context.Context, owner string, lease time.Duration) (*repository.AccountQuotaSaga, error) {
	if service.sagaRepo == nil {
		return nil, repository.ErrNotFound
	}
	return service.sagaRepo.ClaimAccountQuotaWork(ctx, owner, lease)
}

func (service *billingService) FinishAccountQuotaWork(ctx context.Context, owner string, id domain.ID, generation int64,
	released bool, workErr error) error {
	if service.sagaRepo == nil {
		return nil
	}
	return service.sagaRepo.FinishAccountQuotaWork(ctx, owner, id, generation, released, workErr)
}

func (service *billingService) ScheduleAccountQuotaRelease(ctx context.Context, tenantID, reservationID string) (bool, error) {
	if service.sagaRepo == nil {
		return true, nil
	}
	return service.sagaRepo.ScheduleAccountQuotaRelease(ctx, tenantID, reservationID)
}

func (service *billingService) ScheduleAccountQuotaReleaseOwned(ctx context.Context, id domain.ID, generation int64) (bool, error) {
	if service.sagaRepo == nil {
		return false, nil
	}
	return service.sagaRepo.ScheduleAccountQuotaReleaseOwned(ctx, id, generation)
}

func (service *billingService) FinishAccountQuotaRelease(ctx context.Context, tenantID, reservationID string,
	released bool, releaseErr error) error {
	if service.sagaRepo == nil {
		return nil
	}
	// The repository locates the durable saga by tenant and reservation. The
	// worker path uses its lease-aware method; this path is the one-shot request
	// attempt immediately after it creates the release intent.
	return service.sagaRepo.FinishAccountQuotaRelease(ctx, tenantID, reservationID, released, releaseErr)
}

func releaseKey(reservationID string) string {
	return "usr-release-" + strings.TrimPrefix(reservationID, "usr-res-")
}

func validReservationID(value string) bool {
	const prefix = "usr-res-"
	return strings.HasPrefix(value, prefix) && len(value) <= 256 &&
		len(value) > len(prefix) && value == strings.TrimSpace(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func billingStableID(tenantID, issuer, subject string) string {
	digest := sha256.Sum256([]byte(tenantID + "\x00" + issuer + "\x00" + subject))
	return hex.EncodeToString(digest[:])
}

// grantExhausted reports whether a limit grant admits no new capacity: a
// finite grant whose hard cap is zero. Unlimited grants always admit capacity.
func grantExhausted(grant domain.LimitGrant) bool {
	return !grant.Unlimited && grant.Hard == 0
}

func mapBillingError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, billing.ErrQuotaExceeded) {
		return apperr.Conflict("billing.quota_exceeded", "tenant user quota is exhausted")
	}
	if errors.Is(err, billing.ErrEntitlement) {
		return apperr.Forbidden("billing.entitlement_inactive", "tenant has no active user entitlement")
	}
	if errors.Is(err, billing.ErrReservationExpired) {
		return apperr.Conflict("billing.reservation_expired", "account quota reservation expired")
	}
	if errors.Is(err, billing.ErrUnauthorized) {
		return apperr.Forbidden("billing.source_unauthorized", "tenant billing source is not authorized")
	}
	return billingUnavailable(err)
}

func billingUnavailable(err error) error {
	return apperr.Unavailable("billing.unavailable", "tenant entitlement is temporarily unavailable").Wrap(err)
}
