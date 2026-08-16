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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

// migrationsTable is the bookkeeping table recording which versions are applied.
const migrationsTable = "schema_migrations"

// migrationFileRE is the canonical migration filename contract, shared with the
// bash gate (scripts/check-migration-set.sh G1-G3) and migrations_test.go: a
// zero-padded NNNNNN version, a non-empty name, and exactly ".up.sql" or
// ".down.sql". Discover rejects any migration-like file that fails it.
var migrationFileRE = regexp.MustCompile(`^[0-9]{6}_.+\.(up|down)\.sql$`)

// migrationFilePattern is the human-readable form of migrationFileRE, used in
// stable error messages.
const migrationFilePattern = "NNNNNN_name.up.sql / NNNNNN_name.down.sql"

// discoveredFile tracks one migration file during Discover's two-pass merge.
type discoveredFile struct {
	path string // absolute path to the file
	name string // name component (the part after the version prefix)
}

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
// the list of versions it applied (empty if already up to date). The migration
// set is validated before any database access so a broken set fails before
// schema_migrations is created (fail-before-mutate). All database work runs
// under a session-level advisory lock (withSessionLock) so concurrent Up/Down
// runs against one database serialize instead of double-applying migrations.
func (m *Migrator) Up() ([]uint64, error) {
	migrations, err := Discover(m.dir)
	if err != nil {
		return nil, err
	}

	var done []uint64
	err = m.withSessionLock(context.Background(), func() error {
		if err := m.ensureTable(); err != nil {
			return err
		}
		applied, err := m.appliedVersions()
		if err != nil {
			return err
		}

		for _, mig := range migrations {
			if applied[mig.Version] {
				continue
			}
			if mig.UpFile == "" {
				return fmt.Errorf("migration %06d_%s has no .up.sql file", mig.Version, mig.Name)
			}
			sqlText, err := os.ReadFile(mig.UpFile)
			if err != nil {
				return fmt.Errorf("read %s: %w", mig.UpFile, err)
			}
			m.logf("applying %06d_%s", mig.Version, mig.Name)
			if err := m.applyTx(string(sqlText), mig, true); err != nil {
				return fmt.Errorf("apply %06d_%s: %w", mig.Version, mig.Name, err)
			}
			done = append(done, mig.Version)
		}
		return nil
	})
	return done, err
}

// Down rolls back the n most-recently-applied migrations (highest versions
// first). It returns the versions it rolled back.
//
//nolint:cyclop // rollback validates and applies each migration in reverse order atomically.
func (m *Migrator) Down(n int) ([]uint64, error) {
	if n <= 0 {
		return nil, fmt.Errorf("down count must be >= 1, got %d", n)
	}
	// Validate the migration set before touching the database (fail-before-mutate,
	// symmetric with Up). All database work runs under the same session-level
	// advisory lock as Up so concurrent runs serialize on one key.
	migrations, err := Discover(m.dir)
	if err != nil {
		return nil, err
	}

	var done []uint64
	err = m.withSessionLock(context.Background(), func() error {
		done, err = m.downLocked(n, migrations)
		return err
	})
	return done, err
}

// downLocked runs the rollback steps while the session lock is held. It is a
// separate method so Down stays under the funlen budget and the lock wrapper
// stays a pure acquire/run/release.
//
//nolint:cyclop // rollback validates and applies each migration in reverse order atomically.
func (m *Migrator) downLocked(n int, migrations []Migration) ([]uint64, error) {
	if err := m.ensureTable(); err != nil {
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
// returns the merged set sorted ascending by version. A broken migration set
// errors, naming the offending file(s) and violation class:
//   - D1 duplicate version (a version in more than one .up.sql or .down.sql);
//   - D2 unpaired migration (an up without its down, or vice versa);
//   - D3 name mismatch between a version's up and down files;
//   - D4 malformed migration-like filename (fails the canonical contract);
//   - D5 non-migration files (README.md, notes.txt, tests, dotfiles, dirs)
//     are silently ignored.
func Discover(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %q: %w", dir, err)
	}
	ups := make(map[uint64]discoveredFile)
	downs := make(map[uint64]discoveredFile)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		version, migName, direction, ok := parseFilename(e.Name())
		if !ok {
			// Malformed migration-like files must error; everything else is a
			// non-migration file and stays ignored.
			if isMigrationLike(e.Name()) {
				return nil, fmt.Errorf("malformed migration filename %q (want %s)", e.Name(), migrationFilePattern)
			}
			continue
		}
		if !migrationFileRE.MatchString(e.Name()) {
			return nil, fmt.Errorf("malformed migration filename %q (want %s)", e.Name(), migrationFilePattern)
		}
		full := filepath.Join(dir, e.Name())
		file := discoveredFile{path: full, name: migName}
		var target map[uint64]discoveredFile
		switch direction {
		case "up":
			target = ups
		case "down":
			target = downs
		}
		if err := registerFile(target, version, file); err != nil {
			return nil, err
		}
	}
	return mergePairs(ups, downs)
}

