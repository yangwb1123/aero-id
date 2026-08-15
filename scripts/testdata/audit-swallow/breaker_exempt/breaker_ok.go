package fixture

// Breaker observations (internal/sso/client.go:299,306,310 shape): bare
// c.breaker.Record(...) calls record circuit-breaker outcome (false =
// availability failure, true = healthy), not audit writes. breaker.Record is
// void, so nothing is discarded and the P1 `_ =` form cannot compile. The
// raw P1/P2 patterns match these lines (P2); BREAKER_EXEMPT_PATTERN must
// filter them all out (REQ-G2-4 liveness self-test).

func breakerObservations() {
	c.breaker.Record(false) // SSO unreachable: availability failure
	c.breaker.Record(true)  // SSO healthy: close the breaker
}
