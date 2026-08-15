package http

import "github.com/aero/aero-id/internal/repository"

// Rule (e) positive fixture: no handler file may import internal/repository
// (the enum-cast allow-list was deleted when LegacyCutoverStatus and
// AuditHoldScope moved to internal/domain). Any repository import under
// internal/handler/ is a violation, from any file.
func cast() {
	repository.NewAuditHoldRepository(nil, nil)
}
