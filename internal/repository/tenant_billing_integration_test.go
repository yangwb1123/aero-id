//go:build integration

package repository_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestTenantScopedAccountsAndMonotonicEntitlementProjection(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	accounts := repository.NewAccountRepository(db)
	first, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "user-1", "user-1", "local")
	if err != nil {
		t.Fatal(err)
	}
	second, err := accounts.EnsureByIdentity(t.Context(), "tenant-b", "snaplink", "user-1", "user-1", "local")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.TenantID != "tenant-a" || second.TenantID != "tenant-b" {
		t.Fatalf("accounts are not tenant scoped: %+v %+v", first, second)
	}
	if _, err := accounts.GetByID(t.Context(), "tenant-b", first.ID); err == nil {
		t.Fatal("cross-tenant account lookup succeeded")
	}

	entitlements := repository.NewEntitlementRepository(db)
	now := time.Now().UTC()
	newer := entitlementFixture(now, 2, 20)
	if err := entitlements.SaveEntitlement(t.Context(), newer); err != nil {
		t.Fatal(err)
	}
	older := entitlementFixture(now.Add(-time.Minute), 1, 10)
	if err := entitlements.SaveEntitlement(t.Context(), older); err != nil {
		t.Fatal(err)
	}
	stored, err := entitlements.GetEntitlement(t.Context(), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != 2 || stored.Limits["users"].Hard != 20 {
		t.Fatalf("stale entitlement replaced newer projection: %+v", stored)
	}
	conflict := entitlementFixture(now.Add(time.Minute), 2, 21)
	if err := entitlements.SaveEntitlement(t.Context(), conflict); !errors.Is(err, repository.ErrEntitlementRevisionConflict) {
		t.Fatalf("same-revision content conflict error = %v", err)
	}
}

func TestAccountQuotaReservationPersistsAcrossCommitRetry(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	accounts := repository.NewAccountRepository(db)
	quota, ok := accounts.(repository.AccountQuotaRepository)
	if !ok {
		t.Fatal("account repository does not implement quota persistence")
	}
	account, err := quota.EnsureByIdentityWithQuota(t.Context(), "tenant-a", "snaplink", "user-2",
		"user-2", "local", "usr-res-stable")
	if err != nil {
		t.Fatal(err)
	}
	reservationID, err := quota.PendingAccountQuota(t.Context(), "tenant-a", account.ID)
	if err != nil || reservationID != "usr-res-stable" {
		t.Fatalf("pending reservation = %q, %v", reservationID, err)
	}
	if err := quota.MarkAccountQuotaCommitted(t.Context(), "tenant-a", account.ID, reservationID); err != nil {
		t.Fatal(err)
	}
	if err := quota.MarkAccountQuotaCommitted(t.Context(), "tenant-a", account.ID, reservationID); err != nil {
		t.Fatalf("idempotent mark failed: %v", err)
	}
	if _, err := quota.PendingAccountQuota(t.Context(), "tenant-a", account.ID); err == nil {
		t.Fatal("committed reservation remained pending")
	}
}

func entitlementFixture(at time.Time, revision uint64, hard int64) *domain.EntitlementProjection {
	return &domain.EntitlementProjection{
		TenantID: "tenant-a", SubscriptionID: "subscription-a", Plan: domain.PlanRef{ID: "full", Version: 1},
		Revision: revision, Active: true, Features: map[string]bool{"account_profiles": true},
		Limits: map[string]domain.LimitGrant{"users": {Hard: hard}}, EffectiveAt: at.Add(-time.Hour),
		ExpiresAt: at.Add(time.Hour), GeneratedAt: at, FetchedAt: at,
	}
}
