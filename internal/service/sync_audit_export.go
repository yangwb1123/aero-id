package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

// maxAuditExportEvents caps one audit export job; larger tenants export in
// bounded batches via the keyset cursor in a follow-up run.
const maxAuditExportEvents = 100_000

// auditExportEventsPerPage matches the repository's keyset page ceiling. Keeping
// the request at that ceiling avoids silently asking a repository to normalize
// a larger page and makes the bounded page budget explicit.
const auditExportEventsPerPage = domain.MaxPageLimit

// executeAuditExport implements P4-08: an asynchronous, TTL-bound audit export
// with a signed-style manifest (every fact's record hash is listed, and the
// manifest carries its own digest for verification). The artifact is encrypted
// with the export key and downloadable through the standard job endpoint.
//
//nolint:cyclop // export assembles header, pages and digest with explicit outcomes.
func (s *syncJobService) executeAuditExport(ctx context.Context, job *domain.SyncJob) (*domain.SyncJob, error) {
	if s.auditLedger == nil {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobFailed, "audit_export.ledger_unavailable", "audit ledger repository is not configured", nil)
	}
	cursor, err := s.auditExportStartCursor(ctx, job)
	if err != nil {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobFailed, "audit_export.invalid_continuation", "audit export continuation state is invalid", nil)
	}
	events, next, truncated, err := collectAuditExportEvents(ctx, s.auditLedger, job.TenantID, cursor, maxAuditExportEvents)
	if err != nil {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobFailed, "audit_export.ledger_read_failed", "failed to read the audit ledger", nil)
	}
	generatedAt := time.Now().UTC()
	manifest := map[string]any{
		"schema_version": 1, "type": "audit_export", "job_id": job.ID.String(), "tenant_id": job.TenantID,
		"account_id": job.AccountID.String(), "generated_at": generatedAt,
		"event_count": len(events), "events": events,
	}
	if truncated {
		// Keep the cursor in the durable server-side result, not in the
		// download token. The next request references this job id and can only
		// resume this tenant/account window.
		manifest["truncated"] = true
		manifest["continuation_job_id"] = job.ID.String()
		manifest[auditExportContinuationCursorKey] = next
	}
	manifest["manifest_digest"] = auditExportDigest(manifest)
	return s.writeAuditExportArtifact(ctx, job, manifest, generatedAt)
}

//nolint:cyclop // export validates bounded serialization, encryption, storage, and checkpoint outcomes.
func (s *syncJobService) writeAuditExportArtifact(ctx context.Context, job *domain.SyncJob,
	manifest map[string]any, generatedAt time.Time) (*domain.SyncJob, error) {
	if s.store == nil {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobPartiallyComplete, "audit_export.store_unavailable",
			"temporary export storage is not configured", manifest)
	}
	if len(s.exportKey) == 0 {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobPartiallyComplete, "audit_export.encryption_not_configured",
			"export encryption is not configured", manifest)
	}
	expiresAt := generatedAt.Add(s.exportTTL)
	manifest["download_url"] = "/v1/sync/jobs/" + job.ID.String() + "/export/download"
	manifest["expires_at"] = expiresAt
	artifactManifest := make(map[string]any, len(manifest))
	for key, value := range manifest {
		if len(key) == 0 || key[0] != '_' {
			artifactManifest[key] = value
		}
	}
	data, err := json.Marshal(artifactManifest)
	if err != nil {
		cause := apperr.Internal("audit_export.serialize_failed", "failed to serialize audit export manifest").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "audit_export.serialize_failed", "failed to serialize audit export manifest", nil, cause)
	}
	if int64(len(data)) > s.maxExport {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobPartiallyComplete, "audit_export.too_large",
			"audit export exceeds the configured limit", auditExportContinuationResult(manifest))
	}
	encrypted, err := encryptExport(data, s.exportKey)
	if err != nil {
		cause := apperr.Internal("audit_export.encrypt_failed", "failed to encrypt audit export artifact").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "audit_export.encrypt_failed", "failed to encrypt audit export artifact", nil, cause)
	}
	key, _, err := s.putSyncArtifact(ctx, job, "audit_export", encrypted, artifactManifest, "audit-export-"+job.ID.String()+".bin")
	if err != nil {
		var checkpointErr *artifactCheckpointError
		if errors.As(err, &checkpointErr) {
			return s.finishJobAndLoad(ctx, job, domain.SyncJobUnknown, "audit_export.artifact_checkpoint_unknown",
				"audit export artifact metadata outcome is unknown", nil)
		}
		cause := apperr.Internal("audit_export.write_failed", "failed to write audit export artifact").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "audit_export.write_failed", "failed to write audit export artifact", nil, cause)
	}
	manifest["_storage_key"] = key
	return s.finishJobAndLoad(ctx, job, domain.SyncJobCompleted, "", "", manifest)
}

