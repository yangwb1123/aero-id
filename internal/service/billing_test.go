package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/connector/billing"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type entitlementRepoStub struct {
	projection *domain.EntitlementProjection
	err        error
}

func (stub *entitlementRepoStub) GetEntitlement(context.Context, string) (*domain.EntitlementProjection, error) {
	return stub.projection, stub.err
}

func (stub *entitlementRepoStub) SaveEntitlement(_ context.Context, value *domain.EntitlementProjection) error {
	stub.projection = value
	return stub.err
}

type billingGatewayStub struct {
	projection *domain.EntitlementProjection
	err        error
	commands   []billing.ReserveCommand
	releases   []billingReleaseCall
}

type billingReleaseCall struct {
	tenantID       string
	reservationID  string
	idempotencyKey string
}

func (stub *billingGatewayStub) CurrentEntitlement(context.Context, string) (*domain.EntitlementProjection, error) {
	return stub.projection, stub.err
}

func (stub *billingGatewayStub) Reserve(_ context.Context, tenantID string, command billing.ReserveCommand) (*billing.Reservation, error) {
	stub.commands = append(stub.commands, command)
	if stub.err != nil {
		return nil, stub.err
	}
	return &billing.Reservation{ID: command.ID, TenantID: tenantID, Dimension: command.Dimension, Quantity: command.Quantity}, nil
}

func (stub *billingGatewayStub) Commit(context.Context, string, string, string, string, map[string]string) error {
	return stub.err
}

func (stub *billingGatewayStub) Release(_ context.Context, tenantID, reservationID, idempotencyKey string) error {
	stub.releases = append(stub.releases, billingReleaseCall{tenantID: tenantID, reservationID: reservationID, idempotencyKey: idempotencyKey})
	return stub.err
}

func TestBillingServiceUsesRecentProjectionDuringCentralOutage(t *testing.T) {
	now := time.Now().UTC()
	projection := testEntitlement(now, domain.LimitGrant{Hard: 10})
	repo := &entitlementRepoStub{projection: projection}
	gateway := &billingGatewayStub{err: billing.ErrUnavailable}
	service := NewBillingService(repo, gateway, BillingConfig{MaxProjectionStaleness: time.Hour})
	service.now = func() time.Time { return now }
	result, err := service.CurrentEntitlement(t.Context(), "tenant-1")
	if err != nil || result != projection {
		t.Fatalf("cached entitlement = %+v, %v", result, err)
	}
}

func TestBillingServiceFailsClosedWithoutFirstProjection(t *testing.T) {
	repo := &entitlementRepoStub{err: repository.ErrNotFound}
	service := NewBillingService(repo, &billingGatewayStub{err: billing.ErrUnavailable}, BillingConfig{})
	_, err := service.CurrentEntitlement(t.Context(), "tenant-1")
	var appError *apperr.AppError
	if !errors.As(err, &appError) || appError.HTTP != 503 {
		t.Fatalf("first-projection error = %v", err)
	}
}

func TestBillingServiceTreatsHardZeroAsZeroCapacity(t *testing.T) {
	now := time.Now().UTC()
	gateway := &billingGatewayStub{projection: testEntitlement(now, domain.LimitGrant{})}
	service := NewBillingService(&entitlementRepoStub{}, gateway, BillingConfig{})
	service.now = func() time.Time { return now }
	if _, err := service.ReserveAccount(t.Context(), "tenant-1", "snaplink", "user-1"); err == nil {
		t.Fatal("hard zero entitlement admitted a user")
	}
	if len(gateway.commands) != 0 {
		t.Fatal("central reservation was called after local zero-capacity decision")
	}
}

func TestBillingServiceRejectsDisabledAccountFeature(t *testing.T) {
	now := time.Now().UTC()
	projection := testEntitlement(now, domain.LimitGrant{Unlimited: true})
	projection.Features["account"] = false
	gateway := &billingGatewayStub{projection: projection}
	service := NewBillingService(&entitlementRepoStub{}, gateway, BillingConfig{})
	service.now = func() time.Time { return now }
	if _, err := service.ReserveAccount(t.Context(), "tenant-1", "snaplink", "user-1"); err == nil {
		t.Fatal("disabled account feature admitted a user")
	}
	if len(gateway.commands) != 0 {
		t.Fatal("central reservation was called for a disabled account feature")
	}
}

func TestBillingServiceUsesStableReservationIdentity(t *testing.T) {
	now := time.Now().UTC()
	gateway := &billingGatewayStub{projection: testEntitlement(now, domain.LimitGrant{Unlimited: true})}
	service := NewBillingService(&entitlementRepoStub{}, gateway, BillingConfig{})
	service.now = func() time.Time { return now }
	first, err := service.ReserveAccount(t.Context(), "tenant-1", "snaplink", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ReserveAccount(t.Context(), "tenant-1", "snaplink", "user-1")
	if err != nil || first != second || gateway.commands[0].IdempotencyKey != gateway.commands[1].IdempotencyKey {
		t.Fatalf("reservation identity not stable: %q %q", first, second)
	}
	if err := service.CommitAccount(t.Context(), "tenant-1", first, idgen.NewID()); err != nil {
		t.Fatal(err)
	}
}

func TestBillingServiceRejectsMalformedReleaseReservation(t *testing.T) {
	gateway := &billingGatewayStub{}
	service := NewBillingService(&entitlementRepoStub{}, gateway, BillingConfig{})
	if err := service.ReleaseAccount(t.Context(), "tenant-1", "usr-res-"); err == nil {
		t.Fatal("malformed reservation was released")
	}
	if len(gateway.releases) != 0 {
		t.Fatal("malformed reservation reached billing gateway")
	}
}

func TestBillingServiceReleaseUsesStableIdempotencyKey(t *testing.T) {
	gateway := &billingGatewayStub{}
	service := NewBillingService(&entitlementRepoStub{}, gateway, BillingConfig{})
	if err := service.ReleaseAccount(t.Context(), "tenant-1", "usr-res-stable"); err != nil {
		t.Fatal(err)
	}
	if len(gateway.releases) != 1 {
		t.Fatalf("release calls = %d, want 1", len(gateway.releases))
	}
	got := gateway.releases[0]
	if got.tenantID != "tenant-1" || got.reservationID != "usr-res-stable" || got.idempotencyKey != "usr-release-stable" {
		t.Fatalf("release call = %+v, want stable tenant/reservation/key", got)
	}
}

func testEntitlement(now time.Time, grant domain.LimitGrant) *domain.EntitlementProjection {
	return &domain.EntitlementProjection{
		TenantID: "tenant-1", SubscriptionID: "subscription-1", Plan: domain.PlanRef{ID: "full", Version: 1},
		Revision: 1, Active: true, Features: map[string]bool{"account": true},
		Limits: map[string]domain.LimitGrant{"users": grant}, EffectiveAt: now.Add(-time.Hour),
		ExpiresAt: now.Add(time.Hour), GeneratedAt: now.Add(-time.Minute), FetchedAt: now.Add(-time.Minute),
	}
}
