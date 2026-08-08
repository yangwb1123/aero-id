package fixture

// I6 cmd-path fixture for check-audit-swallow.sh: Requeue* swallow shapes
// under a root named cmd/ — the same shapes the Makefile gate root `cmd`
// must police in the real tree. The self-test greps this directory with the
// exact P1/P2 patterns and requires a match.

func replay() {
	_ = s.publications.RequeuePublication(ctx, ids, 0, nil) // P1: replay swallow under cmd/
	s.publications.RequeueDead(ctx, ids, 0, nil)            // P2: bare replay call under cmd/
}
