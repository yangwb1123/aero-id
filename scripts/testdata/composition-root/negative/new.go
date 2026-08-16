// Negative fixture: mirrors today's exact new.go shapes — exactly one
// buildAuditStack call before NewFileRepository, exactly one drain Start, and
// the hold-aware audit cleaner wired with the shared hold registry (C-6).
package app

func newFixtures(cfg, gdb, controlMetrics) {
	callbackSvc, auditLedger, auditLedgerRepo, err := buildAuditStack(cfg, gdb, controlMetrics)
	fileRepo := repository.NewFileRepository(gdb, auditLedgerRepo)
	stopCallbacks := callbackSvc.Start(context.Background(), cfg.Callback.WorkerInterval)
	holdRepo := repository.NewAuditHoldRepository(gdb, auditLedgerRepo)
	stopAuditClean = repository.RunAuditCleanerWithHolds(
		auditRepo, holdRepo, log,
		cfg.DataRetention.AuditRetention,
		cfg.DataRetention.AuditCleanInterval,
	)
}
