package fixture

// Mandatory no-hiding regression (REQ-G2-5): the breaker exemption must
// never hide a real audit swallow. These canonical audit-receiver bare-call
// shapes (P2) must stay red through the BREAKER_EXEMPT_PATTERN filter.

func auditWrite() {
	s.audit.Log(ctx, event)
	s.audit.Record(ctx, f)
}
