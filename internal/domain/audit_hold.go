package domain

// AuditHoldScope is the legal-hold target kind (P4-07).
type AuditHoldScope string

const (
	AuditHoldScopeTenant  AuditHoldScope = "tenant"
	AuditHoldScopeAccount AuditHoldScope = "account"
	AuditHoldScopeActor   AuditHoldScope = "actor" // legacy audit_events exemption
)
