package service

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// buildLegacyAuditFact reproduces the Log-adapter mapping
// (internal/service/audit_ledger.go: ActorUID, Action, SubjectType→
// ResourceType, SubjectID→ResourceID, Meta→Payload) plus TargetUID→
// Payload["target_uid"] — the adapter drops TargetUID today; the in-tx
// conversion is the moment to stop losing it (B2-1 REQ-3.2).
//
// EventID policy (B2-1 REQ-3.3): message delete and comment resolve keep
// their existing anchored EventIDs byte-identical (message ID / report ID —
// both are load-bearing retry-dedup anchors), so this helper takes an
// optional anchoredID. The other eight legacy-Log sites (admin×2, ban×2,
// block×2, file-share×2) mint a fresh v7 per call: a stable reused ID across
// distinct intents would let ErrAuditDuplicate roll back a legitimate domain
// write (the D3 hazard).
//
// C2 allowlist: the fresh mint below is the ONLY EventID: idgen.NewID()
// outside the historical ReplayDelivery site; scripts/check-anchored-event-ids.sh
// lists this file in its allowlist with this rationale.
func buildLegacyAuditFact(ctx context.Context, actor, action, subjectType, subjectID, targetUID string,
	meta map[string]any, anchoredID domain.ID) *domain.AuditFact {
	fact := buildAuditFact(ctx, AuditFactInput{
		Source: "aero-id", EventID: anchoredID, ActorUID: actor, Action: action,
		ResourceType: subjectType, ResourceID: subjectID, Payload: meta,
	})
	if anchoredID == (domain.ID{}) {
		fact.EventID = idgen.NewID()
	}
	if targetUID != "" {
		fact.Payload["target_uid"] = targetUID
	}
	return fact
}
