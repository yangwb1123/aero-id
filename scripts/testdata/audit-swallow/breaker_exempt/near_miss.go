package fixture

// Near-miss pins (receiver-exactness, REQ-G2-5): the exemption must match
// ONLY the exact receiver chain c.breaker. A different receiver (d.breaker)
// or a suffixed receiver (c.breaker2) stays red. These lines are
// intentionally uncompilable/unreferenced — same convention as the existing
// positive/ fixtures; they are text-scanned only.

func nearMisses() {
	d.breaker.Record(false) // different receiver: must stay red
	c.breaker2.Record(true) // different receiver: must stay red
}
