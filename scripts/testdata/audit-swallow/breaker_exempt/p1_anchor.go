package fixture

// Statement-anchoring pin (REQ-G2-5): `_ = c.breaker.Record(...)` contains
// the c.breaker.Record( substring but is a P1 assign-swallow, not a bare
// breaker observation. A loosened substring exemption would zero this file
// and redden the gate (FM-3). Uncompilable by design: breaker.Record is
// void; kept as a future-proof pin if Record ever gains a return value.

func anchorPin() {
	_ = c.breaker.Record(false)
}
