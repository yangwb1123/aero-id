//go:build integration

package testpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/pkg/migrate"
)

const (
	migrateChildEnv     = "AERO_ID_TESTPG_MIGRATE_CHILD"
	migrateFixtureEnv   = "AERO_ID_TESTPG_MIGRATE_FIXTURE"
	migrateSnapshotEnv  = "AERO_ID_TESTPG_MIGRATE_SNAPSHOT"
	migrateLockName     = "aero-id:migrate"
	migrateChildTimeout = 2 * time.Minute
)

type migrationRow struct {
	Version int64  `json:"version"`
	Name    string `json:"name"`
	Applied bool   `json:"applied"`
}

type migrationSnapshot struct {
	SchemaMigrations bool           `json:"schema_migrations"`
	Marker           bool           `json:"marker"`
	Baseline         bool           `json:"baseline"`
	Failed           bool           `json:"failed"`
	Later            bool           `json:"later"`
	Rows             []migrationRow `json:"rows"`
	LockFree         bool           `json:"lock_free"`
	Error            string         `json:"error,omitempty"`
}

// TestMigrateHelperChild is invoked in a separate process because Migrate has
// the intentional testing.T.Fatalf API and expected failures must not fail the
// parent test.
func TestMigrateHelperChild(t *testing.T) {
	if os.Getenv(migrateChildEnv) != "1" {
		return
	}
	db := Start(t)
	fixture := os.Getenv(migrateFixtureEnv)
	snapshotPath := os.Getenv(migrateSnapshotEnv)
	if fixture == "" || snapshotPath == "" {
		t.Fatal("missing child migration fixture or snapshot path")
	}
	observeMigrationState(t, db, snapshotPath)
	Migrate(t, db, fixture)
}

func observeMigrationState(t *testing.T, db interface{ DB() (*sql.DB, error) }, path string) {
	t.Helper()
	t.Cleanup(func() {
		snapshot := migrationSnapshot{}
		sqlDB, err := db.DB()
		if err != nil {
			snapshot.Error = err.Error()
		} else if snapshot, err = collectMigrationState(sqlDB); err != nil {
			snapshot.Error = err.Error()
		}
		if err := writeMigrationSnapshot(path, snapshot); err != nil {
			t.Errorf("write migration snapshot: %v", err)
		}
	})
}

func writeMigrationSnapshot(path string, snapshot migrationSnapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".migration-snapshot-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func collectMigrationState(db *sql.DB) (migrationSnapshot, error) {
	var snapshot migrationSnapshot
	var err error
	if snapshot.SchemaMigrations, err = tableExists(db, "schema_migrations"); err != nil {
		return snapshot, err
	}
	for name, target := range map[string]*bool{
		"migrate_marker":   &snapshot.Marker,
		"migrate_baseline": &snapshot.Baseline,
		"migrate_failed":   &snapshot.Failed,
		"migrate_later":    &snapshot.Later,
	} {
		if *target, err = tableExists(db, name); err != nil {
			return snapshot, err
		}
	}
	if snapshot.SchemaMigrations {
		rows, queryErr := db.Query(`SELECT version, name, applied_at IS NOT NULL
			FROM schema_migrations ORDER BY version`)
		if queryErr != nil {
			return snapshot, queryErr
		}
		for rows.Next() {
			var row migrationRow
			if err := rows.Scan(&row.Version, &row.Name, &row.Applied); err != nil {
				_ = rows.Close()
				return snapshot, err
			}
			snapshot.Rows = append(snapshot.Rows, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return snapshot, err
		}
		if err := rows.Close(); err != nil {
			return snapshot, err
		}
	}
	snapshot.LockFree, err = tryMigrationLock(db)
	return snapshot, err
}

func tableExists(db *sql.DB, name string) (bool, error) {
	var exists bool
	err := db.QueryRow(`SELECT EXISTS(
		SELECT FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = $1)`, name).Scan(&exists)
	return exists, err
}

func tryMigrationLock(db *sql.DB) (bool, error) {
	conn, err := db.Conn(context.Background())
	if err != nil {
		return false, err
	}
	defer conn.Close()
	var acquired bool
	if err := conn.QueryRowContext(context.Background(), `SELECT pg_try_advisory_lock(hashtext($1))`, migrateLockName).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}
	_, err = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, migrateLockName)
	return err == nil, err
}