// collectAuditExportEvents reads only bounded keyset pages. The final page is
// fetched with the remaining capacity, so a non-empty repository next cursor
// is the proof that an exactly-full window has more facts.
func collectAuditExportEvents(ctx context.Context, ledger repository.AuditLedgerRepository, tenant, cursor string,
	maxEvents int) ([]map[string]any, string, bool, error) {
	if maxEvents <= 0 {
		return nil, "", false, errors.New("audit export event limit must be positive")
	}
	events := make([]map[string]any, 0, auditExportEventsPerPage)
	pages := 0
	for len(events) < maxEvents {
		pages++
		if pages > maxEvents {
			return nil, "", false, errors.New("audit export exceeded its bounded page budget")
		}
		limit := auditExportEventsPerPage
		if remaining := maxEvents - len(events); remaining < limit {
			limit = remaining
		}
		previous := cursor
		facts, next, err := listAuditExportPage(ctx, ledger, tenant, cursor, limit)
		if err != nil {
			return nil, "", false, err
		}
		for _, fact := range facts {
			events = append(events, auditExportEvent(fact))
		}
		if len(events) >= maxEvents {
			return events, next, next != "", nil
		}
		if next == "" {
			return events, "", false, nil
		}
		if next == previous {
			return nil, "", false, errors.New("audit export ledger cursor did not advance")
		}
		cursor = next
	}
	return events, "", false, nil
}

func auditExportContinuationResult(manifest map[string]any) map[string]any {
	result := make(map[string]any, 8)
	for _, key := range []string{"schema_version", "type", "tenant_id", "account_id", "generated_at", "event_count",
		"truncated", "continuation_job_id", "manifest_digest", auditExportContinuationCursorKey} {
		if value, ok := manifest[key]; ok {
			result[key] = value
		}
	}
	return result
}

func listAuditExportPage(ctx context.Context, ledger repository.AuditLedgerRepository, tenant, cursor string,
	limit int) ([]*domain.AuditFact, string, error) {
	facts, next, err := ledger.List(ctx, domain.AuditFactFilter{
		TenantID: tenant, Page: domain.Page{Limit: limit, Cursor: cursor},
	})
	if err != nil {
		return nil, "", err
	}
	if len(facts) > limit {
		return nil, "", errors.New("audit export ledger returned an oversized page")
	}
	for _, fact := range facts {
		if fact == nil || fact.TenantID != tenant {
			return nil, "", errors.New("audit export ledger returned a cross-tenant fact")
		}
	}
	return facts, next, nil
}

// auditExportEvent is the allow-listed per-fact projection: identity fields,
// action, resource, timing and the durable record hash for verification.
func auditExportEvent(fact *domain.AuditFact) map[string]any {
	return map[string]any{
		"event_id":      fact.ID.String(),
		"source":        fact.Source,
		"source_region": fact.SourceRegion,
		"action":        fact.Action,
		"actor_uid":     fact.ActorUID,
		"account_id":    fact.AccountID.String(),
		"resource_type": fact.ResourceType,
		"resource_id":   fact.ResourceID,
		"operation_id":  fact.OperationID.String(),
		"occurred_at":   fact.OccurredAt.UTC(),
		"record_hash":   fact.RecordHash,
		// Ledger rows can predate the service redaction boundary or arrive
		// through an internal importer. Re-apply the allow-list at export time.
		"payload": redactAuditPayload(fact.Payload),
	}
}

// auditExportDigest hashes the deterministic manifest fields so a downloaded
// artifact can be verified against the recorded digest (P4-08 hash 验证).
func auditExportDigest(manifest map[string]any) string {
	hasher := sha256.New()
	header, _ := json.Marshal(map[string]any{
		"schema_version": manifest["schema_version"], "type": manifest["type"],
		"job_id": manifest["job_id"], "tenant_id": manifest["tenant_id"], "account_id": manifest["account_id"],
		"generated_at": manifest["generated_at"], "event_count": manifest["event_count"],
	})
	_, _ = hasher.Write(header)
	events, _ := json.Marshal(manifest["events"])
	_, _ = hasher.Write(events)
	return hex.EncodeToString(hasher.Sum(nil))
}
