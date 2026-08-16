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
//	validate      report migration-set integrity and disk-vs-applied drift without mutating
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
  version       print the current schema version
  validate      report migration-set integrity and disk-vs-applied drift without mutating`
}

// run parses flags, pre-flights the CLI command, then loads config, opens the
// database, and dispatches. CLI-input validation (preflight) always precedes
// config loading so command/argument errors are deterministic and Docker-free
// testable, mirroring cmd/audit-replay, which validates its -actor/ID args
// before touching config. config loading is the shared validation seam that
// fires the audit-governance activation gate and the F3 anchor-scheme boot
// coverage, so it must keep running for every migrate command.
func run(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	cfgPath := fs.String("config", "configs/config.yaml", "path to config file")
	migrationsDir := fs.String("migrations", "migrations", "path to migrations directory")
	fs.Usage = func() { fmt.Fprintln(os.Stderr, usage()) }
	if err := fs.Parse(args); err != nil {
		return err
	}

	cmd, n, err := preflight(fs.Args())
	if err != nil {
		return err
	}

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

	return dispatch(m, cmd, n)
}

// preflight validates CLI input before any config or DB work: a command is
// required, must be whitelisted to {up, down, version, validate}, and an
// optional down count must parse. It returns the command and the down-step
// count (1 when absent). Extra positional arguments beyond the down count are
// ignored, preserving the previous behavior.
func preflight(rest []string) (cmd string, downN int, err error) {
	if len(rest) == 0 {
		return "", 0, fmt.Errorf("a command is required\n\n%s", usage())
	}
	cmd = rest[0]
	switch cmd {
	case "up", "version", "validate":
		return cmd, 1, nil
	case "down":
		arg := ""
		if len(rest) > 1 {
			arg = rest[1]
		}
		n, err := migrate.ParseDownArg(arg)
		if err != nil {
			return "", 0, err
		}
		return cmd, n, nil
	default:
		return "", 0, fmt.Errorf("unknown command %q\n\n%s", cmd, usage())
	}
}

// dispatch executes the pre-validated command. It never sees an unlisted
// command: preflight whitelists before config loading.
func dispatch(m *migrate.Migrator, cmd string, n int) error {
	switch cmd {
	case "up":
		return runUp(m)
	case "down":
		return runDown(m, n)
	case "version":
		return runVersion(m)
	case "validate":
		return validateCmd(m)
	default:
		// Unreachable in practice: preflight rejects unknown commands.
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage())
	}
}

func runUp(m *migrate.Migrator) error {
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
}

func runDown(m *migrate.Migrator, n int) error {
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
}

func runVersion(m *migrate.Migrator) error {
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
}

// validateCmd implements "migrate validate": strict Discover (any D-rule
// violation fails the command), then disk-vs-applied drift reporting. It never
// mutates the database — schema_migrations is read, never created, and no
// migration SQL runs.
func validateCmd(m *migrate.Migrator) error {
	res, err := m.Validate()
	if err != nil {
		return err
	}
	if len(res.AppliedNoFiles) > 0 {
		for _, v := range res.AppliedNoFiles {
			fmt.Fprintf(os.Stderr, "migrate: applied version %06d has no migration files on disk\n", v)
		}
		return fmt.Errorf("validate: %d applied migration(s) missing from disk", len(res.AppliedNoFiles))
	}
	fmt.Printf("migration set: %d up/down pairs; applied: %d; pending: %d; applied-without-files: %d\n",
		res.Pairs, res.Pairs-len(res.Pending), len(res.Pending), len(res.AppliedNoFiles))
	return nil
}
