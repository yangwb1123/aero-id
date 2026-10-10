package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

func TestSyncExecutionRequiresCurrentAuthorityForEveryJobKind(t *testing.T) {
	for _, kind := range []domain.SyncJobType{domain.SyncJobSync, domain.SyncJobErase, domain.SyncJobExport, domain.SyncJobAuditExport} {
		for _, mode := range []string{"denied", "outage", "missing", "typed nil", "typed nil account", "home changed", "erased", "foreign account", "foreign tenant"} {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				svc, job, repo, source, account, allowed := syncExecutionFixture(t, kind)
				svc.permission = allowAllPermissions{} // never substitutes for execution authority
				code := "sync.authorization_unavailable"
				switch mode {
				case "denied":
					allowed.Store(false)
					code = "sync.authorization_denied"
				case "outage":
					svc.executionPermission = capabilityPermissions(func(context.Context, string, string) (bool, error) { return false, errors.New("authority offline") })
				case "missing":
					svc.executionPermission = nil
				case "typed nil":
					var checker *operationAuthorizationPermission
					svc.executionPermission = checker
				case "typed nil account":
					var missing *exportAccountFixture
					svc.accountRepo = missing
				case "home changed":
					account.account.HomeRegion = "other"
					code = "account.home_region_required"
				case "erased":
					account.account.Status = domain.AccountErased
					code = "account.erased"
				case "foreign account":
					account.account.ID = idgen.NewID()
					code = "sync.execution_scope_invalid"
				case "foreign tenant":
					account.account.TenantID = "other"
					code = "sync.execution_scope_invalid"
				}
				_, err := svc.execute(t.Context(), job)
				if requireAppError(t, err).Code != code {
					t.Fatalf("got %v, want %s", err, code)
				}
				if source.summaries.Load() != 0 || source.erases.Load() != 0 || source.queries.Load() != 0 || account.erased != 0 || account.sourcePages != 0 || account.memberPages != 0 {
					t.Fatal("blocked job sent an effect or read export content")
				}
				if repo.job.Status != domain.SyncJobPending || repo.job.ID != job.ID || repo.job.IdempotencyKey != job.IdempotencyKey || len(repo.facts) != 1 {
					t.Fatal("unstarted claim lost its intent, pacing or audit")
				}
			})
		}
	}
}

func TestSyncExecutionRechecksBeforeProjectionAndReplaysWithoutNewSourceRead(t *testing.T) {
	svc, job, repo, source, _, allowed := syncExecutionFixture(t, domain.SyncJobSync)
	source.afterSummary = func() { allowed.Store(false) }
	_, err := svc.execute(t.Context(), job)
	app := svc.application.(*syncGateApplication)
	if requireAppError(t, err).Code != "sync.authorization_denied" || source.summaries.Load() != 1 || app.applied != 0 || len(repo.replays) != 1 || repo.job.Status != domain.SyncJobUnknown {
		t.Fatal("revocation discarded the original summary or applied a projection", err)
	}
	claimed := armSyncGateJob(repo.job, "reconcile")
	_, err = svc.reconcileClaimed(t.Context(), claimed)
	if requireAppError(t, err).Code != "sync.authorization_denied" || source.summaries.Load() != 1 || app.reconciled != 0 || repo.job.Status != domain.SyncJobUnknown {
		t.Fatal("revoked recovery read source or changed unknown", err)
	}
	allowed.Store(true)
	claimed = armSyncGateJob(repo.job, "reconcile")
	result, err := svc.reconcileClaimed(t.Context(), claimed)
	if err != nil || result.Status != domain.SyncJobCompleted || source.summaries.Load() != 1 || app.reconciled != 1 || app.applied != 0 {
		t.Fatal("recovery did not apply the stored request once", err)
	}
}

