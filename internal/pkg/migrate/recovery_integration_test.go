//go:build integration

package migrate_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/pkg/migrate"
	"github.com/aero/aero-id/internal/repository/testpg"
)

const migrationRecoveryTimeout = 10 * time.Second

// TestMigratorDoesNotSelfDeadlockWithSingleConnection proves that holding the
// session lock does not consume one connection while migration work waits for
// another. This is the smallest pool a caller can configure.
func TestMigratorDoesNotSelfDeadlockWithSingleConnection(t *testing.T) {
	db := testpg.Start(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	dir, top := writeFixture(t, 3)
	m := migrate.New(db, dir, nil)
	result := make(chan error, 1)
	go func() {
		_, err := m.Up()
		result <- err
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("up with one connection: %v", err)
		}
	case <-time.After(migrationRecoveryTimeout):
		t.Fatal("up deadlocked with MaxOpenConns=1")
	}
	assertMigrationCount(t, sqlDB, top)
}

// TestConcurrentUpWithMinimalPool checks both sides of the lock contention
// path with only one connection per runner. A runner must not hold its lock
// connection while waiting for a second connection from the same pool.
func TestConcurrentUpWithMinimalPool(t *testing.T) {
	db := testpg.Start(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(2)

	dir, top := writeFixture(t, 3)
	m := migrate.New(db, dir, nil)
	results := startPair(t, m.Up)
	var applied, skipped int
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("concurrent up: %v", result.err)
			}
			switch len(result.versions) {
			case int(top):
				applied++
			case 0:
				skipped++
			default:
				t.Fatalf("runner applied %d migrations, want %d or 0", len(result.versions), top)
			}
		case <-time.After(migrationRecoveryTimeout):
			t.Fatal("concurrent up deadlocked with one connection per runner")
		}
	}
	if applied != 1 || skipped != 1 {
		t.Fatalf("concurrent up results = applied %d, skipped %d; want 1, 1", applied, skipped)
	}
	assertMigrationCount(t, sqlDB, top)
}

