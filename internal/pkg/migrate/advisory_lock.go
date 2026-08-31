package migrate

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// advisoryLockKey is the single, namespaced advisory-lock key serializing all
// Up/Down runs against one database. It is pinned by TestAdvisoryLockKeyStable:
// revaluing it during a rolling deployment would split contention across two
// keys and re-open the race, so the value is load-bearing. The "aero-id:"
// prefix avoids collision with existing advisory keys ('coverage-gate-writer',
// 'p-2', tenant/partition hashtexts).
const advisoryLockKey = "aero-id:migrate"

// advisoryLockQuery builds the session-level acquire/release SQL from key so
// Up and Down provably contend on the package constant (never a divergent
// hardcoded string). Session-level by construction: pg_advisory_lock (not
// pg_advisory_xact_lock) survives transaction boundaries, which is required
// because Up/Down span one transaction per migration.
func advisoryLockQuery(key string) (acquire, release string) {
	return "SELECT pg_advisory_lock(hashtext('" + key + "'))",
		"SELECT pg_advisory_unlock(hashtext('" + key + "'))"
}

// withSessionLock runs fn while holding the package advisory lock on a
// dedicated *sql.Conn obtained from the GORM pool. The lock must live on a
// dedicated connection, never via m.db.Exec on a pooled connection: a session
// lock taken on a pooled connection that GORM returns to the pool survives and
// leaks (documented hazard, internal/app/audit_integration_test.go:110-111).
// Migration work is also bound to that connection. Besides making the lock and
// migration transaction unambiguously belong to one session, this avoids a
// pool self-deadlock when MaxOpenConns is one: holding the lock connection must
// not require a second pooled connection to run the callback.
// The dedicated connection is closed on every path — including error paths —
// and closing it releases the session lock, which makes release-on-error real.
func (m *Migrator) withSessionLock(ctx context.Context, fn func(*gorm.DB) error) error {
	sqlDB, err := m.db.DB()
	if err != nil {
		return fmt.Errorf("migrate lock: sql handle: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate lock: acquire conn: %w", err)
	}
	// Close on every path, before the lock is even taken: a failed acquire
	// must not strand the connection. Closing also releases the session lock.
	defer func() { _ = conn.Close() }()

	acquire, release := advisoryLockQuery(advisoryLockKey)
	if _, err := conn.ExecContext(ctx, acquire); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	// Best-effort release: unlock failure is logged via m.logf and never masks
	// or changes the migration result; the deferred Close above releases the
	// session lock regardless.
	defer func() {
		if _, err := conn.ExecContext(context.Background(), release); err != nil {
			m.logf("release migration lock: %v", err)
		}
	}()

	// Supplying Context forces GORM to clone the statement. Without that clone,
	// assigning ConnPool would mutate m.db's shared statement and leave the
	// caller's pool pointing at this connection after it is closed.
	lockedDB := m.db.Session(&gorm.Session{NewDB: true, Context: ctx})
	lockedDB.Statement.ConnPool = conn
	return fn(lockedDB)
}
