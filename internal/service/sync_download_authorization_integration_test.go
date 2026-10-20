//go:build integration

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/storage"
)

type exportDownloadLocalReadProbe struct {
	storage.FileStorage
	reads int
	after func()
}

func (s *exportDownloadLocalReadProbe) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	s.reads++
	reader, err := s.FileStorage.Get(ctx, key)
	if s.after != nil {
		s.after()
	}
	return reader, err
}

func postgresAuditDownloadFixture(t *testing.T, h *standaloneHarness) (*syncJobService, *domain.SyncJob, *exportDownloadLocalReadProbe) {
	t.Helper()
	accounts := repository.NewAccountRepository(h.db, h.repo)
	actor := "original-initiator-" + idgen.NewID().String()
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", actor, actor, "local")
	if err != nil {
		t.Fatal(err)
	}
	jobs := repository.NewSyncJobRepository(h.db, h.repo)
	job := &domain.SyncJob{ID: idgen.NewID(), AccountID: account.ID, ActorUID: actor, TenantID: account.TenantID,
		JobType: domain.SyncJobAuditExport, Status: domain.SyncJobPending, IdempotencyKey: idgen.NewID().String(),
		Request: map[string]any{"datasets": []domain.AccountDataset{}}, Result: map[string]any{}}
	if err := jobs.Create(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	job, err = jobs.Claim(t.Context(), job.ID, "download-fixture", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	local := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	svc := NewSyncJobServiceWithOptions(jobs, nil, accounts, nil, nil, local, nil,
		WithSyncJobPermission(allowAllPermissions{}), WithSyncJobExecutionPermission(allowAllPermissions{}),
		WithSyncJobAudit(h.ledger), WithSyncExportConfig("download-fixture-encryption-key", 1<<20, time.Hour)).(*syncJobService)
	seedAuditDownloadArtifact(t, svc, job, local)
	job, err = jobs.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	probe := &exportDownloadLocalReadProbe{FileStorage: local}
	svc.store = probe
	return svc, job, probe
}

// Seed a completed original artifact through the real checkpoint/finish ports.
// This fixture tests download admission, not production generation or S3.
func seedAuditDownloadArtifact(t *testing.T, svc *syncJobService, job *domain.SyncJob, local *storage.Local) {
	t.Helper()
	manifest := map[string]any{"schema_version": 1, "type": "audit_export", "job_id": job.ID.String(),
		"account_id": job.AccountID.String(), "tenant_id": job.TenantID, "generated_at": time.Now().UTC(),
		"event_count": 0, "events": []any{}}
	manifest["manifest_digest"] = auditExportDigest(manifest)
	manifest["expires_at"] = time.Now().UTC().Add(time.Hour)
	plain, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := encryptExport(plain, svc.exportKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := storage.JobArtifactKey(job.LeaseOwner, job.ID, "audit_export")
	if err != nil {
		t.Fatal(err)
	}
	recovery := svc.repo.(repository.SyncJobRecoveryRepository)
	if err := svc.checkpointJobArtifact(t.Context(), recovery, job, "audit_export", key, int64(len(data)), manifest); err != nil {
		t.Fatal(err)
	}
	writtenKey, size, err := local.PutJobArtifact(t.Context(), job.LeaseOwner, job.ID, "audit_export", bytes.NewReader(data))
	if err != nil || writtenKey != key || size != int64(len(data)) {
		t.Fatal("original artifact fixture write failed", err)
	}
	result := artifactResultMetadata(job.JobType, manifest)
	result["_storage_key"] = key
	if err := svc.repo.Finish(t.Context(), job.ID, job.LeaseOwner, domain.SyncJobCompleted, "", "", result); err != nil {
		t.Fatal(err)
	}
}

func assertPostgresDownloadRetained(t *testing.T, h *standaloneHarness, svc *syncJobService, original *domain.SyncJob) {
	t.Helper()
	fresh, err := svc.repo.Get(t.Context(), original.ID)
	if err != nil || fresh.Result["_storage_key"] != original.Result["_storage_key"] || fresh.Result["expires_at"] != original.Result["expires_at"] {
		t.Fatal("refusal changed original artifact pointer/TTL")
	}
	if _, err := svc.store.Stat(t.Context(), original.Result["_storage_key"].(string)); err != nil {
		t.Fatal("refusal deleted the original artifact", err)
	}
	metadata, err := svc.repo.(repository.SyncJobArtifactRepository).GetArtifact(t.Context(), original.ID, "audit_export")
	if err != nil || metadata.StorageKey != original.Result["_storage_key"] {
		t.Fatal("refusal lost the original recovery checkpoint", err)
	}
	var facts int64
	if err := h.db.Table("audit_ledger").Where("action = ? AND resource_id = ?", "audit.export.downloaded", original.ID.String()).Count(&facts).Error; err != nil || facts != 0 {
		t.Fatal("refusal committed a successful download fact", err)
	}
}

func TestExportDownloadPostgresCurrentAuthorityAndAtomicConsumption(t *testing.T) {
	h := startStandaloneHarness(t)
	for _, mode := range []string{"read denied", "revoked during object IO", "foreign tenant", "audit rollback"} {
		t.Run(mode, func(t *testing.T) {
			svc, job, store := postgresAuditDownloadFixture(t, h)
			ctx := exportOwnerContext(t)
			code, reads := "export.forbidden", 0
			switch mode {
			case "read denied":
				svc.executionPermission = capabilityPermissions(func(_ context.Context, _ string, permission string) (bool, error) {
					return permission != domain.PermAuditRead, nil
				})
			case "revoked during object IO":
				revoked := false
				store.after = func() { revoked = true }
				svc.executionPermission = capabilityPermissions(func(_ context.Context, _ string, permission string) (bool, error) {
					return !revoked || permission != domain.PermAuditRead, nil
				})
				reads = 1
			case "foreign tenant":
				identity, _ := IdentityFrom(ctx)
				identity.TenantID = "foreign-tenant"
				ctx = WithIdentity(t.Context(), identity)
				code = "sync.not_found"
			case "audit rollback":
				installDownloadAuditFailure(t, h)
				code, reads = "export.admission_failed", 1
			}
			data, err := svc.DownloadExport(ctx, job.ID, true)
			if len(data) != 0 || requireAppError(t, err).Code != code || store.reads != reads {
				t.Fatal("refused download returned content or crossed its read boundary", err)
			}
			assertPostgresDownloadRetained(t, h, svc, job)
		})
	}
	t.Run("current reader repeat and one-time admission", func(t *testing.T) {
		svc, job, store := postgresAuditDownloadFixture(t, h)
		calls := 0
		svc.executionPermission = capabilityPermissions(func(_ context.Context, actor, permission string) (bool, error) {
			calls++
			if actor != "actor-1" || actor == job.ActorUID || (permission != domain.PermAuditExport && permission != domain.PermAuditRead) {
				t.Fatal("download borrowed initiator authority or checked the wrong grant")
			}
			return true, nil
		})
		for _, oneTime := range []bool{false, false, true} {
			data, err := svc.DownloadExport(exportOwnerContext(t), job.ID, oneTime)
			if err != nil || len(data) == 0 {
				t.Fatal("authorized original artifact download failed", err)
			}
		}
		if calls != 12 || store.reads != 3 {
			t.Fatal("download skipped a current grant before or after object IO")
		}
		assertPostgresDownloadConsumed(t, h, svc, job)
	})
}

func installDownloadAuditFailure(t *testing.T, h *standaloneHarness) {
	t.Helper()
	if err := h.db.Exec(`CREATE FUNCTION reject_current_download_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action = 'audit.export.downloaded' THEN RAISE EXCEPTION 'injected download audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_current_download_audit BEFORE INSERT ON audit_ledger FOR EACH ROW EXECUTE FUNCTION reject_current_download_audit()`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.db.Exec("DROP TRIGGER reject_current_download_audit ON audit_ledger; DROP FUNCTION reject_current_download_audit()").Error; err != nil {
			t.Error(err)
		}
	})
}

func assertPostgresDownloadConsumed(t *testing.T, h *standaloneHarness, svc *syncJobService, original *domain.SyncJob) {
	t.Helper()
	var counts struct {
		Facts      int64
		Executions int64
	}
	if err := h.db.Raw(`SELECT count(*) AS facts, count(DISTINCT id) AS executions FROM audit_ledger
 WHERE action = 'audit.export.downloaded' AND resource_id = ? AND actor_uid = 'actor-1'`, original.ID.String()).Scan(&counts).Error; err != nil || counts.Facts != 3 || counts.Executions != 3 {
		t.Fatal("successful reads did not retain independent current-reader facts", err)
	}
	fresh, err := svc.repo.Get(t.Context(), original.ID)
	if err != nil || fresh.Result["_storage_key"] != nil {
		t.Fatal("one-time admission retained a downloadable pointer", err)
	}
	if _, err := svc.store.Stat(t.Context(), original.Result["_storage_key"].(string)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("one-time cleanup did not delete the original artifact", err)
	}
	if _, err := svc.repo.(repository.SyncJobArtifactRepository).GetArtifact(t.Context(), original.ID, "audit_export"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatal("confirmed disposal retained the recovery checkpoint", err)
	}
	if data, err := svc.DownloadExport(exportOwnerContext(t), original.ID, true); len(data) != 0 || err == nil {
		t.Fatal("consumed download was regenerated")
	}
}
