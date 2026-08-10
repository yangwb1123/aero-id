// Negative fixture: one app.New on the Loaded config, no governance tokens.
package main

func main() {
	cfg, err := config.Load(*cfgPath)
	application, err := app.New(ctx, cfg, log)
	_ = application
}
