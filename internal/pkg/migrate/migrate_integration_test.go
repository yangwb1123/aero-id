//go:build integration

package migrate_test

import (
	"path/filepath"
	"testing"

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
