package fixtures

// Negative self-test fixture for check-anchored-event-ids.sh: patterns the
// gate must ACCEPT. This file lives under scripts/testdata and is never
// scanned by the live-tree gate; it only proves the gate accepts the
// sanctioned derivation shapes.

import (
	"time"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

// R-1/R-2 positive shapes: anchored EventID, the single AnchorNamespace
// definition, and the canonical time rendering are all legal.
var AnchorNamespace = idgen.AnchorNamespace // re-export style reference: one definition, never re-minted

func goodHoldSet(tenant, scopeType, scopeID, reason string, expiresAt *time.Time) idgen.ID {
	return idgen.Anchor("audit_hold.set", tenant, scopeType, scopeID, reason, idgen.RenderAnchorTime(expiresAt))
}

func goodCutover(tenant, status, previous string) idgen.ID {
	return idgen.Anchor("legacy.cutover.set", tenant, status, previous)
}

func goodOperation(action, tenant, opID, step, status string) idgen.ID {
	return idgen.Anchor(action, tenant, opID, step, status)
}
