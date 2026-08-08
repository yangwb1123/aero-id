//go:build integration

package migrations_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"testing"

	"github.com/aero/aero-id/internal/repository/testpg"
)

// migrationFileRE matches the zero-padded NNNNNN_name.direction.sql naming
// contract (internal/pkg/migrate/migrate.go parseFilename).
var migrationFileRE = regexp.MustCompile(`^(\d{6})_.+\.(up|down)\.sql$`)

// TestMigrationsUpAndDown applies every *.up.sql file in ascending version
// order against a throwaway Postgres container, asserts the schema landmarks
// exist (the three legacy tables plus the audit ledger/publication tables the
// dead-letter replay surface depends on), then applies every *.down.sql file
// in DESCENDING version order — exactly mirroring migrate.Down's ordering
// (internal/pkg/migrate/migrate.go: versions[i] > versions[j]) — and confirms
// clean rollback.
//
// The descending down order is the FK-dependency-safe order: a higher-numbered
// down always runs before a lower-numbered one, so a child table's down (e.g.
// 000045's audit_publications) runs before its parent's (000034's
// audit_ledger). A down that references an object already dropped by a
// higher-numbered down fails the sweep. Before this sweep, only 000001-000003
// were exercised and 000045's down had never been executed by any test.
func TestMigrationsUpAndDown(t *testing.T) {
	db := testpg.Start(t)

	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)

	migrations := discoverMigrations(t, dir)
	if len(migrations) == 0 {
		t.Fatal("no migration pairs discovered")
	}
	versions := make([]int, 0, len(migrations))
	for version := range migrations {
		versions = append(versions, version)
	}
	sort.Ints(versions)

	// Apply UP migrations in ascending order (same order as testpg.Migrate and
	// migrate.Up).
	for _, version := range versions {
		pair := migrations[version]
		sql, err := os.ReadFile(filepath.Join(dir, pair.up))
		if err != nil {
			t.Fatalf("read %s: %v", pair.up, err)
		}
		if res := db.Exec(string(sql)); res.Error != nil {
			t.Fatalf("exec %s: %v", pair.up, res.Error)
		}
	}

	// Assert the schema landmarks survive the full up chain: the three legacy
	// tables (the sweep's original purpose) plus audit_ledger (000034) and
	// audit_publications (000045) — the tables the replay/purge surface reads
	// and writes.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	for _, table := range []string{"files", "messages", "comments", "audit_ledger", "audit_publications", "callback_deliveries"} {
		if !tableExists(t, sqlDB, table) {
			t.Fatalf("table %q missing after up migrations", table)
		}
		t.Logf("table %q: present", table)
	}

	// Apply DOWN migrations in DESCENDING version order (migrate.Down's
	// ordering): 000059 first, 000001 last.
	for i := len(versions) - 1; i >= 0; i-- {
		pair := migrations[versions[i]]
		sql, err := os.ReadFile(filepath.Join(dir, pair.down))
		if err != nil {
			t.Fatalf("read %s: %v", pair.down, err)
		}
		if res := db.Exec(string(sql)); res.Error != nil {
			t.Fatalf("exec %s: %v", pair.down, res.Error)
		}
	}

	// Assert clean rollback: every landmark table is gone.
	for _, table := range []string{"files", "messages", "comments", "audit_ledger", "audit_publications", "callback_deliveries"} {
		if tableExists(t, sqlDB, table) {
			t.Fatalf("table %q still present after down migrations", table)
		}
		t.Logf("table %q: gone (rollback confirmed)", table)
	}
}

// migrationPair holds the up/down file names of one migration version.
type migrationPair struct {
	up   string
	down string
}

// discoverMigrations lists every NNNNNN_name.up.sql / .down.sql pair under the
// migrations directory, failing on orphans (a version with only one direction)
// or malformed names. The pair count must match the file inventory exactly —
// an unpaired migration is a rollback hazard, and a version whose name differs
// between up and down is a Discover()-time error in migrate.go.
func discoverMigrations(t *testing.T, dir string) map[int]migrationPair {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	ups := map[int]string{}
	downs := map[int]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFileRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		version, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("parse version %q: %v", m[1], err)
		}
		switch m[2] {
		case "up":
			ups[version] = e.Name()
		case "down":
			downs[version] = e.Name()
		}
	}
	if len(ups) != len(downs) {
		t.Fatalf("up/down pair count mismatch: %d ups vs %d downs", len(ups), len(downs))
	}
	pairs := make(map[int]migrationPair, len(ups))
	for version, up := range ups {
		down, ok := downs[version]
		if !ok {
			t.Fatalf("migration %06d has an up file (%s) but no down file", version, up)
		}
		pairs[version] = migrationPair{up: up, down: down}
	}
	return pairs
}

func tableExists(t *testing.T, sqlDB *sql.DB, table string) bool {
	t.Helper()
	var ok bool
	if err := sqlDB.QueryRow(
		`SELECT EXISTS(SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)`,
		table,
	).Scan(&ok); err != nil {
		t.Fatalf("query table %q: %v", table, err)
	}
	return ok
}
