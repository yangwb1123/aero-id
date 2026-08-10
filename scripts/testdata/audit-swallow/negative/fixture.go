package fixture

// Negative fixtures: legitimate handling shapes that must never match.
func negative() error {
	if err := s.audit.Record(ctx, f); err != nil { // converted sites
		return err
	}
	return s.audit.Record(ctx, f) // propagated return
	err := s.audit.Record(ctx, f)
	if err != nil {
		return err
	}
	// _ = s.audit.Record(ctx, f)  — comment lines are not code
	// _ = s.publications.FinishPublication(ctx, id, w, "completed", "", now)
	if err := s.audit.Log(ctx, event); err != nil { // converted Log sites
		return err
	}
	return s.audit.Log(ctx, event) // propagated Log return
	d.pusher.Publish(ctx, ev)   // best-effort SSE hub: excluded from P2
	_ = p.tokens.Invalidate(binding) // documented exclusion: token-cache invalidation (auditgovernance/publisher.go:106), not an audit write
	return nil
}
