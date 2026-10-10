package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/storage"
)

func deriveExportKey(value string) []byte {
	if value == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func encryptExport(data, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	const prefix = "AERO-ID-EXPORT-V1\x00"
	return append([]byte(prefix), append(nonce, gcm.Seal(nil, nonce, data, nil)...)...), nil
}

func decryptExport(data, key []byte) ([]byte, error) {
	const prefix = "AERO-ID-EXPORT-V1\x00"
	if len(data) <= len(prefix) || string(data[:len(prefix)]) != prefix {
		return nil, errors.New("invalid export artifact")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(data) < len(prefix)+gcm.NonceSize() {
		return nil, errors.New("invalid export artifact")
	}
	offset := len(prefix)
	return gcm.Open(nil, data[offset:offset+gcm.NonceSize()], data[offset+gcm.NonceSize():], nil)
}

//nolint:cyclop,funlen // export execution coordinates projection reads, encryption, storage, and durable job state.
func (s *syncJobService) executeExport(ctx context.Context, job *domain.SyncJob) (*domain.SyncJob, error) {
	if job.ErrorCode == "export.cleanup_unknown" {
		return s.reconcileInvalidatedExport(ctx, job)
	}
	if shouldRecoverExport(job) {
		return s.recoverExportArtifact(ctx, job)
	}
	ctx, cancel := syncRemoteContext(ctx)
	defer cancel()
	if err := s.checkSyncExecution(ctx, job, false); err != nil {
		return s.pauseSyncExecution(ctx, job, err, true, nil)
	}
	manifest, err := s.buildAccountExportManifest(ctx, job)
	if err != nil {
		return s.exportDataFailure(ctx, job, err)
	}
	if s.store == nil {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobPartiallyComplete, "export.store_unavailable", "temporary export storage is not configured", nil)
	}
	if len(s.exportKey) == 0 {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobPartiallyComplete, "export.encryption_not_configured", "export encryption is not configured", nil)
	}
	expiresAt := time.Now().UTC().Add(s.exportTTL)
	manifest["download_url"] = "/v1/sync/jobs/" + job.ID.String() + "/export/download"
	manifest["expires_at"] = expiresAt
	data, err := json.Marshal(manifest)
	if err != nil {
		cause := apperr.Internal("export.serialize_failed", "failed to serialize export manifest").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "export.serialize_failed", "failed to serialize export manifest", nil, cause)
	}
	if int64(len(data)) > s.maxExport {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobPartiallyComplete, "export.too_large", "export manifest exceeds the configured limit", nil)
	}
	encrypted, err := encryptExport(data, s.exportKey)
	if err != nil {
		cause := apperr.Internal("export.encrypt_failed", "failed to encrypt export artifact").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "export.encrypt_failed", "failed to encrypt export artifact", nil, cause)
	}
	if err := s.recheckExportAccount(ctx, job); err != nil {
		return s.exportDataFailure(ctx, job, err)
	}
	key, _, err := s.putSyncArtifact(ctx, job, "export", encrypted, manifest, "account-export-"+job.ID.String()+".bin")
	if key != "" {
		manifest["_storage_key"] = key
	}
	if err != nil {
		if isSyncExecutionBlocked(err) {
			return s.pauseSyncExecution(ctx, job, err, false, manifest)
		}
		var checkpointErr *artifactCheckpointError
		if errors.As(err, &checkpointErr) {
			return s.finishJobAndLoad(ctx, job, domain.SyncJobUnknown, "export.artifact_checkpoint_unknown",
				"export artifact metadata outcome is unknown", safeAccountExportResult(manifest))
		}
		cause := apperr.Internal("export.write_failed", "failed to write export artifact").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "export.write_failed", "failed to write export artifact", nil, cause)
	}
	manifest["_storage_key"] = key
	if err := s.recheckExportAccount(ctx, job); err != nil {
		return s.discardExportAfterDispositionChange(ctx, job, manifest, key, err)
	}
	return s.finishExportArtifact(ctx, job, manifest, key)
}

func (s *syncJobService) DownloadExport(ctx context.Context, id domain.ID, oneTime bool) ([]byte, error) {
	job, err := s.loadExportDownload(ctx, id)
	if err != nil {
		return nil, err
	}
	plain, key, err := s.readExportArtifact(ctx, job)
	if err != nil {
		return nil, err
	}
	if admitted, err := s.admitExportDownload(ctx, job, key, oneTime, len(plain)); admitted {
		if err != nil {
			return nil, err
		}
		return bytes.TrimSpace(plain), nil
	}
	if err := s.recheckLegacyExportDownload(ctx, job); err != nil {
		return nil, err
	}
	if s.audit != nil {
		// audit-site: standalone-fact — successful export download access audit.
		if err := s.audit.Record(ctx, exportDownloadAudit(ctx, job, oneTime, len(plain))); err != nil {
			return nil, apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)
		}
	}
	if err := s.burnOneTimeExport(ctx, job, key, oneTime); err != nil {
		return nil, apperr.Internal("export.burn_failed", "failed to finalize one-time export").Wrap(err)
	}
	return bytes.TrimSpace(plain), nil
}

