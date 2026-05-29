// Command migrate is aero-id's production database migration tool.
//
// It loads the same config as cmd/server (-config flag) and applies the SQL
// migrations under migrations/ against the configured database, tracking applied
// versions in a schema_migrations table.
//
// Usage:
//
//	migrate [-config configs/config.yaml] [-migrations migrations] <command>
//
// Commands:
//
//	up            apply all pending migrations
//	down [N]      roll back the most recent migration (or the most recent N)
//	version       print the current schema version
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/pkg/db"
	"github.com/aero/aero-id/internal/pkg/migrate"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate: "+err.Error())
		os.Exit(1)
	}
}

func usage() string {
	return `usage: migrate [-config path] [-migrations dir] <command>

commands:
  up            apply all pending migrations
  down [N]      roll back the most recent migration (or the most recent N)
  version       print the current schema version`
}

func run(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	cfgPath := fs.String("config", "configs/config.yaml", "path to config file")
	migrationsDir := fs.String("migrations", "migrations", "path to migrations directory")
	fs.Usage = func() { fmt.Fprintln(os.Stderr, usage()) }
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("a command is required\n\n%s", usage())
	}
	cmd := rest[0]

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	gdb, err := db.Open(db.Options{
		DSN:             cfg.Database.DSN(),
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
		LogLevel:        cfg.Database.LogLevel,
	})
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return fmt.Errorf("sql db handle: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	logf := func(format string, a ...any) { fmt.Printf(format+"\n", a...) }
	m := migrate.New(gdb, *migrationsDir, logf)

	switch cmd {
	case "up":
		applied, err := m.Up()
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			fmt.Println("already up to date")
			return nil
		}
		fmt.Printf("applied %d migration(s); now at version %06d\n", len(applied), applied[len(applied)-1])
		return nil

	case "down":
		var arg string
		if len(rest) > 1 {
			arg = rest[1]
		}
		n, err := migrate.ParseDownArg(arg)
		if err != nil {
			return err
		}
		rolled, err := m.Down(n)
		if err != nil {
			return err
		}
		if len(rolled) == 0 {
			fmt.Println("nothing to roll back")
			return nil
		}
		v, err := m.Version()
		if err != nil {
			return err
		}
		fmt.Printf("rolled back %d migration(s); now at version %06d\n", len(rolled), v)
		return nil

	case "version":
		v, err := m.Version()
		if err != nil {
			return err
		}
		if v == 0 {
			fmt.Println("no migrations applied (version 0)")
			return nil
		}
		fmt.Printf("%06d\n", v)
		return nil

	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage())
	}
}
