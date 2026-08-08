package fixture

// site.go is a negative fixture: the mapped account.viewed site with its
// documented marker — the gate must accept it.
func viewed(ctx context.Context, s *auditor) error {
	// audit-site: standalone-fact
	if s.audit != nil {
		if err := s.audit.Record(ctx, AuditFactInput{Action: "account.viewed"}); err != nil {
			return err
		}
	}
	return nil
}
