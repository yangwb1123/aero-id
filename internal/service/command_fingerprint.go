package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

// commandFingerprint hashes only normalized intent and non-secret identity.
// Request/trace IDs, execution status, leases and current authorization are not
// intent: retries still have to pass the current authorization checks.
func commandFingerprint(ctx context.Context, intent any) (string, error) {
	identity, _ := IdentityFrom(ctx)
	principal := map[string]string{"actor": actorFromContext(ctx), "tenant": TenantFrom(ctx)}
	if identity != nil {
		principal["issuer"], principal["subject"] = identity.Issuer, identity.Subject
		principal["client"] = identity.ClientID
	}
	encoded, err := json.Marshal(struct {
		Principal map[string]string `json:"principal"`
		Intent    any               `json:"intent"`
	}{principal, intent})
	if err != nil {
		return "", apperr.BadRequest("idempotency.invalid_request", "command request cannot be normalized")
	}
	digest := sha256.Sum256(encoded)
	return "v1:" + hex.EncodeToString(digest[:]), nil
}

func operationFingerprint(ctx context.Context, operation *domain.Operation) (string, error) {
	steps := make([]map[string]any, 0, len(operation.Steps))
	for _, step := range operation.Steps {
		steps = append(steps, map[string]any{"key": step.StepKey, "source": step.Source,
			"action": step.Action, "request": fingerprintPayload(step.RequestPayload),
			"compensation": step.CompensationAction})
	}
	return commandFingerprint(ctx, map[string]any{"kind": "operation", "account": operation.AccountID.String(),
		"type": operation.OperationType, "request": operation.RequestPayload, "steps": steps})
}

func syncJobFingerprint(ctx context.Context, input SyncJobInput, datasets []domain.AccountDataset) (string, error) {
	selection := datasetNames(datasets)
	sort.Strings(selection)
	// A zero account denotes the verified identity's own account, not an
	// arbitrary caller-supplied target. Preserve that scope across retries.
	return commandFingerprint(ctx, map[string]any{"kind": "sync_job", "account": input.AccountID.String(),
		"type": input.JobType, "datasets": selection, "continuation": input.AuditExportContinuationID.String()})
}

func fingerprintPayload(payload map[string]any) map[string]any {
	copy := make(map[string]any, len(payload))
	for key, value := range payload {
		copy[key] = value
	}
	if values, ok := operationDatasetValues(copy["datasets"]); ok {
		names := datasetNames(values)
		sort.Strings(names)
		copy["datasets"] = names
	}
	return copy
}

func checkCommandFingerprint(kind, stored, requested string) error {
	// An unbound historical receipt must not be treated as proof that a new
	// client's body is the original request. GET/list/reconcile stay available.
	if stored == "" || requested == "" || stored != requested {
		return apperr.Conflict(kind+".idempotency_conflict", "idempotency key is bound to a different or unverified command request")
	}
	return nil
}

func reuseOperation(existing *domain.Operation, fingerprint string) (*domain.Operation, error) {
	if err := checkCommandFingerprint("operation", existing.RequestFingerprint, fingerprint); err != nil {
		return nil, err
	}
	return existing, nil
}

func (s *syncJobService) reuseSyncJob(ctx context.Context, existing *domain.SyncJob, fingerprint string) (*domain.SyncJob, error) {
	if err := checkCommandFingerprint("sync", existing.RequestFingerprint, fingerprint); err != nil {
		return nil, err
	}
	if err := authorizeAccountTarget(ctx, s.accountRepo, s.permission, existing.AccountID,
		actorFromContext(ctx), syncPermission(existing.JobType)); err != nil {
		return nil, err
	}
	return existing, nil
}