// TestFailedMigrationRollsBackAllStatements verifies that a failed migration
// cannot leave either partial DDL or a bookkeeping row behind.
func TestFailedMigrationRollsBackAllStatements(t *testing.T) {
	db := testpg.Start(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	dir, _ := writeFixture(t, 3)
	partial := "CREATE TABLE partial_marker (id BIGINT PRIMARY KEY);\n" +
		"INSERT INTO partial_marker VALUES (1);\n" +
		"SELECT * FROM migration_failure_marker;\n"
	if err := os.WriteFile(filepath.Join(dir, "000002_b.up.sql"), []byte(partial), 0o600); err != nil {
		t.Fatal(err)
	}

	m := migrate.New(db, dir, nil)
	if _, err := m.Up(); err == nil {
		t.Fatal("up: expected migration failure")
	}
	assertTableExists(t, sqlDB, "mig_a", true)
	assertTableExists(t, sqlDB, "partial_marker", false)
	assertTableExists(t, sqlDB, "mig_c", false)
	assertMigrationCount(t, sqlDB, 1)
}

// TestRetrySkipsCommittedMigration models a process dying after migration 1
// commits but before the run reaches migration 2. A fresh migrator must use
// schema_migrations as the recovery watermark and never replay migration 1.
func TestRetrySkipsCommittedMigration(t *testing.T) {
	db := testpg.Start(t)
	dir, top := writeFixture(t, 3)
	failed := "SELECT * FROM migration_failure_marker;\n"
	if err := os.WriteFile(filepath.Join(dir, "000002_b.up.sql"), []byte(failed), 0o600); err != nil {
		t.Fatal(err)
	}

	first := migrate.New(db, dir, nil)
	if _, err := first.Up(); err == nil {
		t.Fatal("initial up: expected migration 2 failure")
	}
	// Migration 1 committed before the later failure. Repair the failed file
	// and discard the first runner just as a crashed process would.
	if err := os.WriteFile(filepath.Join(dir, "000002_b.up.sql"), []byte("CREATE TABLE mig_b (id BIGINT PRIMARY KEY); INSERT INTO mig_b (id) VALUES (1);\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	retry := migrate.New(db, dir, nil)
	applied, err := retry.Up()
	if err != nil {
		t.Fatalf("retry up: %v", err)
	}
	if len(applied) != 2 || applied[0] != 2 || applied[1] != top {
		t.Fatalf("retry applied %v, want [2 %d]", applied, top)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	assertMigrationCount(t, sqlDB, top)
}

func assertMigrationCount(t *testing.T, sqlDB *sql.DB, want uint64) {
	t.Helper()
	var count int
	if err := sqlDB.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if count != int(want) {
		t.Fatalf("schema_migrations has %d rows, want %d", count, want)
	}
}

func assertTableExists(t *testing.T, sqlDB *sql.DB, table string, want bool) {
	t.Helper()
	var exists bool
	if err := sqlDB.QueryRow(
		"SELECT EXISTS(SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)", table,
	).Scan(&exists); err != nil {
		t.Fatalf("query table %s: %v", table, err)
	}
	if exists != want {
		t.Fatalf("table %s exists = %t, want %t", table, exists, want)
	}
}

// TestRecoveryDownRefusesLiveState ensures the recovery migration cannot be
// rolled back while doing so would orphan resumable work. Once all state is
// removed, the same down migration remains usable for a clean rollback.
func TestRecoveryDownRefusesLiveState(t *testing.T) {
	db := testpg.Start(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	root := filepath.Join(testpg.RepoRoot(t), "migrations")
	m := migrate.New(db, root, nil)
	if _, err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}

	accountID := "00000000-0000-0000-0000-000000000070"
	jobID := "00000000-0000-0000-0000-000000000071"
	if err := db.Exec(`INSERT INTO accounts (id, canonical_uid, tenant_id, home_region, created_at, updated_at)
		VALUES (?, 'rollback-user', 'rollback-tenant', 'local', clock_timestamp(), clock_timestamp())`, accountID).Error; err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := db.Exec(`INSERT INTO sync_jobs
		(id, account_id, actor_uid, job_type, status, idempotency_key, request, result)
		VALUES (?, ?, 'rollback-user', 'sync', 'unknown', 'rollback-job', '{}'::jsonb, '{}'::jsonb)`, jobID, accountID).Error; err != nil {
		t.Fatalf("insert job: %v", err)
	}

	cases := []struct {
		name   string
		insert func() error
		clear  func() error
	}{
		{"unknown job", func() error { return nil }, func() error {
			return db.Exec("UPDATE sync_jobs SET status = 'completed' WHERE id = ?", jobID).Error
		}},
		{"replay row", func() error {
			return db.Exec(`INSERT INTO sync_job_summary_replays
				(job_id, source, execution_attempt, request, request_sha256)
				VALUES (?, 'aero-im', 1, '{"account_id":"00000000-0000-0000-0000-000000000070","source":"aero-im"}', ?)`,
				jobID, strings.Repeat("a", 64)).Error
		}, func() error { return db.Exec("DELETE FROM sync_job_summary_replays WHERE job_id = ?", jobID).Error }},
		{"retryable erase", func() error {
			return db.Exec("UPDATE sync_jobs SET status = 'partially_completed', job_type = 'erase', error_code = 'erase.local_projection_failed' WHERE id = ?", jobID).Error
		}, func() error {
			return db.Exec("UPDATE sync_jobs SET status = 'completed', error_code = '' WHERE id = ?", jobID).Error
		}},
		{"expired reconcile lease", func() error {
			return db.Exec("UPDATE sync_jobs SET status = 'running', job_type = 'sync', lease_kind = 'reconcile', lease_owner = 'expired-worker', lease_until = clock_timestamp() - interval '1 second' WHERE id = ?", jobID).Error
		}, func() error {
			return db.Exec("UPDATE sync_jobs SET status = 'completed', lease_kind = '', lease_owner = '', lease_until = NULL, error_code = '' WHERE id = ?", jobID).Error
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.insert(); err != nil {
				t.Fatal(err)
			}
			_, err := m.Down(1)
			if err == nil || !strings.Contains(err.Error(), "refusing to drop sync recovery state") {
				t.Fatalf("down error = %v, want recovery refusal", err)
			}
			if err := tc.clear(); err != nil {
				t.Fatal(err)
			}
		})
	}
	// The refusal itself must be non-destructive. Clear every recovery row
	// before proving that a clean rollback is still allowed.
	for _, table := range []string{"sync_job_summary_replays", "sync_job_source_outcomes", "sync_job_artifacts"} {
		if err := db.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	if _, err := m.Down(1); err != nil {
		t.Fatalf("clean down: %v", err)
	}
	assertTableExists(t, sqlDB, "sync_job_summary_replays", false)
	assertTableExists(t, sqlDB, "sync_job_source_outcomes", false)
	assertTableExists(t, sqlDB, "sync_job_artifacts", false)
}
