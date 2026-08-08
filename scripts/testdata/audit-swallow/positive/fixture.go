package fixture

// Positive fixtures: every historical swallow shape plus the bare-call hole.
func positive() {
	_ = s.audit.Record(ctx, f)                                             // P1: six-site shape
	_, _ = s.audit.Record(ctx, f)                                          // P1: double-assign variant
	_ = s.audit.Log(ctx, event)                                            // P1: Log swallow (the 10 E7 sites)
	_ = s.publications.FinishPublication(ctx, id, w, "completed", "", now) // P1: N3 drain shape
	_ = publisher.Publish(ctx, fact)                                       // P1: inline loop shape
	s.audit.Record(ctx, f)                                                 // P2: bare statement discards error
	s.audit.Log(ctx, event)                                                // P2: bare Log call discards error
	s.publications.ClaimPublication(ctx, w, 5, 1)                          // P2: bare claim
	_ = s.publications.RequeuePublication(ctx, ids, 0, nil)               // P1: B2-5 replay swallow (fact append + requeue)
	s.publications.RequeueDead(ctx, ids, 0, nil)                          // P2: bare B2-5 replay call discards the error
}
