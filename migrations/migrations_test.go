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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

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

func TestOutboxRetryEligibilityMigration(t *testing.T) {
	db := testpg.Start(t)
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	migrations := discoverMigrations(t, dir)
	versions := make([]int, 0, len(migrations))
	for version := range migrations {
		if version <= 68 {
			versions = append(versions, version)
		}
	}
	sort.Ints(versions)
	for _, version := range versions {
		migration := migrations[version]
		sqlText, err := os.ReadFile(filepath.Join(dir, migration.up))
		if err != nil {
			t.Fatalf("read %s: %v", migration.up, err)
		}
		if err := db.Exec(string(sqlText)).Error; err != nil {
			t.Fatalf("exec %s: %v", migration.up, err)
		}
	}

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	last := time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC)
	rows := []struct {
		retry int
		last  *time.Time
		want  time.Time
	}{
		{retry: 0, want: created},
		{retry: -1, want: created},
		{retry: 1, last: &last, want: last.Add(5 * time.Second)},
		{retry: 10, last: &last, want: last.Add(2560 * time.Second)},
		{retry: 11, last: &last, want: last.Add(time.Hour)},
		{retry: 2, want: created},
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = uuid.New()
		if err := db.Exec(`INSERT INTO outbox_events
			(id, aggregate_type, aggregate_id, event_type, payload, created_at, retry_count, last_retry_at, dead_letter)
			VALUES (?, 'test', ?, 'test.retry', '{}'::jsonb, ?, ?, ?, false)`,
			ids[i], ids[i].String(), created, row.retry, row.last).Error; err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}

	up, err := os.ReadFile(filepath.Join(dir, migrations[69].up))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(up)).Error; err != nil {
		t.Fatalf("apply 000069: %v", err)
	}
	for i, id := range ids {
		var got time.Time
		if err := db.Raw(`SELECT next_attempt_at FROM outbox_events WHERE id = ?`, id).Scan(&got).Error; err != nil {
			t.Fatal(err)
		}
		if !got.Equal(rows[i].want) {
			t.Fatalf("row %d next_attempt_at = %v, want %v", i, got, rows[i].want)
		}
	}
	assertOutboxRetryColumn(t, db, true)
	assertOutboxRetryIndex(t, db, true)

	down, err := os.ReadFile(filepath.Join(dir, migrations[69].down))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(down)).Error; err != nil {
		t.Fatalf("rollback 000069: %v", err)
	}
	assertOutboxRetryColumn(t, db, false)
	assertOutboxRetryIndex(t, db, false)

	// A failure after the DDL/backfill must leave both schema and data at the
	// pre-migration state because the production migrator wraps each migration
	// and its bookkeeping update in one transaction.
	tx := db.Begin()
	if err := tx.Exec(string(up)).Error; err != nil {
		t.Fatalf("begin failed migration: %v", err)
	}
	if err := tx.Exec(`SELECT * FROM migration_failure_fixture`).Error; err == nil {
		t.Fatal("expected forced migration failure")
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	assertOutboxRetryColumn(t, db, false)
	assertOutboxRetryIndex(t, db, false)
}

func assertOutboxRetryColumn(t *testing.T, db interface {
	Raw(string, ...interface{}) *gorm.DB
}, present bool) {
	t.Helper()
	var exists bool
	if err := db.Raw(`SELECT EXISTS(SELECT FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'outbox_events' AND column_name = 'next_attempt_at')`).Scan(&exists).Error; err != nil {
		t.Fatal(err)
	}
	if exists != present {
		t.Fatalf("next_attempt_at exists = %v, want %v", exists, present)
	}
	if !present {
		return
	}
	var column struct {
		Nullable string
		Default  *string
	}
	if err := db.Raw(`SELECT is_nullable AS nullable, column_default AS default
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'outbox_events' AND column_name = 'next_attempt_at'`).Scan(&column).Error; err != nil {
		t.Fatal(err)
	}
	if column.Nullable != "NO" || column.Default == nil || !strings.Contains(*column.Default, "now()") {
		t.Fatalf("next_attempt_at metadata = %+v, want NOT NULL DEFAULT now()", column)
	}
}

func assertOutboxRetryIndex(t *testing.T, db interface {
	Raw(string, ...interface{}) *gorm.DB
}, migrated bool) {
	t.Helper()
	var definition string
	if err := db.Raw(`SELECT pg_get_indexdef(c.oid)
		FROM pg_class c WHERE c.relname = 'idx_outbox_unpublished'`).Scan(&definition).Error; err != nil {
		t.Fatal(err)
	}
	var predicate string
	if err := db.Raw(`SELECT pg_get_expr(i.indpred, i.indrelid)
		FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'idx_outbox_unpublished'`).Scan(&predicate).Error; err != nil {
		t.Fatal(err)
	}
	if migrated {
		if !strings.Contains(definition, "next_attempt_at") || !strings.Contains(predicate, "dead_letter") || strings.Contains(predicate, "now()") {
			t.Fatalf("migrated index definition/predicate = %q/%q, want due key, non-dead filtering, and no volatile predicate", definition, predicate)
		}
	} else if !strings.Contains(definition, "created_at") || strings.Contains(definition, "next_attempt_at") {
		t.Fatalf("rolled-back index definition = %q, want the pre-migration index", definition)
	}
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
