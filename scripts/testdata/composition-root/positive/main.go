// Positive fixture: cfg mutated after Load and a second app.New — C-4 must flag.
package main

func main() {
	cfg, err := config.Load(*cfgPath)
	cfg.AuditGovernance.Enabled = false
	application, err := app.New(ctx, cfg, log)
	application2, err := app.New(ctx, cfg, log)
	_ = application
	_ = application2
}
