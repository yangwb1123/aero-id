package service

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type exportDownloadReadProbe struct {
	*memoryStore
	reads int
	after func()
}

func (s *exportDownloadReadProbe) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	s.reads++
	reader, err := s.memoryStore.Get(ctx, key)
	if s.after != nil {
		s.after()
	}
	return reader, err
}

func completedAuditDownloadFixture(t *testing.T) (*syncJobService, *domain.SyncJob, *exportDownloadReadProbe, *auditRecordSpy) {
	t.Helper()
	store := &exportDownloadReadProbe{memoryStore: &memoryStore{objects: map[string][]byte{}}}
	svc := newAuditExportService([]*domain.AuditFact{auditExportFact("tenant-a")}, store)
	job := &domain.SyncJob{ID: idgen.NewID(), TenantID: "tenant-a", AccountID: idgen.NewID(),
		ActorUID: "original-initiator", JobType: domain.SyncJobAuditExport}
	svc.repo.(*minimalSyncJobRepo).SeedJob(job)
	if _, err := svc.executeAuditExport(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	audit := &auditRecordSpy{}
	svc.audit = audit
	svc.permission = allowAllPermissions{}
	return svc, job, store, audit
}

func assertDownloadUnconsumed(t *testing.T, svc *syncJobService, job *domain.SyncJob, store *exportDownloadReadProbe, audit *auditRecordSpy) {
	t.Helper()
	stored, err := svc.repo.Get(t.Context(), job.ID)
	key, _ := job.Result["_storage_key"].(string)
	if err != nil || key == "" || stored.Result["_storage_key"] != key || len(store.objects[key]) == 0 || audit.records != 0 {
		t.Fatal("refused download consumed its original artifact or recorded successful access")
	}
}

func TestExportDownloadRequiresDirectCurrentAuthorityAndAuditRead(t *testing.T) {
	for _, mode := range []string{"export denied", "read denied", "outage", "missing", "typed nil"} {
		t.Run(mode, func(t *testing.T) {
			svc, job, store, audit := completedAuditDownloadFixture(t)
			svc.permission = allowAllPermissions{} // Legacy/ownership is never a download grant.
			code := "export.permission_check_failed"
			switch mode {
			case "missing":
				svc.executionPermission = nil
			case "typed nil":
				var missing *operationAuthorizationPermission
				svc.executionPermission = missing
			default:
				svc.executionPermission = capabilityPermissions(func(_ context.Context, actor, permission string) (bool, error) {
					if actor != "actor-1" {
						t.Fatal("download authorization used another principal")
					}
					if mode == "outage" {
						return false, errors.New("authority unavailable")
					}
					return permission != map[string]string{"export denied": domain.PermAuditExport, "read denied": domain.PermAuditRead}[mode], nil
				})
				if mode != "outage" {
					code = "export.forbidden"
				}
			}
			data, err := svc.DownloadExport(exportOwnerContext(t), job.ID, true)
			if len(data) != 0 || requireAppError(t, err).Code != code || store.reads != 0 {
				t.Fatal("unverified download authority released or read content", err)
			}
			assertDownloadUnconsumed(t, svc, job, store, audit)
		})
	}
}

func TestAuditDownloadRechecksBothCurrentReaderGrantsAfterObjectRead(t *testing.T) {
	for _, revoke := range []string{domain.PermAuditExport, domain.PermAuditRead} {
		t.Run(revoke, func(t *testing.T) {
			svc, job, store, audit := completedAuditDownloadFixture(t)
			svc.permission = allowAllPermissions{}
			revoked := false
			store.after = func() { revoked = true } // Object IO is the boundary, not a sleep.
			var permissions []string
			svc.executionPermission = capabilityPermissions(func(_ context.Context, actor, permission string) (bool, error) {
				if actor != "actor-1" {
					t.Fatal("download used the durable initiator instead of the current reader")
				}
				permissions = append(permissions, permission)
				return !revoked || permission != revoke, nil
			})
			data, err := svc.DownloadExport(exportOwnerContext(t), job.ID, true)
			if len(data) != 0 || requireAppError(t, err).Code != "export.forbidden" || store.reads != 1 || len(permissions) < 3 {
				t.Fatal("post-read revocation released content", err)
			}
			assertDownloadUnconsumed(t, svc, job, store, audit)
		})
	}
}

func TestExportDownloadAuthorizationUsesBoundedCurrentReaderContext(t *testing.T) {
	svc, job, _, _, _ := newScopedExportFixture(t, 0)
	job.ActorUID = "original-initiator"
	job.JobType = domain.SyncJobAuditExport
	var permissions []string
	var deadlines []time.Time
	svc.executionPermission = capabilityPermissions(func(ctx context.Context, actor, permission string) (bool, error) {
		if actor != "actor-1" {
			t.Fatal("current reader borrowed original initiator authority")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > syncAuthorizationBudget {
			t.Fatal("download authorization has no bounded deadline")
		}
		permissions = append(permissions, permission)
		deadlines = append(deadlines, deadline)
		return true, nil
	})
	if err := svc.recheckExportDownloadPermission(exportOwnerContext(t), job); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(permissions, []string{domain.PermAuditExport, domain.PermAuditRead}) || !deadlines[0].Equal(deadlines[1]) {
		t.Fatal("audit download did not share one deadline for both current grants")
	}
}

func TestExportDownloadAuthorizationInheritsEarlierDeadlineAndRejectsWrongScope(t *testing.T) {
	svc, job, _, _, _ := newScopedExportFixture(t, 0)
	ctx, cancel := context.WithTimeout(exportOwnerContext(t), time.Second)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	calls := 0
	svc.executionPermission = capabilityPermissions(func(ctx context.Context, _ string, permission string) (bool, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(parentDeadline) || permission != domain.PermAccountExport {
			t.Fatal("account download changed the original deadline or permission")
		}
		return true, nil
	})
	if err := svc.recheckExportDownloadPermission(ctx, job); err != nil || calls != 1 {
		t.Fatal("valid current account export grant was rejected", err)
	}
	job.TenantID = "foreign-tenant"
	if err := svc.recheckExportDownloadPermission(ctx, job); requireAppError(t, err).Code != "export.not_found" || calls != 1 {
		t.Fatal("foreign download borrowed current reader grants")
	}
	job.TenantID, job.JobType = "tenant-a", domain.SyncJobSync
	if err := svc.recheckExportDownloadPermission(ctx, job); requireAppError(t, err).Code != "export.not_available" || calls != 1 {
		t.Fatal("non-export job borrowed an export grant")
	}
	job.JobType = domain.SyncJobExport
	cancel()
	if err := svc.recheckExportDownloadPermission(ctx, job); requireAppError(t, err).Code != "export.permission_check_failed" || calls != 1 {
		t.Fatal("canceled admission called the authority")
	}
}

func TestExportDownloadRejectsLateGrantsAndIncompleteVerifiedIdentity(t *testing.T) {
	t.Run("late success", func(t *testing.T) {
		svc, job, store, audit := completedAuditDownloadFixture(t)
		svc.permission = allowAllPermissions{}
		ctx, cancel := context.WithCancel(exportOwnerContext(t))
		defer cancel()
		svc.executionPermission = capabilityPermissions(func(context.Context, string, string) (bool, error) {
			cancel()
			return true, nil // A noncooperating dependency cannot lend a late grant.
		})
		data, err := svc.DownloadExport(ctx, job.ID, true)
		if len(data) != 0 || requireAppError(t, err).Code != "export.permission_check_failed" || store.reads != 0 {
			t.Fatal("late authority grant reached artifact IO", err)
		}
		assertDownloadUnconsumed(t, svc, job, store, audit)
	})
	for _, field := range []string{"verified", "issuer", "subject", "client", "actor mismatch"} {
		t.Run(field, func(t *testing.T) {
			svc, job, store, audit := completedAuditDownloadFixture(t)
			identity, _ := IdentityFrom(exportOwnerContext(t))
			switch field {
			case "verified":
				identity.Verified = false
			case "issuer":
				identity.Issuer = ""
			case "subject":
				identity.Subject = ""
			case "client":
				identity.ClientID = ""
			}
			ctx := WithIdentity(t.Context(), identity)
			if field == "actor mismatch" {
				ctx = WithActor(ctx, "different-actor")
			}
			data, err := svc.DownloadExport(ctx, job.ID, true)
			if len(data) != 0 || err == nil || store.reads != 0 {
				t.Fatal("incomplete/inconsistent identity reached artifact IO")
			}
			assertDownloadUnconsumed(t, svc, job, store, audit)
		})
	}
}
