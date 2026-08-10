// Negative fixture: mirrors today's exact new.go shapes — exactly one
// buildAuditStack call before NewFileRepository, exactly one drain Start.
package app

func newFixtures(cfg, gdb, controlMetrics) {
	callbackSvc, auditLedger, auditLedgerRepo, err := buildAuditStack(cfg, gdb, controlMetrics)
	fileRepo := repository.NewFileRepository(gdb, auditLedgerRepo)
	stopCallbacks := callbackSvc.Start(context.Background(), cfg.Callback.WorkerInterval)
}
