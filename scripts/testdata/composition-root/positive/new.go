// Positive fixture: the buildAuditStack call is wrapped/renamed — C-1 must flag.
package app

func newFixtures(cfg, gdb, controlMetrics) {
	callbackSvc, auditLedger, auditLedgerRepo, err := buildAuditStackNoGate(cfg, gdb, controlMetrics)
	fileRepo := repository.NewFileRepository(gdb, auditLedgerRepo)
	stopCallbacks := callbackSvc.Start(context.Background(), cfg.Callback.WorkerInterval)
}
