package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// replayActions maps a replay kind to its fixed audit action / resource-type
// pair: audit.publication.requeued and callback.delivery.requeued are the two
// facts the replay surface may mint (controlled-replay doctrine: every
// requeue writes NEW audit facts under these actions).
var replayActions = map[string][2]string{
	"publication": {"audit.publication.requeued", "audit_publication"},
	"delivery":    {"callback.delivery.requeued", "callback_delivery"},
}

// BuildAuditReplayIntent builds the operator intent for one replay kind
// through the shared buildAuditFact path (envelope normalization, partition
// composition, redaction/truncation machinery). The repository clones the
// envelope once per affected tenant and mints a fresh v7 EventID per
// (run, leg, tenant) inside the transaction — it alone knows the tenant set.
//
// operationID is shared across the legs of one CLI run: pass a zero ID to
// mint the run's fresh v7 OperationID (first leg), and pass the returned ID
// for the second leg so every fact of the run carries the same operation id.
// Actor is required, trimmed, and bounded at 256 bytes (fail-closed).
func BuildAuditReplayIntent(ctx context.Context, kind, actor, tenantFilter string,
	allowedTenants []string, operationID domain.ID) (repository.ReplayIntent, error) {
	pair, ok := replayActions[kind]
	if !ok {
		return repository.ReplayIntent{}, fmt.Errorf("invalid replay kind %q", kind)
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return repository.ReplayIntent{}, fmt.Errorf("replay actor is required")
	}
	if len(actor) > 256 {
		return repository.ReplayIntent{}, fmt.Errorf("replay actor must be at most 256 bytes")
	}
	if operationID == (domain.ID{}) {
		operationID = idgen.NewID() // one fresh v7 per CLI run (all legs, all tenants)
	}
	envelope := buildAuditFact(ctx, AuditFactInput{
		Source: "aero-id", SourceRegion: "local", PartitionKey: "requeue",
		OperationID: operationID, CorrelationID: operationID, ActorUID: actor,
		Action: pair[0], ResourceType: pair[1],
	})
	return repository.ReplayIntent{
		OperationID: operationID, ActorUID: actor, TenantFilter: tenantFilter,
		AllowedTenants: allowedTenants, Envelope: *envelope,
	}, nil
}
