package http

import "github.com/aero/aero-id/internal/repository"

// EX-2 negative fixture: the enum type-cast is the only allowed handler
// repository access, and only from exactly internal/handler/http/account.go
// or internal/handler/http/audit_ledger.go (ident allow-list, not line
// pinning). Casts of LegacyCutoverStatus / AuditHoldScope pass both (e1)
// and (e2).
func cast() repository.AuditHoldScope {
	return repository.AuditHoldScope("tenant")
}
