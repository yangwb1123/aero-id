package fixture

// REQ-2 comment_positive fixture for check-audit-swallow.sh: the exact P1/P2
// shapes that made internal/comment the last excluded swallow site (the dead
// tree's service.go:431 `_ = s.audit.Log(` and the bare-call hole). The
// self-test greps this directory with the exact P1/P2 patterns and requires a
// match (pattern-regression tripwire), and the ROOTS-membership assertion
// pins the Makefile tripwire root (root-removal tripwire).

func resolveReport() {
	_ = s.audit.Log(ctx, event) // P1: the historical dead-tree swallow shape
	s.audit.Log(ctx, event)     // P2: bare call discards the error
}
