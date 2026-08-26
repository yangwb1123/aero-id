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
	account, err := s.accountRepo.GetByID(ctx, job.TenantID, job.AccountID)
	if err != nil {
		cause := apperr.Internal("export.account_load_failed", "failed to load account").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "export.account_load_failed", "failed to load account", nil, cause)
	}
	snapshots, err := s.snapshots.ListSnapshots(ctx, job.AccountID, nil)
	if err != nil {
		cause := apperr.Internal("export.snapshot_load_failed", "failed to load export data").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "export.snapshot_load_failed", "failed to load export data", nil, cause)
	}
	profile, err := s.accountRepo.GetProfile(ctx, job.AccountID)
	if err != nil {
		cause := apperr.Internal("export.profile_load_failed", "failed to load export profile").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "export.profile_load_failed", "failed to load export profile", nil, cause)
	}
	sources, _, err := s.accountRepo.ListSources(ctx, job.AccountID, domain.Page{Limit: domain.MaxPageLimit})
	if err != nil {
		cause := apperr.Internal("export.sources_load_failed", "failed to load export sources").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "export.sources_load_failed", "failed to load export sources", nil, cause)
	}
	memberships, _, err := s.accountRepo.ListMemberships(ctx, job.AccountID, domain.Page{Limit: domain.MaxPageLimit})
	if err != nil {
		cause := apperr.Internal("export.memberships_load_failed", "failed to load export memberships").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "export.memberships_load_failed", "failed to load export memberships", nil, cause)
	}
	datasets := make([]map[string]any, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot != nil {
			datasets = append(datasets, exportSnapshot(snapshot))
		}
	}
	manifest := map[string]any{"schema_version": 1, "account": exportAccount(account), "profile": exportProfile(profile),
		"sources": exportSources(sources), "memberships": exportMemberships(memberships), "datasets": datasets,
		"generated_at": time.Now().UTC()}
	if s.store == nil {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobPartiallyComplete, "export.store_unavailable", "temporary export storage is not configured", manifest)
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
	key, _, err := s.store.Put(ctx, job.ActorUID, "account-export-"+job.ID.String()+".bin", bytes.NewReader(encrypted))
	if err != nil {
		cause := apperr.Internal("export.write_failed", "failed to write export artifact").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "export.write_failed", "failed to write export artifact", nil, cause)
	}
	manifest["_storage_key"] = key
	return s.finishJobAndLoad(ctx, job, domain.SyncJobCompleted, "", "", manifest)
}

//nolint:cyclop // export download enforces ownership, expiry, decryption, and artifact validation.
func (s *syncJobService) DownloadExport(ctx context.Context, id domain.ID, oneTime bool) ([]byte, error) {
	job, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if job.Status != domain.SyncJobCompleted || len(s.exportKey) == 0 {
		return nil, apperr.Conflict("export.not_available", "export artifact is not available")
	}
	expires, ok := exportExpiry(job.Result["expires_at"])
	if !ok || !time.Now().UTC().Before(expires) {
		s.deleteExpiredExport(ctx, job)
		return nil, apperr.Conflict("export.expired", "export artifact has expired")
	}
	plain, key, err := s.readExportArtifact(ctx, job)
	if err != nil {
		return nil, err
	}
	if s.audit != nil {
		// audit-site: standalone-fact — successful export download access audit.
		if err := s.audit.Record(ctx, exportDownloadAudit(ctx, job, oneTime, len(plain))); err != nil {
			return nil, apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)
		}
	}
	if oneTime {
		// P7-06: one-time sensitive result — the artifact is burned after the
		// first successful read; a second download fails with not_found.
		if err := s.burnExportArtifact(ctx, job, key); err != nil {
			return nil, apperr.Internal("export.burn_failed", "failed to finalize one-time export").Wrap(err)
		}
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
	data, err := io.ReadAll(io.LimitReader(reader, s.maxExport+256))
	if err != nil || int64(len(data)) > s.maxExport {
		return nil, "", apperr.Internal("export.read_failed", "export artifact is too large").Wrap(err)
	}
	plain, err := decryptExport(data, s.exportKey)
	if err != nil {
		return nil, "", apperr.Internal("export.decrypt_failed", "failed to decrypt export artifact").Wrap(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(plain, &manifest); err != nil {
		return nil, "", apperr.Internal("export.invalid_artifact", "export artifact is invalid").Wrap(err)
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
