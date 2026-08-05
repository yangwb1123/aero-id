// Package migrate is a tiny, dependency-free SQL migration runner for aero-id.
//
// It deliberately reuses the same mechanism the test suite already relies on
// (reading ordered "<version>_<name>.up.sql" / ".down.sql" files from the
// migrations/ directory and executing them through the project's GORM handle)
// rather than introducing a new external migration dependency. Applied versions
// are tracked in a schema_migrations bookkeeping table so up/down/version are
// idempotent and safe to run repeatedly.
package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

// migrationsTable is the bookkeeping table recording which versions are applied.
const migrationsTable = "schema_migrations"

// Migration is a single up/down pair discovered on disk.
type Migration struct {
	Version  uint64 // numeric version parsed from the filename prefix
	Name     string // human-readable name (the part after the version prefix)
	UpFile   string // absolute path to the .up.sql file (may be empty)
	DownFile string // absolute path to the .down.sql file (may be empty)
}

// Logf is a minimal logging hook so callers (CLI, app startup) can route output
// to whatever they use without this package importing a logger.
type Logf func(format string, args ...any)

// Migrator applies and rolls back migrations against a GORM-managed database.
type Migrator struct {
	db   *gorm.DB
	dir  string
	logf Logf
}

// New builds a Migrator reading migrations from dir. logf may be nil (silent).
func New(db *gorm.DB, dir string, logf Logf) *Migrator {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Migrator{db: db, dir: dir, logf: logf}
}

// ensureTable creates the bookkeeping table if it does not yet exist.
func (m *Migrator) ensureTable() error {
	const ddl = `CREATE TABLE IF NOT EXISTS ` + migrationsTable + ` (
		version BIGINT PRIMARY KEY,
		name    TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`
	if err := m.db.Exec(ddl).Error; err != nil {
		return fmt.Errorf("create %s: %w", migrationsTable, err)
	}
	return nil
}

// appliedVersions returns the set of versions recorded as applied.
func (m *Migrator) appliedVersions() (map[uint64]bool, error) {
	var versions []uint64
	if err := m.db.Table(migrationsTable).Order("version").Pluck("version", &versions).Error; err != nil {
		return nil, fmt.Errorf("read applied versions: %w", err)
	}
	set := make(map[uint64]bool, len(versions))
	for _, v := range versions {
		set[v] = true
	}
	return set, nil
}

// Version returns the highest applied version, or 0 if none are applied.
func (m *Migrator) Version() (uint64, error) {
	if err := m.ensureTable(); err != nil {
		return 0, err
	}
	var version *uint64
	if err := m.db.Table(migrationsTable).Select("MAX(version)").Scan(&version).Error; err != nil {
		return 0, fmt.Errorf("read current version: %w", err)
	}
	if version == nil {
		return 0, nil
	}
	return *version, nil
}

// Up applies every pending up-migration in ascending version order. It returns
// the list of versions it applied (empty if already up to date).
func (m *Migrator) Up() ([]uint64, error) {
	if err := m.ensureTable(); err != nil {
		return nil, err
	}
	migrations, err := Discover(m.dir)
	if err != nil {
		return nil, err
	}
	applied, err := m.appliedVersions()
	if err != nil {
		return nil, err
	}

	var done []uint64
	for _, mig := range migrations {
		if applied[mig.Version] {
			continue
		}
		if mig.UpFile == "" {
			return done, fmt.Errorf("migration %06d_%s has no .up.sql file", mig.Version, mig.Name)
		}
		sqlText, err := os.ReadFile(mig.UpFile)
		if err != nil {
			return done, fmt.Errorf("read %s: %w", mig.UpFile, err)
		}
		m.logf("applying %06d_%s", mig.Version, mig.Name)
		if err := m.applyTx(string(sqlText), mig, true); err != nil {
			return done, fmt.Errorf("apply %06d_%s: %w", mig.Version, mig.Name, err)
		}
		done = append(done, mig.Version)
	}
	return done, nil
}