func TestSyncEraseRevocationPreservesSourceSuccessAndDefersLocalErase(t *testing.T) {
	svc, job, repo, source, account, allowed := syncExecutionFixture(t, domain.SyncJobErase)
	source.afterErase = func() { allowed.Store(false) }
	_, err := svc.execute(t.Context(), job)
	if requireAppError(t, err).Code != "sync.authorization_denied" || source.erases.Load() != 1 || account.erased != 0 || repo.job.Status != domain.SyncJobUnknown {
		t.Fatal("revocation caused local erase or lost unknown", err)
	}
	if len(repo.outcomes) != 1 || repo.outcomes[0].Status != "completed" || repo.outcomes[0].SourceOperationID != "original-source-op" || repo.outcomes[0].Result["version"] != 7 {
		t.Fatal("known source receipt was overwritten")
	}
	allowed.Store(true)
	claimed := armSyncGateJob(repo.job, "reconcile")
	result, err := svc.reconcileClaimed(t.Context(), claimed)
	if err != nil || result.Status != domain.SyncJobCompleted || source.erases.Load() != 1 || source.queries.Load() != 0 || account.erased != 1 {
		t.Fatal("known success was reissued instead of finishing fenced local erase", err)
	}
}

func TestSyncEraseStatusQueryRechecksBeforeNotAcceptedRetry(t *testing.T) {
	svc, job, repo, source, _, allowed := syncExecutionFixture(t, domain.SyncJobErase)
	job.LeaseKind = "reconcile"
	repo.job.LeaseKind = "reconcile"
	source.status = "not_accepted"
	source.afterStatus = func() { allowed.Store(false) }
	status, id, _, err := svc.reconcileEraseSource(t.Context(), svc.accountRepo.(*syncGateAccount).account, job, "snaplink", map[string]any{"source_operation_id": "original-source-op"})
	if requireAppError(t, err).Code != "sync.authorization_denied" || status != "unknown" || id != "original-source-op" || source.queries.Load() != 1 || source.erases.Load() != 0 {
		t.Fatal("status response lent its grant to a second command", err)
	}
}

func TestSyncExecutionUsesDurableInitiatorAndRejectsLateGrant(t *testing.T) {
	svc, job, repo, source, _, _ := syncExecutionFixture(t, domain.SyncJobSync)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "operator", TenantID: "foreign"})
	svc.executionPermission = capabilityPermissions(func(_ context.Context, actor, permission string) (bool, error) {
		if actor != job.ActorUID || permission != domain.PermSourceSync {
			t.Fatal("ambient identity borrowed for worker authorization")
		}
		return false, nil
	})
	_, err := svc.execute(ctx, job)
	if requireAppError(t, err).Code != "sync.authorization_denied" || repo.facts[0].TenantID != job.TenantID || repo.facts[0].ActorUID != job.ActorUID {
		t.Fatal("fact used ambient scope", err)
	}
	first := repo.facts[0].EventID
	job = armSyncGateJob(repo.job, "execute")
	_, _ = svc.execute(ctx, job)
	if repo.facts[1].EventID == first {
		t.Fatal("separate blocking executions collapsed")
	}
	job = armSyncGateJob(repo.job, "execute")
	deadline, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()
	svc.executionPermission = capabilityPermissions(func(ctx context.Context, _ string, _ string) (bool, error) { <-ctx.Done(); return true, nil })
	_, err = svc.execute(deadline, job)
	if requireAppError(t, err).Code != "sync.authorization_unavailable" || source.summaries.Load() != 0 {
		t.Fatal("late grant sent an effect", err)
	}
}

func TestSyncExecutionLostLeaseAndAuditFailureDoNotRestoreAnotherOwner(t *testing.T) {
	for _, mode := range []string{"lost lease", "audit unavailable"} {
		t.Run(mode, func(t *testing.T) {
			svc, job, repo, source, _, allowed := syncExecutionFixture(t, domain.SyncJobSync)
			if mode == "lost lease" {
				svc.executionPermission = capabilityPermissions(func(context.Context, string, string) (bool, error) {
					repo.mu.Lock()
					repo.job.LeaseOwner = "replacement"
					repo.mu.Unlock()
					return true, nil
				})
			} else {
				allowed.Store(false)
				repo.deferErr = errors.New("ledger failed")
			}
			_, err := svc.execute(t.Context(), job)
			if requireAppError(t, err).Code != "sync.execution_defer_unknown" || repo.job.Status != domain.SyncJobRunning || source.summaries.Load() != 0 || len(repo.facts) != 0 {
				t.Fatal("uncertain deferral was reported as successful or sent effect", err)
			}
			if mode == "lost lease" && repo.job.LeaseOwner != "replacement" {
				t.Fatal("stale owner changed replacement")
			}
		})
	}
}

var _ connector.CommandConnector = (*syncGateSource)(nil)
