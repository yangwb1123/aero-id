// Positive fixture: the drain Start moved into audit.go — C-2b must flag.
package app

func buildAuditStack(cfg *config.Config, gdb *gorm.DB) func() {
	callbackSvc := buildCallbackSvc(cfg, gdb)
	stopCallbacks := callbackSvc.Start(context.Background(), cfg.Callback.WorkerInterval)
	return stopCallbacks
}