func runMigrateChild(t *testing.T, fixture string) (migrationSnapshot, string) {
	t.Helper()
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	ctx, cancel := context.WithTimeout(context.Background(), migrateChildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-tags=integration", "./internal/repository/testpg",
		"-run", "^TestMigrateHelperChild$", "-count=1")
	cmd.Dir = RepoRoot(t)
	cmd.Env = append(os.Environ(),
		migrateChildEnv+"=1",
		migrateFixtureEnv+"="+fixture,
		migrateSnapshotEnv+"="+snapshotPath,
	)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("migration child timed out after %s: %v\nchild output:\n%s", migrateChildTimeout, ctx.Err(), output)
	}
	if err == nil {
		t.Fatalf("child migration unexpectedly succeeded:\n%s", output)
	}
	data, readErr := os.ReadFile(snapshotPath)
	if readErr != nil {
		t.Fatalf("read child snapshot: %v\nchild output:\n%s", readErr, output)
	}
	var snapshot migrationSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("decode child snapshot: %v", err)
	}
	if snapshot.Error != "" {
		t.Fatalf("child snapshot query failed: %s\nchild output:\n%s", snapshot.Error, output)
	}
	return snapshot, string(output)
}

func writeMigration(t *testing.T, dir, version, name, up, down string) {
	t.Helper()
	for suffix, contents := range map[string]string{"up.sql": up, "down.sql": down} {
		path := filepath.Join(dir, version+"_"+name+"."+suffix)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeValidFixture(t *testing.T, count int) string {
	t.Helper()
	dir := t.TempDir()
	writeMigration(t, dir, "000001", "alpha",
		"CREATE TABLE migrate_marker (sequence integer NOT NULL); INSERT INTO migrate_marker VALUES (1);",
		"DROP TABLE migrate_marker;")
	if count >= 2 {
		writeMigration(t, dir, "000002", "beta",
			"INSERT INTO migrate_marker VALUES (2);",
			"DELETE FROM migrate_marker WHERE sequence = 2;")
	}
	if count >= 3 {
		writeMigration(t, dir, "000003", "gamma",
			"INSERT INTO migrate_marker VALUES (3);",
			"DELETE FROM migrate_marker WHERE sequence = 3;")
	}
	return dir
}

func TestMigrateOrderingBookkeepingAndIdempotency(t *testing.T) {
	db := Start(t)
	dir := writeValidFixture(t, 3)
	Migrate(t, db, dir)
	Migrate(t, db, dir)

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	var values []int
	rows, err := sqlDB.Query("SELECT sequence FROM migrate_marker ORDER BY sequence")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var value int
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(values) != "[1 2 3]" {
		t.Fatalf("marker values = %v, want [1 2 3]", values)
	}
	snapshot, err := collectMigrationState(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Rows) != 3 || !snapshot.SchemaMigrations {
		t.Fatalf("migration bookkeeping = %+v, want three rows", snapshot)
	}
	for i, row := range snapshot.Rows {
		if row.Version != int64(i+1) || !row.Applied {
			t.Errorf("row %d = %+v, want version %d and applied", i, row, i+1)
		}
	}
}

func TestMigrateValidationBeforeMutation(t *testing.T) {
	cases := []struct {
		name  string
		bad   string
		files []string
		want  string
	}{
		{"malformed", "000002_bad.UP.sql", nil, "malformed migration filename"},
		{"duplicate", "", []string{"000002_a.up.sql", "000002_a.down.sql", "000002_b.up.sql", "000002_b.down.sql"}, "duplicate migration version"},
		{"unpaired", "000002_orphan.up.sql", nil, "has no .down.sql"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeValidFixture(t, 1)
			if tc.bad != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.bad), []byte("SELECT 1;"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, file := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, file), []byte("SELECT 1;"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, output := runMigrateChild(t, dir)
			if !strings.Contains(output, tc.want) {
				t.Fatalf("child output %q missing %q", output, tc.want)
			}
			if snapshot.SchemaMigrations || snapshot.Marker || len(snapshot.Rows) != 0 {
				t.Fatalf("invalid set mutated database: %+v", snapshot)
			}
		})
	}
}

func TestMigratePerMigrationAtomicity(t *testing.T) {
	dir := t.TempDir()
	writeMigration(t, dir, "000001", "baseline",
		"CREATE TABLE migrate_baseline (id integer PRIMARY KEY);",
		"DROP TABLE migrate_baseline;")
	writeMigration(t, dir, "000002", "failed",
		"CREATE TABLE migrate_failed (id integer); INSERT INTO migrate_failed VALUES (1); SELECT * FROM no_such_migration_table;",
		"DROP TABLE migrate_failed;")
	writeMigration(t, dir, "000003", "later",
		"CREATE TABLE migrate_later (id integer);",
		"DROP TABLE migrate_later;")

	snapshot, output := runMigrateChild(t, dir)
	if !strings.Contains(output, "apply 000002_failed") {
		t.Fatalf("child output %q does not identify failed migration", output)
	}
	if !snapshot.Baseline || snapshot.Failed || snapshot.Later || len(snapshot.Rows) != 1 {
		t.Fatalf("partial migration state: %+v", snapshot)
	}
	if snapshot.Rows[0].Version != 1 || snapshot.Rows[0].Name != "baseline" || !snapshot.LockFree {
		t.Fatalf("bookkeeping or lock state after failure: %+v", snapshot)
	}
}

func TestMigrateAdvisoryLockSerialization(t *testing.T) {
	db := Start(t)
	dir := writeValidFixture(t, 3)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	unlock := holdMigrationLock(t, sqlDB)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := migrate.New(db, dir, nil).Up()
			results <- err
		}()
	}
	if err := waitForMigrationWaiters(t, sqlDB, 2, 10*time.Second); err != nil {
		unlock()
		// Do not turn a probe failure into an unbounded test deadlock. The
		// database cleanup below closes any runner that remains stuck.
		for i := 0; i < 2; i++ {
			select {
			case <-results:
			case <-time.After(time.Second):
				t.Fatal(err)
			}
		}
		t.Fatal(err)
	}
	unlock()
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("migration runners did not finish after lock release")
		}
	}
	snapshot, err := collectMigrationState(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Rows) != 3 || !snapshot.Marker {
		t.Fatalf("serialized migration state: %+v", snapshot)
	}
	if free, err := tryMigrationLock(sqlDB); err != nil || !free {
		t.Fatalf("canonical migration lock not reusable: free=%v err=%v", free, err)
	}
}

func holdMigrationLock(t *testing.T, db *sql.DB) func() {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_lock(hashtext($1))`, migrateLockName); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, migrateLockName)
			_ = conn.Close()
		})
	}
	t.Cleanup(release)
	return release
}

func waitForMigrationWaiters(t *testing.T, db *sql.DB, want int, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	const query = `SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory' AND granted = false AND pid <> pg_backend_pid()
		  AND classid = ((hashtext($1)::bigint >> 32) & 4294967295)
		  AND objid = (hashtext($1)::bigint & 4294967295)
		  AND objsubid = 1`
	for time.Now().Before(deadline) {
		var count int
		if err := db.QueryRow(query, migrateLockName).Scan(&count); err != nil {
			return fmt.Errorf("migration lock probe: %w", err)
		}
		if count >= want {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("migration runners did not become waiters for %q within %s", migrateLockName, timeout)
}
