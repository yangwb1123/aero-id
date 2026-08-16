// Positive fixture for C-6: same shape as the negative cleaner.go but the
// plain cleaner (nil hold registry) is wired — a regression to the bare
// cleaner call form. scan_new_go must FLAG this file (C-6 zero-bare-form
// check; the WithHolds form is absent too). (Comment text deliberately avoids
// the exact pinned call strings so the fixed-string counts are unambiguous.)
package app

func newFixtures(cfg, gdb, controlMetrics) {
	callbackSvc, auditLedger, auditLedgerRepo, err := buildAuditStack(cfg, gdb, controlMetrics)
	fileRepo := repository.NewFileRepository(gdb, auditLedgerRepo)
	stopCallbacks := callbackSvc.Start(context.Background(), cfg.Callback.WorkerInterval)
	holdRepo := repository.NewAuditHoldRepository(gdb, auditLedgerRepo)
	stopAuditClean = repository.RunAuditCleaner(
		auditRepo, log,
		cfg.DataRetention.AuditRetention,
		cfg.DataRetention.AuditCleanInterval,
	)
}