// Down rolls back the n most-recently-applied migrations (highest versions
// first). It returns the versions it rolled back.
//
//nolint:cyclop // rollback validates and applies each migration in reverse order atomically.
func (m *Migrator) Down(n int) ([]uint64, error) {
	if n <= 0 {
		return nil, fmt.Errorf("down count must be >= 1, got %d", n)
	}
	if err := m.ensureTable(); err != nil {
		return nil, err
	}
	migrations, err := Discover(m.dir)
	if err != nil {
		return nil, err
	}
	byVersion := make(map[uint64]Migration, len(migrations))
	for _, mig := range migrations {
		byVersion[mig.Version] = mig
	}

	applied, err := m.appliedVersions()
	if err != nil {
		return nil, err
	}
	// Applied versions, highest first.
	versions := make([]uint64, 0, len(applied))
	for v := range applied {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] > versions[j] })

	var done []uint64
	for i := 0; i < n && i < len(versions); i++ {
		v := versions[i]
		mig, ok := byVersion[v]
		if !ok {
			return done, fmt.Errorf("applied version %06d has no migration files on disk", v)
		}
		if mig.DownFile == "" {
			return done, fmt.Errorf("migration %06d_%s has no .down.sql file", mig.Version, mig.Name)
		}
		sqlText, err := os.ReadFile(mig.DownFile)
		if err != nil {
			return done, fmt.Errorf("read %s: %w", mig.DownFile, err)
		}
		m.logf("rolling back %06d_%s", mig.Version, mig.Name)
		if err := m.applyTx(string(sqlText), mig, false); err != nil {
			return done, fmt.Errorf("rollback %06d_%s: %w", mig.Version, mig.Name, err)
		}
		done = append(done, mig.Version)
	}
	return done, nil
}

// applyTx runs one migration's SQL and updates the bookkeeping table in a single
// transaction so a partially-applied migration never desyncs the version table.
func (m *Migrator) applyTx(sqlText string, mig Migration, up bool) error {
	return m.db.Transaction(func(tx *gorm.DB) error {
		if strings.TrimSpace(sqlText) != "" {
			if err := tx.Exec(sqlText).Error; err != nil {
				return err
			}
		}
		if up {
			return tx.Exec(
				"INSERT INTO "+migrationsTable+" (version, name) VALUES (?, ?)",
				mig.Version, mig.Name,
			).Error
		}
		return tx.Exec(
			"DELETE FROM "+migrationsTable+" WHERE version = ?",
			mig.Version,
		).Error
	})
}

// Discover scans dir for "<version>_<name>.up.sql" / ".down.sql" files and
// returns the merged set sorted ascending by version.
func Discover(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %q: %w", dir, err)
	}
	byVersion := make(map[uint64]*Migration)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		version, name, direction, ok := parseFilename(e.Name())
		if !ok {
			continue
		}
		mig := byVersion[version]
		if mig == nil {
			mig = &Migration{Version: version, Name: name}
			byVersion[version] = mig
		}
		full := filepath.Join(dir, e.Name())
		switch direction {
		case "up":
			mig.UpFile = full
		case "down":
			mig.DownFile = full
		}
	}
	out := make([]Migration, 0, len(byVersion))
	for _, mig := range byVersion {
		out = append(out, *mig)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// parseFilename extracts the numeric version, name, and direction ("up"/"down")
// from a migration filename like "000001_create_files.up.sql". It returns
// ok=false for anything that is not a recognized migration file.
func parseFilename(name string) (version uint64, migName, direction string, ok bool) {
	var suffix string
	switch {
	case strings.HasSuffix(name, ".up.sql"):
		direction = "up"
		suffix = ".up.sql"
	case strings.HasSuffix(name, ".down.sql"):
		direction = "down"
		suffix = ".down.sql"
	default:
		return 0, "", "", false
	}
	stem := strings.TrimSuffix(name, suffix)
	idx := strings.IndexByte(stem, '_')
	if idx <= 0 {
		return 0, "", "", false
	}
	v, err := strconv.ParseUint(stem[:idx], 10, 64)
	if err != nil {
		return 0, "", "", false
	}
	return v, stem[idx+1:], direction, true
}

// ParseDownArg parses the optional count argument to the "down" subcommand.
// An empty argument means a single step. The result is always >= 1.
func ParseDownArg(arg string) (int, error) {
	if strings.TrimSpace(arg) == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil {
		return 0, fmt.Errorf("invalid down count %q: must be a positive integer", arg)
	}
	if n < 1 {
		return 0, fmt.Errorf("invalid down count %d: must be >= 1", n)
	}
	return n, nil
}
