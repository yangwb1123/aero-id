package service

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/storage"
)

type artifactRecoveryFixture struct {
	*minimalSyncJobRepo
	repository.SyncJobRecoveryRepository
	metadata      *repository.ArtifactMetadata
	checkpointErr error
	metadataErr   error
}

func (r *artifactRecoveryFixture) CheckpointArtifact(_ context.Context, input repository.ArtifactCheckpoint) error {
	r.metadata = &repository.ArtifactMetadata{JobID: input.JobID, Kind: input.Kind, StorageKey: input.StorageKey,
		SizeBytes: input.SizeBytes, ManifestSHA256: input.ManifestSHA256, Continuation: input.Continuation}
	return r.checkpointErr // Can commit and then lose the response.
}
func (r *artifactRecoveryFixture) GetArtifact(context.Context, domain.ID, string) (*repository.ArtifactMetadata, error) {
	return r.metadata, r.metadataErr
}

type jobArtifactFixture struct {
	*memoryStore
	repo     *artifactRecoveryFixture
	puts     int
	writeErr error
}

func (s *jobArtifactFixture) PutJobArtifact(_ context.Context, owner string, id domain.ID, kind string, reader io.Reader) (string, int64, error) {
	s.puts++
	if s.repo.metadata == nil {
		panic("object write preceded durable checkpoint")
	}
	key, err := storage.JobArtifactKey(owner, id, kind)
	if err != nil {
		return "", 0, err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", 0, err
	}
	s.objects[key] = data
	return key, int64(len(data)), s.writeErr
}

func recoverableExportFixture(t *testing.T) (*syncJobService, *domain.SyncJob, *artifactRecoveryFixture, *jobArtifactFixture, *exportAccountFixture) {
	t.Helper()
	svc, job, account, _, memory := newScopedExportFixture(t, 1)
	job.LeaseOwner = idgen.NewID().String()
	repo := &artifactRecoveryFixture{minimalSyncJobRepo: svc.repo.(*minimalSyncJobRepo)}
	store := &jobArtifactFixture{memoryStore: memory, repo: repo}
	svc.repo, svc.store = repo, store
	return svc, job, repo, store, account
}

func TestExportRecoveryUsesOriginalCheckpointAfterLostPutResponse(t *testing.T) {
	svc, job, _, store, account := recoverableExportFixture(t)
	store.writeErr = errors.New("lost object write response")
	first, err := svc.executeExport(t.Context(), job)
	if err != nil || first.Status != domain.SyncJobUnknown {
		t.Fatalf("first = %+v/%v", first, err)
	}
	key, expiry, digest := first.Result["_storage_key"], first.Result["expires_at"], first.Result["manifest_digest"]
	if key == nil || expiry == nil || digest == nil {
		t.Fatal("unknown lost recovery metadata")
	}
	store.writeErr = nil
	first.LeaseOwner, first.LeaseKind = idgen.NewID().String(), "reconcile"
	result, err := svc.executeExport(t.Context(), first)
	if err != nil || result.Status != domain.SyncJobCompleted {
		t.Fatalf("recovery = %+v/%v", result, err)
	}
	if store.puts != 1 || account.sourcePages != 1 || result.Result["_storage_key"] != key ||
		result.Result["expires_at"] != expiry || result.Result["manifest_digest"] != digest {
		t.Fatal("recovery regenerated content, key, TTL or digest")
	}
}

func TestExportCheckpointUncertaintyNeverWritesOrRegenerates(t *testing.T) {
	svc, job, repo, store, _ := recoverableExportFixture(t)
	repo.checkpointErr = errors.New("checkpoint commit acknowledgement lost")
	first, err := svc.executeExport(t.Context(), job)
	if err != nil || first.Status != domain.SyncJobUnknown || store.puts != 0 {
		t.Fatalf("prepare = %+v/%v", first, err)
	}
	repo.checkpointErr = nil
	first.LeaseKind = "reconcile"
	result, err := svc.executeExport(t.Context(), first)
	if err != nil || result.Status != domain.SyncJobUnknown || store.puts != 0 {
		t.Fatalf("missing object authorized a second write: %+v/%v", result, err)
	}
	result.Result["expires_at"] = time.Now().UTC().Add(-time.Second)
	_, err = svc.executeExport(t.Context(), result)
	if requireAppError(t, err).Code != "export.expired" || store.puts != 0 {
		t.Fatal("expired recovery generated another object")
	}
}

func TestAccountExportDownloadBindsExpiryAndRequiresCheckpoint(t *testing.T) {
	svc, job, repo, _, _ := recoverableExportFixture(t)
	completed, err := svc.executeExport(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.readExportArtifact(t.Context(), completed); err != nil {
		t.Fatal(err)
	}
	completed.Result["expires_at"] = time.Now().UTC().Add(time.Hour)
	if _, _, err := svc.readExportArtifact(t.Context(), completed); requireAppError(t, err).Code != "export.invalid_artifact" {
		t.Fatal("job and object expiry mismatch was accepted")
	}
	completed.Result["expires_at"] = repo.metadata.Continuation["_artifact_result"].(map[string]any)["expires_at"]
	repo.metadataErr = repository.ErrNotFound
	if _, _, err := svc.readExportArtifact(t.Context(), completed); requireAppError(t, err).Code != "export.metadata_unavailable" {
		t.Fatal("missing durable checkpoint was accepted")
	}
}

func TestAccountExportDigestPreservesLargeVersionsAndCiphertextOverhead(t *testing.T) {
	svc, job, _, _, _ := recoverableExportFixture(t)
	svc.snapshots.(*exportSnapshotFixture).values[0].Version = (1 << 54) + 1
	completed, err := svc.executeExport(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := svc.readExportArtifact(t.Context(), completed)
	if err != nil {
		t.Fatal(err)
	}
	svc.maxExport = int64(len(plain))
	if _, _, err := svc.readExportArtifact(t.Context(), completed); err != nil {
		t.Fatalf("exact plaintext budget incorrectly included encryption overhead: %v", err)
	}
}

func TestAccountExportRecoveryErasedAccountOnlyDisposesOriginalObject(t *testing.T) {
	svc, job, _, store, account := recoverableExportFixture(t)
	store.writeErr = io.ErrUnexpectedEOF
	first, err := svc.executeExport(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	account.account.Status = domain.AccountErased
	first.LeaseKind = "reconcile"
	_, err = svc.executeExport(t.Context(), first)
	if requireAppError(t, err).Code != "export.account_erased" || store.puts != 1 || len(store.objects) != 0 {
		t.Fatal("erased account recovery regenerated or retained an object")
	}
}
