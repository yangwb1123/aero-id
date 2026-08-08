package fixture

// unmapped.go is a positive fixture: a bare audit Record with no manifest row
// and no marker — the gate must flag it (and its self-test must match).
func unmapped(ctx context.Context, s *auditor) error {
	if s != nil {
		if err := s.audit.Record(ctx, input); err != nil {
			return err
		}
	}
	return nil
}
