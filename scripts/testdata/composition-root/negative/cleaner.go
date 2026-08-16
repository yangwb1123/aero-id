// Negative fixture for C-6: mirrors the post-change new.go shape — it
// satisfies C-1/C-2/C-3 and wires the hold-aware cleaner (the WithHolds call
// form) with the shared hold registry, and contains zero bare cleaner call
// forms. scan_new_go must PASS this file. (Comment text deliberately avoids
// the exact pinned call strings so the fixed-string counts are unambiguous.)
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
