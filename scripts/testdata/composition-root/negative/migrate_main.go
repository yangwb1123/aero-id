// Negative fixture: one config.Load on the shared validation seam and one
// migrate.New dispatch — the live cmd/migrate/main.go shape. C-5 must pass.
package main

func main() {
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		panic(err)
	}
	_ = cfg
	m := migrate.New(gdb, *migrationsDir, logf)
	_ = m
}