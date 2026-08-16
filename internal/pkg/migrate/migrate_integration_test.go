//go:build integration

package migrate_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/pkg/migrate"
	"github.com/aero/aero-id/internal/repository/testpg"
)

// TestMigratorUpAndDown applies the real migrations/ directory against a fresh
// Postgres container via the Migrator, asserts it reaches the highest on-disk
// version and that an expected table exists, then rolls one migration back.
func TestMigratorUpAndDown(t *testing.T) {
	db := testpg.Start(t)
	dir := filepath.Join(testpg.RepoRoot(t), "migrations")

	migs, err := migrate.Discover(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(migs) == 0 {
		t.Fatalf("no migrations discovered in %s", dir)
	}
	top := migs[len(migs)-1].Version

	m := migrate.New(db, dir, func(f string, a ...any) { t.Logf(f, a...) })

	// Apply all pending migrations.
	applied, err := m.Up()
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(applied) != len(migs) {
		t.Fatalf("applied %d migrations, expected %d", len(applied), len(migs))
	}

	v, err := m.Version()
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if v != top {
		t.Fatalf("version = %d after up, want %d", v, top)
	}

	// Up again should be a no-op.
	again, err := m.Up()
	if err != nil {
		t.Fatalf("second up: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second up applied %d migrations, want 0", len(again))
	}

	// The first migration creates the files table; it must exist now.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	var exists bool
	if err := sqlDB.QueryRow(
		`SELECT EXISTS(SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name='files')`,
	).Scan(&exists); err != nil {
		t.Fatalf("query files table: %v", err)
	}
	if !exists {
		t.Fatal("files table missing after up migrations")
	}

	// Roll back one migration; version should drop below the top.
	rolled, err := m.Down(1)
	if err != nil {
		t.Fatalf("down: %v", err)
	}
	if len(rolled) != 1 || rolled[0] != top {
		t.Fatalf("rolled back %v, want [%d]", rolled, top)
	}
	v2, err := m.Version()
	if err != nil {
		t.Fatalf("version after down: %v", err)
	}
	if v2 >= top {
		t.Fatalf("version = %d after down, want < %d", v2, top)
	}
}

// TestValidateRoundtrip pins the up/validate roundtrip against a real
// Postgres: on a fresh database Validate() reports all pairs pending WITHOUT
// creating schema_migrations; after Up() it reports every on-disk pair applied
// with no drift.
func TestValidateRoundtrip(t *testing.T) {
	db := testpg.Start(t)
	dir := filepath.Join(testpg.RepoRoot(t), "migrations")

	m := migrate.New(db, dir, func(f string, a ...any) { t.Logf(f, a...) })

	migs, err := migrate.Discover(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}

	// Fresh database: validate reports all pending and must not create
	// schema_migrations (non-mutation contract).
	res, err := m.Validate()
	if err != nil {
		t.Fatalf("validate on fresh db: %v", err)
	}
	var exists bool
	if err := sqlDB.QueryRow(
		`SELECT EXISTS(SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name='schema_migrations')`,
	).Scan(&exists); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if exists {
		t.Fatal("validate created schema_migrations on a fresh database")
	}
	if res.Pairs != len(migs) || len(res.Pending) != len(migs) || len(res.AppliedNoFiles) != 0 {
		t.Fatalf("validate on fresh db: %+v (want %d pending, 0 drift)", res, len(migs))
	}

	// Apply everything, then validate: no pending, no drift, pairs unchanged.
	applied, err := m.Up()
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(applied) != len(migs) {
		t.Fatalf("applied %d migrations, expected %d", len(applied), len(migs))
	}
	res, err = m.Validate()
	if err != nil {
		t.Fatalf("validate after up: %v", err)
	}
	if res.Pairs != len(migs) || len(res.Pending) != 0 || len(res.AppliedNoFiles) != 0 {
		t.Fatalf("validate after up: %+v (want %d pairs, 0 pending, 0 drift)", res, len(migs))
	}
}

// migrateLockKey is the pinned advisory-lock literal serializing Up/Down runs
// (unit-pinned by TestAdvisoryLockKeyStable in package migrate). The external
// test package cannot see the unexported constant, so these tests write the
// literal directly into coordinator/try-lock SQL.
const migrateLockKey = "aero-id:migrate"

// runResult is the outcome of one Up/Down invocation from a goroutine.
type runResult struct {
	versions []uint64
	err      error
}

// writeFixture writes n up/down migration pairs into a fresh temp dir, each
// version creating a distinct table (mig_a, mig_b, ...) so duplicate-object
// failures are detectable. Returns the dir and the top version.
func writeFixture(t *testing.T, n int) (string, uint64) {
	t.Helper()
	dir := t.TempDir()
	for i := 1; i <= n; i++ {
		name := string(rune('a' + i - 1))
		table := fmt.Sprintf("mig_%s", name)
		version := fmt.Sprintf("%06d", i)
		if err := os.WriteFile(filepath.Join(dir, version+"_"+name+".up.sql"), []byte("CREATE TABLE "+table+" (id BIGINT PRIMARY KEY);\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, version+"_"+name+".down.sql"), []byte("DROP TABLE "+table+";\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, uint64(n)
}

// holdMigrateKey takes the package advisory lock on a dedicated connection and
// returns a release function. The connection is closed on cleanup (and again on
// release) so a failed assertion can never wedge the container: closing the
// conn releases the session lock.
func holdMigrateKey(t *testing.T, sqlDB *sql.DB) func() {
	t.Helper()
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("advisory lock conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`SELECT pg_advisory_lock(hashtext('%s'))`, migrateLockKey)); err != nil {
		_ = conn.Close()
		t.Fatalf("pg_advisory_lock(%s): %v", migrateLockKey, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return func() {
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`SELECT pg_advisory_unlock(hashtext('%s'))`, migrateLockKey)); err != nil {
			t.Errorf("pg_advisory_unlock(%s): %v", migrateLockKey, err)
		}
		_ = conn.Close()
	}
}

// waitForBlockedAdvisory polls pg_locks until at least min ungranted advisory
// waits (excluding the polling backend) are visible, or fails after timeout.
// Mirrors internal/repository/barrier_helpers_test.go:41-48.
func waitForBlockedAdvisory(t *testing.T, sqlDB *sql.DB, min int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	const query = `SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory' AND granted = false AND pid <> pg_backend_pid()`
	for {
		var n int
		if err := sqlDB.QueryRow(query).Scan(&n); err != nil {
			t.Fatalf("advisory lock probe: %v", err)
		}
		if n >= min {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d blocked advisory wait(s) not observed within %s (saw %d)", min, timeout, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// tryAdvisoryLock reports whether the package key is free: it attempts a
// session-level try-lock on a DEDICATED connection (never a pooled one, so the
// attempt cannot leak a session lock into the pool) and immediately releases it.
func tryAdvisoryLock(t *testing.T, sqlDB *sql.DB) bool {
	t.Helper()
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("try-lock conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	var ok bool
	if err := conn.QueryRowContext(ctx, fmt.Sprintf(`SELECT pg_try_advisory_lock(hashtext('%s'))`, migrateLockKey)).Scan(&ok); err != nil {
		t.Fatalf("pg_try_advisory_lock(%s): %v", migrateLockKey, err)
	}
	if ok {
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`SELECT pg_advisory_unlock(hashtext('%s'))`, migrateLockKey)); err != nil {
			t.Fatalf("pg_advisory_unlock(%s): %v", migrateLockKey, err)
		}
	}
	return ok
}

// startPair launches fn twice behind a start barrier and returns a buffered
// channel of results, so both runners contend from the same instant.
func startPair(t *testing.T, fn func() ([]uint64, error)) <-chan runResult {
	t.Helper()
	start := make(chan struct{})
	results := make(chan runResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			versions, err := fn()
			results <- runResult{versions: versions, err: err}
		}()
	}
	close(start)
	return results
}

// assertUpPair asserts the concurrent-Up contract: no error from either runner;
// exactly one applied all N migrations and the other applied 0; the database
// ends fully applied (N rows, top version, every fixture table present).
func assertUpPair(t *testing.T, sqlDB *sql.DB, m *migrate.Migrator, results <-chan runResult, top uint64) {
	t.Helper()
	var one, zero *runResult
	for i := 0; i < 2; i++ {
		res := <-results
		if res.err != nil {
			t.Fatalf("concurrent up: %v", res.err)
		}
		switch len(res.versions) {
		case int(top):
			one = &res
		case 0:
			zero = &res
		default:
			t.Fatalf("concurrent up: runner applied %d migrations, want %d or 0", len(res.versions), top)
		}
	}
	if one == nil || zero == nil {
		t.Fatalf("concurrent up: want one runner with %d applied and one with 0, got one=%v zero=%v", top, one, zero)
	}
	var n int
	if err := sqlDB.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if n != int(top) {
		t.Fatalf("schema_migrations has %d rows, want %d", n, top)
	}
	if v, err := m.Version(); err != nil || v != top {
		t.Fatalf("version = %d (err %v), want %d", v, err, top)
	}
	for i := uint64(1); i <= top; i++ {
		table := fmt.Sprintf("mig_%c", 'a'+i-1)
		var exists bool
		if err := sqlDB.QueryRow(`SELECT EXISTS(SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)`, table).Scan(&exists); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %s missing after concurrent up", table)
		}
	}
}

// TestUpBlocksWhileKeyHeld (A1.1, Up): while a coordinator session holds the
// package key, Up() must block on exactly that key (session semantics, visible
// as an ungranted advisory wait) and complete applying every pending migration
// only after the coordinator releases.
func TestUpBlocksWhileKeyHeld(t *testing.T) {
	db := testpg.Start(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	dir, top := writeFixture(t, 3)
	m := migrate.New(db, dir, func(f string, a ...any) { t.Logf(f, a...) })

	unlock := holdMigrateKey(t, sqlDB)
	done := make(chan runResult, 1)
	go func() {
		versions, err := m.Up()
		done <- runResult{versions: versions, err: err}
	}()

	waitForBlockedAdvisory(t, sqlDB, 1, 10*time.Second)
	select {
	case res := <-done:
		t.Fatalf("Up completed while key held: %+v", res)
	default:
	}
	unlock()

	res := <-done
	if res.err != nil {
		t.Fatalf("up after release: %v", res.err)
	}
	if len(res.versions) != int(top) {
		t.Fatalf("up applied %d migrations, want %d", len(res.versions), top)
	}
	if v, err := m.Version(); err != nil || v != top {
		t.Fatalf("version = %d (err %v), want %d", v, err, top)
	}
}

// TestDownBlocksWhileKeyHeld (A1.1, Down): the same blocking handshake on a
// fully-applied fixture; Down(1) rolls back exactly the top version after
// release.
func TestDownBlocksWhileKeyHeld(t *testing.T) {
	db := testpg.Start(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	dir, top := writeFixture(t, 3)
	m := migrate.New(db, dir, func(f string, a ...any) { t.Logf(f, a...) })
	if _, err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}

	unlock := holdMigrateKey(t, sqlDB)
	done := make(chan runResult, 1)
	go func() {
		versions, err := m.Down(1)
		done <- runResult{versions: versions, err: err}
	}()

	waitForBlockedAdvisory(t, sqlDB, 1, 10*time.Second)
	select {
	case res := <-done:
		t.Fatalf("Down completed while key held: %+v", res)
	default:
	}
	unlock()

	res := <-done
	if res.err != nil {
		t.Fatalf("down after release: %v", res.err)
	}
	if len(res.versions) != 1 || res.versions[0] != top {
		t.Fatalf("down rolled back %v, want [%d]", res.versions, top)
	}
	if v, err := m.Version(); err != nil || v != top-1 {
		t.Fatalf("version = %d (err %v), want %d", v, err, top-1)
	}
}

// TestLockReleasedAfterSuccess (A1.2): after a completed Up() and Down(1), a
// fresh session's try-lock on the package key succeeds immediately — the lock
// was not leaked onto a pooled connection.
func TestLockReleasedAfterSuccess(t *testing.T) {
	db := testpg.Start(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	dir, _ := writeFixture(t, 3)
	m := migrate.New(db, dir, func(f string, a ...any) { t.Logf(f, a...) })

	if _, err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}
	if _, err := m.Down(1); err != nil {
		t.Fatalf("down: %v", err)
	}
	if !tryAdvisoryLock(t, sqlDB) {
		t.Fatal("advisory lock leaked after Up/Down success (pooled-conn leak)")
	}
}

// TestLockReleasedOnError (A1.3): a failing migration (SQL that passes Discover
// but fails execution) errors out of Up/Down and still releases the key; the
// error path must not strand the session lock.
func TestLockReleasedOnError(t *testing.T) {
	t.Run("up", func(t *testing.T) {
		db := testpg.Start(t)
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("sql.DB: %v", err)
		}
		dir, _ := writeFixture(t, 3)
		if err := os.WriteFile(filepath.Join(dir, "000002_b.up.sql"), []byte("SELECT * FROM mig_no_such_table_xyz;\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		m := migrate.New(db, dir, func(f string, a ...any) { t.Logf(f, a...) })

		if _, err := m.Up(); err == nil {
			t.Fatal("up: expected error, got nil")
		}
		if !tryAdvisoryLock(t, sqlDB) {
			t.Fatal("advisory lock leaked after Up error")
		}
		var n int
		if err := sqlDB.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatalf("count schema_migrations: %v", err)
		}
		if n != 1 {
			t.Fatalf("schema_migrations has %d rows after failed up, want 1", n)
		}
	})

	t.Run("down", func(t *testing.T) {
		db := testpg.Start(t)
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("sql.DB: %v", err)
		}
		dir, _ := writeFixture(t, 3)
		m := migrate.New(db, dir, func(f string, a ...any) { t.Logf(f, a...) })
		if _, err := m.Up(); err != nil {
			t.Fatalf("up: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "000003_c.down.sql"), []byte("SELECT * FROM mig_no_such_table_xyz;\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Down(1); err == nil {
			t.Fatal("down: expected error, got nil")
		}
		if !tryAdvisoryLock(t, sqlDB) {
			t.Fatal("advisory lock leaked after Down error")
		}
	})
}

// TestConcurrentUpAppliesOnce (A2): two concurrent Up() runs against one fresh
// Postgres serialize on the package key — exactly one applies all N pending
// migrations, the other observes the completed applied set and applies 0, with
// no error and no duplicate-object failure. The held-key subtest pins the
// deterministic core (both runners visibly blocked on ONE key); the plain-race
// subtest is the literal acceptance shape.
func TestConcurrentUpAppliesOnce(t *testing.T) {
	t.Run("held-key", func(t *testing.T) {
		db := testpg.Start(t)
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("sql.DB: %v", err)
		}
		dir, top := writeFixture(t, 3)
		m := migrate.New(db, dir, func(f string, a ...any) { t.Logf(f, a...) })

		unlock := holdMigrateKey(t, sqlDB)
		results := startPair(t, m.Up)
		// Both runners must be blocked on the single package key (pre-fix this
		// poll times out: neither takes an advisory lock).
		waitForBlockedAdvisory(t, sqlDB, 2, 10*time.Second)
		unlock()
		assertUpPair(t, sqlDB, m, results, top)
	})

	t.Run("plain-race", func(t *testing.T) {
		db := testpg.Start(t)
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("sql.DB: %v", err)
		}
		dir, top := writeFixture(t, 3)
		m := migrate.New(db, dir, func(f string, a ...any) { t.Logf(f, a...) })

		results := startPair(t, m.Up)
		assertUpPair(t, sqlDB, m, results, top)
	})
}

// TestConcurrentDownRollsBackOnce (A3): two concurrent Down(1) runs serialize
// on the same key. The fixture is N=1 so the acceptance assertions are
// simultaneously satisfiable: the winner rolls back version 1 (len==1) and the
// loser observes the empty applied set and rolls back 0, leaving Version() at
// top-1 == 0. (With N>1 a serialized second Down(1) rolls back a DIFFERENT
// migration — len==1 again — so the literal "one applies 1, other applies 0"
// shape requires the single-version fixture.)
func TestConcurrentDownRollsBackOnce(t *testing.T) {
	db := testpg.Start(t)
	dir, _ := writeFixture(t, 1)
	m := migrate.New(db, dir, func(f string, a ...any) { t.Logf(f, a...) })
	if _, err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}

	results := startPair(t, func() ([]uint64, error) { return m.Down(1) })
	var one, zero *runResult
	for i := 0; i < 2; i++ {
		res := <-results
		if res.err != nil {
			t.Fatalf("concurrent down: %v", res.err)
		}
		switch len(res.versions) {
		case 1:
			one = &res
		case 0:
			zero = &res
		default:
			t.Fatalf("concurrent down: runner rolled back %d migrations, want 1 or 0", len(res.versions))
		}
	}
	if one == nil || zero == nil {
		t.Fatalf("concurrent down: want one runner with 1 rollback and one with 0, got one=%v zero=%v", one, zero)
	}
	if v, err := m.Version(); err != nil || v != 0 {
		t.Fatalf("version = %d (err %v), want 0 (top-1)", v, err)
	}
}
