//go:build integration

package repository_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

func exportFenceFixture(t *testing.T, db *gorm.DB, subject string) (repository.AccountRepository, repository.SyncJobRepository, *domain.SyncJob) {
	t.Helper()
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "export-fence-test-signing-key"})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "export-fence", "snaplink", subject, subject, "local")
	if err != nil {
		t.Fatal(err)
	}
	jobs := repository.NewSyncJobRepository(db, ledger)
	job := &domain.SyncJob{ID: idgen.NewID(), AccountID: account.ID, TenantID: account.TenantID, ActorUID: subject,
		JobType: domain.SyncJobExport, Status: domain.SyncJobPending, IdempotencyKey: subject,
		Request: map[string]any{"datasets": []domain.AccountDataset{"aero-vault.usage"}}}
	if err := jobs.Create(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	job, err = jobs.Claim(t.Context(), job.ID, "export-fence-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return accounts, jobs, job
}

func exportDownloadFact(job *domain.SyncJob) *domain.AuditFact {
	return &domain.AuditFact{ID: idgen.NewID(), EventID: idgen.NewID(), Source: "aero-id", SourceRegion: "local",
		PartitionKey: job.TenantID + ":" + job.AccountID.String(), TenantID: job.TenantID, ActorUID: job.ActorUID,
		AccountID: job.AccountID, Action: "account.export.downloaded", ResourceType: "sync_job",
		ResourceID: job.ID.String(), OccurredAt: time.Now().UTC(), Payload: map[string]any{"one_time": true}}
}
func exportPublishedResult(job *domain.SyncJob) map[string]any {
	return map[string]any{"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
		"_storage_key": "exports/" + job.ID.String() + "/artifact"}
}

func TestExportPublicationAndDownloadShareAccountDispositionFence(t *testing.T) {
	db, _, _, _ := newEventContentHarness(t)
	t.Run("finish waits for erase and rejects publication", func(t *testing.T) {
		_, jobs, job := exportFenceFixture(t, db, "publish-race")
		tx := db.WithContext(t.Context()).Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		defer tx.Rollback()
		if err := tx.Exec("UPDATE accounts SET status = 'erased' WHERE id = ?", job.AccountID).Error; err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			done <- jobs.Finish(t.Context(), job.ID, job.LeaseOwner, domain.SyncJobCompleted, "", "", exportPublishedResult(job))
		}()
		select {
		case err := <-done:
			t.Fatalf("publication escaped root lock: %v", err)
		case <-time.After(30 * time.Millisecond):
		}
		if err := tx.Commit().Error; err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, repository.ErrAccountErased) {
			t.Fatalf("finish = %v", err)
		}
		stored, err := jobs.Get(t.Context(), job.ID)
		if err != nil || stored.Status != domain.SyncJobRunning {
			t.Fatal("invalidated publication committed")
		}
	})
	t.Run("download waits for erase and does not consume or audit", func(t *testing.T) {
		_, jobs, job := exportFenceFixture(t, db, "download-race")
		result := exportPublishedResult(job)
		if err := jobs.Finish(t.Context(), job.ID, job.LeaseOwner, domain.SyncJobCompleted, "", "", result); err != nil {
			t.Fatal(err)
		}
		tx := db.WithContext(t.Context()).Begin()
		defer tx.Rollback()
		if err := tx.Exec("UPDATE accounts SET status = 'erased' WHERE id = ?", job.AccountID).Error; err != nil {
			t.Fatal(err)
		}
		fact := exportDownloadFact(job)
		done := make(chan error, 1)
		go func() {
			done <- jobs.(repository.SyncJobExportDownloadRepository).AdmitExportDownload(t.Context(), job.ID, "export", result["_storage_key"].(string), true, fact)
		}()
		select {
		case err := <-done:
			t.Fatalf("download escaped root lock: %v", err)
		case <-time.After(30 * time.Millisecond):
		}
		if err := tx.Commit().Error; err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, repository.ErrAccountErased) {
			t.Fatalf("admit = %v", err)
		}
		var count int64
		if err := db.Table("audit_ledger").Where("event_id = ?", fact.EventID).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("rejected download retained a success fact")
		}
	})
	t.Run("one-time concurrent reads admit only one", func(t *testing.T) {
		_, jobs, job := exportFenceFixture(t, db, "one-time-race")
		result := exportPublishedResult(job)
		if err := jobs.Finish(t.Context(), job.ID, job.LeaseOwner, domain.SyncJobCompleted, "", "", result); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		outcomes := make(chan error, 2)
		for range 2 {
			wg.Go(func() {
				outcomes <- jobs.(repository.SyncJobExportDownloadRepository).AdmitExportDownload(t.Context(), job.ID, "export", result["_storage_key"].(string), true, exportDownloadFact(job))
			})
		}
		wg.Wait()
		close(outcomes)
		succeeded := 0
		for err := range outcomes {
			if err == nil {
				succeeded++
			} else if !errors.Is(err, repository.ErrNotFound) {
				t.Fatal(err)
			}
		}
		if succeeded != 1 {
			t.Fatalf("admitted = %d", succeeded)
		}
	})
	t.Run("checkpoint keeps the original key and TTL", func(t *testing.T) {
		_, jobs, job := exportFenceFixture(t, db, "checkpoint-original")
		recovery := jobs.(repository.SyncJobRecoveryRepository)
		expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
		key := "recovery/" + job.ID.String() + "/original"
		checkpoint := repository.ArtifactCheckpoint{JobID: job.ID, Kind: "export", Worker: job.LeaseOwner,
			StorageKey: key, ManifestSHA256: strings.Repeat("a", 64), SizeBytes: 3,
			Continuation: map[string]any{"_artifact_result": map[string]any{"expires_at": expires, "_storage_key": key}}}
		if err := recovery.CheckpointArtifact(t.Context(), checkpoint); err != nil {
			t.Fatal(err)
		}
		checkpoint.Continuation = map[string]any{"_artifact_result": map[string]any{"expires_at": expires.Add(time.Hour)}}
		if err := recovery.CheckpointArtifact(t.Context(), checkpoint); err != nil {
			t.Fatal(err)
		}
		fresh, err := jobs.Get(t.Context(), job.ID)
		if err != nil || fresh == nil || fresh.Result["_storage_key"] != key ||
			fresh.Result["expires_at"] != expires.Format(time.RFC3339) {
			t.Fatalf("checkpoint rewrote original metadata: %+v, %v", fresh, err)
		}
		checkpoint.StorageKey = "recovery/" + job.ID.String() + "/replacement"
		if err := recovery.CheckpointArtifact(t.Context(), checkpoint); !errors.Is(err, repository.ErrSyncOutcomeConflict) {
			t.Fatalf("replacement checkpoint = %v", err)
		}
	})

	t.Run("audit failure rolls back consumption", func(t *testing.T) {
		_, jobs, job := exportFenceFixture(t, db, "download-audit")
		result := exportPublishedResult(job)
		if err := jobs.Finish(t.Context(), job.ID, job.LeaseOwner, domain.SyncJobCompleted, "", "", result); err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`CREATE FUNCTION reject_export_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$;
 CREATE TRIGGER reject_export_audit BEFORE INSERT ON audit_ledger FOR EACH ROW EXECUTE FUNCTION reject_export_audit()`).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			db.Exec("DROP TRIGGER reject_export_audit ON audit_ledger; DROP FUNCTION reject_export_audit()")
		})
		err := jobs.(repository.SyncJobExportDownloadRepository).AdmitExportDownload(context.Background(), job.ID, "export", result["_storage_key"].(string), true, exportDownloadFact(job))
		if err == nil {
			t.Fatal("audit failure admitted download")
		}
		stored, err := jobs.Get(t.Context(), job.ID)
		if err != nil || stored.Result["_storage_key"] != result["_storage_key"] {
			t.Fatal("audit failure consumed the pointer")
		}
	})
}