// registerFile records one parsed migration file in the direction map, erroring
// on a duplicate version (D1) and naming BOTH files.
func registerFile(m map[uint64]discoveredFile, version uint64, file discoveredFile) error {
	if prev, dup := m[version]; dup {
		return fmt.Errorf("duplicate migration version %06d: %s and %s", version, prev.path, file.path)
	}
	m[version] = file
	return nil
}

// mergePairs combines the per-direction file maps into a Migration list sorted
// ascending by version, enforcing the up/down pair invariant (D2) and up/down
// name consistency (D3).
func mergePairs(ups, downs map[uint64]discoveredFile) ([]Migration, error) {
	versions := unionVersions(ups, downs)
	out := make([]Migration, 0, len(versions))
	for _, v := range versions {
		up, hasUp := ups[v]
		down, hasDown := downs[v]
		if hasUp != hasDown {
			if hasUp {
				return nil, fmt.Errorf("migration %06d has no .down.sql file (%s)", v, up.path)
			}
			return nil, fmt.Errorf("migration %06d has no .up.sql file (%s)", v, down.path)
		}
		if up.name != down.name {
			return nil, fmt.Errorf("migration %06d name mismatch: up name %q vs down name %q", v, up.name, down.name)
		}
		out = append(out, Migration{Version: v, Name: up.name, UpFile: up.path, DownFile: down.path})
	}
	return out, nil
}

// unionVersions returns the sorted union of the version keys of both maps.
func unionVersions(ups, downs map[uint64]discoveredFile) []uint64 {
	versions := make([]uint64, 0, len(ups)+len(downs))
	seen := make(map[uint64]bool, len(ups)+len(downs))
	for v := range ups {
		if !seen[v] {
			seen[v] = true
			versions = append(versions, v)
		}
	}
	for v := range downs {
		if !seen[v] {
			seen[v] = true
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	return versions
}

// isMigrationLike reports whether name looks like a migration file but fails
// the canonical contract: it either ends with ".up.sql"/".down.sql"
// (case-insensitive) or carries a [0-9]{6}_ prefix and a ".sql" suffix
// (case-insensitive). Such files are malformed and must error at Discover time;
// anything else is a non-migration file and is ignored. The same predicate is
// implemented in scripts/check-migration-set.sh (G3).
func isMigrationLike(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".up.sql") || strings.HasSuffix(lower, ".down.sql") {
		return true
	}
	if len(name) < 7 || !strings.HasSuffix(lower, ".sql") {
		return false
	}
	for i := 0; i < 6; i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return name[6] == '_'
}

// ValidateResult reports disk-vs-applied drift computed by ValidateSet.
type ValidateResult struct {
	Pairs          int      // complete up/down pairs on disk
	Pending        []uint64 // on-disk versions not yet applied
	AppliedNoFiles []uint64 // applied versions with no files on disk
}

// ValidateSet is the pure, DB-free core of Validate: it compares the on-disk
// migration set against the set of applied versions. applied may be nil.
func ValidateSet(migrations []Migration, applied map[uint64]bool) ValidateResult {
	var res ValidateResult
	onDisk := make(map[uint64]bool, len(migrations))
	for _, mig := range migrations {
		onDisk[mig.Version] = true
		if mig.UpFile != "" && mig.DownFile != "" {
			res.Pairs++
		}
		if !applied[mig.Version] {
			res.Pending = append(res.Pending, mig.Version)
		}
	}
	for v := range applied {
		if !onDisk[v] {
			res.AppliedNoFiles = append(res.AppliedNoFiles, v)
		}
	}
	sort.Slice(res.Pending, func(i, j int) bool { return res.Pending[i] < res.Pending[j] })
	sort.Slice(res.AppliedNoFiles, func(i, j int) bool { return res.AppliedNoFiles[i] < res.AppliedNoFiles[j] })
	return res
}

// Validate reports migration-set integrity and disk-vs-applied drift without
// mutating anything: it never creates schema_migrations and never executes
// migration SQL. A fresh database without schema_migrations is reported as all
// pending, not an error.
func (m *Migrator) Validate() (ValidateResult, error) {
	migrations, err := Discover(m.dir)
	if err != nil {
		return ValidateResult{}, err
	}
	applied := make(map[uint64]bool)
	exists, err := m.schemaMigrationsExists()
	if err != nil {
		return ValidateResult{}, err
	}
	if exists {
		if applied, err = m.appliedVersions(); err != nil {
			return ValidateResult{}, err
		}
	}
	return ValidateSet(migrations, applied), nil
}

// schemaMigrationsExists reports whether the bookkeeping table already exists,
// reading information_schema without creating anything.
func (m *Migrator) schemaMigrationsExists() (bool, error) {
	var exists bool
	if err := m.db.Raw(
		`SELECT EXISTS(SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name=?)`,
		migrationsTable,
	).Scan(&exists).Error; err != nil {
		return false, fmt.Errorf("check %s: %w", migrationsTable, err)
	}
	return exists, nil
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
