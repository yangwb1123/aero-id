package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

// maxAuditExportEvents caps one audit export job; larger tenants export in
// bounded batches via the keyset cursor in a follow-up run.
const maxAuditExportEvents = 100_000

// auditExportEventsPerPage bounds each ledger page fetch.
const auditExportEventsPerPage = 1000

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
	events := make([]map[string]any, 0, auditExportEventsPerPage)
	cursor := ""
	for len(events) < maxAuditExportEvents {
		facts, next, err := s.auditLedger.List(ctx, domain.AuditFactFilter{
			TenantID: job.TenantID, Page: domain.Page{Limit: auditExportEventsPerPage, Cursor: cursor},
		})
		if err != nil {
			return s.finishJobAndLoad(ctx, job, domain.SyncJobFailed, "audit_export.ledger_read_failed", "failed to read the audit ledger", nil)
		}
		for _, fact := range facts {
			events = append(events, auditExportEvent(fact))
		}
		if next == "" {
			break
		}
		cursor = next
	}
	generatedAt := time.Now().UTC()
	manifest := map[string]any{
		"schema_version": 1, "type": "audit_export", "tenant_id": job.TenantID,
		"account_id": job.AccountID.String(), "generated_at": generatedAt,
		"event_count": len(events), "events": events,
	}
	if len(events) == maxAuditExportEvents {
		manifest["truncated"] = true // more facts exist; run another export after this window
	}
	manifest["manifest_digest"] = auditExportDigest(manifest)
	return s.writeAuditExportArtifact(ctx, job, manifest, generatedAt)
}

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
	data, err := json.Marshal(manifest)
	if err != nil {
		cause := apperr.Internal("audit_export.serialize_failed", "failed to serialize audit export manifest").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "audit_export.serialize_failed", "failed to serialize audit export manifest", nil, cause)
	}
	if int64(len(data)) > s.maxExport {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobPartiallyComplete, "audit_export.too_large",
			"audit export exceeds the configured limit", nil)
	}
	encrypted, err := encryptExport(data, s.exportKey)
	if err != nil {
		cause := apperr.Internal("audit_export.encrypt_failed", "failed to encrypt audit export artifact").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "audit_export.encrypt_failed", "failed to encrypt audit export artifact", nil, cause)
	}
	key, _, err := s.store.Put(ctx, job.ActorUID, "audit-export-"+job.ID.String()+".bin", bytes.NewReader(encrypted))
	if err != nil {
		cause := apperr.Internal("audit_export.write_failed", "failed to write audit export artifact").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "audit_export.write_failed", "failed to write audit export artifact", nil, cause)
	}
	manifest["_storage_key"] = key
	return s.finishJobAndLoad(ctx, job, domain.SyncJobCompleted, "", "", manifest)
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
		"payload":       fact.Payload,
	}
}

// auditExportDigest hashes the deterministic manifest fields so a downloaded
// artifact can be verified against the recorded digest (P4-08 hash 验证).
func auditExportDigest(manifest map[string]any) string {
	hasher := sha256.New()
	header, _ := json.Marshal(map[string]any{
		"schema_version": manifest["schema_version"], "type": manifest["type"],
		"tenant_id": manifest["tenant_id"], "account_id": manifest["account_id"],
		"generated_at": manifest["generated_at"], "event_count": manifest["event_count"],
	})
	_, _ = hasher.Write(header)
	events, _ := json.Marshal(manifest["events"])
	_, _ = hasher.Write(events)
	return hex.EncodeToString(hasher.Sum(nil))
}
