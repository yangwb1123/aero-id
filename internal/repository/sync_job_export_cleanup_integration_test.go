//go:build integration

package repository_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

func TestExportCleanupIncludesUnknownAndConsumedCheckpointsWithoutStarvation(t *testing.T) {
	db, _, _, _ := newEventContentHarness(t)
	_, jobs, job := exportFenceFixture(t, db, "cleanup-base")
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	// Earlier non-expired rows must not consume the cleanup page budget.
	for i := range 60 {
		live := &domain.SyncJob{ID: idgen.NewID(), AccountID: job.AccountID, ActorUID: job.ActorUID, TenantID: job.TenantID,
			JobType: domain.SyncJobExport, Status: domain.SyncJobCompleted, IdempotencyKey: "live-" + string(rune('a'+i)),
			Result: map[string]any{"expires_at": future, "_storage_key": "live-key"}}
		if err := jobs.Create(t.Context(), live); err != nil {
			t.Fatal(err)
		}
	}
	key := "exports/" + job.ID.String() + "/original"
	expires := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	recovery := jobs.(repository.SyncJobRecoveryRepository)
	if err := recovery.CheckpointArtifact(t.Context(), repository.ArtifactCheckpoint{JobID: job.ID, Kind: "export", Worker: job.LeaseOwner,
		StorageKey: key, ManifestSHA256: strings.Repeat("a", 64), SizeBytes: 10,
		Continuation: map[string]any{"_artifact_result": map[string]any{"expires_at": expires, "_storage_key": key}}}); err != nil {
		t.Fatal(err)
	}
	stored, err := jobs.Get(t.Context(), job.ID)
	if err != nil || stored.Result["_storage_key"] != key {
		t.Fatal("prepared checkpoint did not persist TTL/pointer")
	}
	if err := jobs.Finish(t.Context(), job.ID, job.LeaseOwner, domain.SyncJobUnknown, "export.artifact_checkpoint_unknown", "", stored.Result); err != nil {
		t.Fatal(err)
	}
	items, err := jobs.ListExpiredExports(t.Context(), time.Now().UTC(), 1)
	if err != nil || len(items) != 1 || items[0].ID != job.ID {
		t.Fatalf("unknown cleanup = %+v/%v", items, err)
	}
	// Simulate pointer consumption before an uncertain backing-store delete.
	if err := db.Exec("UPDATE sync_jobs SET result = result - '_storage_key', status = 'completed' WHERE id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	items, err = jobs.ListExpiredExports(t.Context(), time.Now().UTC(), 1)
	if err != nil || len(items) != 1 || items[0].Result["_storage_key"] != key {
		t.Fatalf("consumed cleanup = %+v/%v", items, err)
	}
	if err := jobs.ClearExportArtifact(t.Context(), job.ID, key); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.(repository.SyncJobArtifactRepository).GetArtifact(t.Context(), job.ID, "export"); err == nil {
		t.Fatal("confirmed cleanup retained checkpoint")
	}
	items, err = jobs.ListExpiredExports(t.Context(), time.Now().UTC(), 1)
	if err != nil || len(items) != 0 {
		t.Fatalf("cleared cleanup = %+v/%v", items, err)
	}
}
