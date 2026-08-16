// Positive fixture: config loaded through a bypass loader — C-5 must flag
// (zero config.Load occurrences means the shared validation seam is gone).
package main

func main() {
	cfg, err := loadConfigOverride(*cfgPath)
	if err != nil {
		panic(err)
	}
	m := migrate.New(gdb, *migrationsDir, logf)
	_ = m
	_ = cfg
}