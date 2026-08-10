package http

import "github.com/aero/aero-id/internal/repository"

// Rule (e) positive fixture: a handler file may import internal/repository
// only in exactly internal/handler/http/account.go or
// internal/handler/http/audit_ledger.go, and may use only the two enum
// idents (LegacyCutoverStatus, AuditHoldScope). This fixture violates both
// (e1) — wrong file — and (e2) — constructor call, not a type-cast.
func fixture() {
	repository.NewAccountRepo()
}
