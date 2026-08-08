//go:build integration

package repository_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// waitForLockPoll observes a GUARANTEED database lock state — never
// synchronizes on time. Each poll asserts the observed state; the deadline is
// fail-fast only, so a wedged backend fails the test instead of hanging it.
// The existing per-package copies (internal/repository, internal/app) follow
// the same convention.
func waitForLockPoll(t *testing.T, db *sql.DB, lockKind string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	query := lockProbeQuery(lockKind)
	for {
		var held bool
		if err := db.QueryRow(query).Scan(&held); err != nil {
			t.Fatalf("lock probe %s: %v", lockKind, err)
		}
		if held {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock state %q not observed within %s", lockKind, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// lockProbeQuery maps a lock-state name to its probe SQL. Probes exclude the
// probing backend's own locks (pid <> pg_backend_pid()).
func lockProbeQuery(lockKind string) string {
	switch lockKind {
	case "advisory":
		// An ungranted advisory lock request means the B1 trigger ran: the
		// requeue UPDATE executed and the trigger's pg_advisory_xact_lock is
		// blocked on the test session's held lock. No other advisory traffic
		// exists in the fixture (the requeue tx is blocked pre-append).
		return `SELECT EXISTS(
			SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory' AND granted = false AND pid <> pg_backend_pid())`
	case "transactionid":
		// A blocked transactionid wait means a statement is in flight and
		// blocked on a row lock held by another backend.
		return `SELECT EXISTS(
			SELECT 1 FROM pg_locks
			WHERE locktype = 'transactionid' AND granted = false AND pid <> pg_backend_pid())`
	default:
		panic(fmt.Sprintf("unknown lock probe %q", lockKind))
	}
}

// installHoldRequeueTrigger installs an AFTER UPDATE trigger that blocks the
// requeue transaction after its UPDATE (row lock held) and before COMMIT by
// taking pg_advisory_xact_lock on the given key. The fire condition is a SQL
// fact — the trigger fires iff the dead_letter → pending transition ran.
// Fixtures must be seeded BEFORE installation so fixture writes never fire it.
func installHoldRequeueTrigger(t *testing.T, db *sql.DB, triggerName, table, key string) {
	t.Helper()
	statements := []string{
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s_fn() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN PERFORM pg_advisory_xact_lock(hashtext('%s')); RETURN NEW; END $$`, triggerName, key),
		fmt.Sprintf(`CREATE TRIGGER %s AFTER UPDATE ON %s
			FOR EACH ROW WHEN (NEW.status = 'pending' AND OLD.status = 'dead_letter')
			EXECUTE FUNCTION %s_fn()`, triggerName, table, triggerName),
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("install trigger %s: %v", triggerName, err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, triggerName, table))
		_, _ = db.Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS %s_fn()`, triggerName))
	})
}

// holdAdvisoryLock takes a SESSION-level advisory lock on the given key from a
// dedicated connection. The requeue trigger's pg_advisory_xact_lock on the
// same key blocks until the returned release function is called. The
// connection is closed on cleanup so a failed assertion can never wedge the
// test container.
func holdAdvisoryLock(t *testing.T, db *sql.DB, key string) func() {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("advisory lock conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`SELECT pg_advisory_lock(hashtext('%s'))`, key)); err != nil {
		_ = conn.Close()
		t.Fatalf("pg_advisory_lock(%s): %v", key, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return func() {
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`SELECT pg_advisory_unlock(hashtext('%s'))`, key)); err != nil {
			t.Errorf("pg_advisory_unlock(%s): %v", key, err)
		}
	}
}