func exportExpiry(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, !typed.IsZero()
	case *time.Time:
		if typed == nil {
			return time.Time{}, false
		}
		return *typed, !typed.IsZero()
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(typed))
		return parsed, err == nil && !parsed.IsZero()
	default:
		return time.Time{}, false
	}
}

func (s *syncJobService) readExportArtifact(ctx context.Context, job *domain.SyncJob) ([]byte, string, error) {
	key, _ := job.Result["_storage_key"].(string)
	if key == "" || s.store == nil {
		return nil, "", apperr.NotFound("export.not_found", "export artifact not found")
	}
	reader, err := s.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, "", apperr.NotFound("export.not_found", "export artifact not found")
		}
		return nil, "", apperr.Internal("export.read_failed", "failed to read export artifact").Wrap(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, s.maxExport+257))
	if err != nil || int64(len(data)) > s.maxExport+256 {
		return nil, "", apperr.Internal("export.read_failed", "export artifact is too large").Wrap(err)
	}
	plain, manifest, err := s.decodeExportArtifact(data)
	if err != nil {
		return nil, "", err
	}
	if err := s.validateDownloadedArtifact(ctx, job, key, len(data), manifest); err != nil {
		return nil, "", err
	}
	return plain, key, nil
}

func exportDownloadAudit(ctx context.Context, job *domain.SyncJob, oneTime bool, size int) AuditFactInput {
	action := "account.export.downloaded"
	if job.JobType == domain.SyncJobAuditExport {
		action = "audit.export.downloaded"
	}
	return AuditFactInput{Source: "aero-id", ActorUID: actorFromContext(ctx), AccountID: job.AccountID,
		TenantID: TenantFrom(ctx), Action: action, ResourceType: "sync_job", ResourceID: job.ID.String(),
		Payload: map[string]any{"job_type": job.JobType, "one_time": oneTime, "bytes": size}}
}

// burnExportArtifact removes the artifact and its storage key from the job so
// a one-time download cannot be repeated.
func (s *syncJobService) burnExportArtifact(ctx context.Context, job *domain.SyncJob, key string) error {
	if burner, ok := s.repo.(repository.SyncJobArtifactBurner); ok {
		// Remove the pointer first. A storage delete is not transactional, but
		// an object left behind after a delete failure is no longer reachable.
		if err := burner.BurnArtifact(ctx, job.ID, string(job.JobType), key); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return apperr.NotFound("export.not_found", "export artifact not found")
			}
			return err
		}
		if s.store != nil {
			if err := s.store.Delete(ctx, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
				return err
			}
		}
		return nil
	}
	if s.store != nil {
		if err := s.store.Delete(ctx, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
	}
	return s.repo.ClearExportArtifact(ctx, job.ID, key)
}

func (s *syncJobService) deleteExpiredExport(ctx context.Context, job *domain.SyncJob) {
	if job == nil || s.store == nil {
		return
	}
	key, ok := job.Result["_storage_key"].(string)
	if !ok || key == "" {
		return
	}
	if err := s.store.Delete(ctx, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return
	}
	_ = s.repo.ClearExportArtifact(ctx, job.ID, key)
}

func (s *syncJobService) cleanupExpiredExports(ctx context.Context) {
	if s.store == nil {
		return
	}
	jobs, err := s.repo.ListExpiredExports(ctx, time.Now().UTC(), 50)
	if err != nil {
		return
	}
	for _, job := range jobs {
		if job == nil {
			continue
		}
		key, ok := job.Result["_storage_key"].(string)
		if !ok || key == "" {
			continue
		}
		if err := s.store.Delete(ctx, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
			continue
		}
		_ = s.repo.ClearExportArtifact(ctx, job.ID, key)
	}
}

func syncPermission(jobType domain.SyncJobType) string {
	switch jobType {
	case domain.SyncJobExport:
		return domain.PermAccountExport
	case domain.SyncJobErase:
		return domain.PermAccountErase
	case domain.SyncJobAuditExport:
		return domain.PermAuditExport
	default:
		return domain.PermSourceSync
	}
}
