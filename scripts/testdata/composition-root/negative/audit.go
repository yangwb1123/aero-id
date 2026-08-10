// Negative fixture: audit.go must not start the drain.
package app

func buildAuditStack(cfg *config.Config, gdb *gorm.DB, controlMetrics service.ControlPlaneMetrics) {
	return nil, nil, nil, nil
}
