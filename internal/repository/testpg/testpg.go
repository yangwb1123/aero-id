//go:build integration

package testpg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	dbpkg "github.com/aero/aero-id/internal/pkg/db"
	"github.com/aero/aero-id/internal/pkg/migrate"
)

// Start launches a throwaway PostgreSQL container and returns a connected *gorm.DB.
// Cleanup is registered with t.Cleanup.
func Start(t *testing.T) *gorm.DB {
	t.Helper()
	startCtx, cancelStart := context.WithTimeout(context.Background(), postgresFixtureTimeout)
	var db *gorm.DB
	c, err := StartContainer(t, startCtx, func() error {
		if db == nil {
			return nil
		}
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	})
	cancelStart()
	if err != nil {
		t.Fatalf("start pg container: %v", err)
	}

	connectionStarted := time.Now()
	connectionCtx, cancelConnection := context.WithTimeout(context.Background(), postgresFixtureTimeout)
	dsn, err := c.ConnectionString(connectionCtx, "sslmode=disable", "TimeZone=UTC")
	cancelConnection()
	logFixturePhase(t, "connection_string", connectionStarted, err)
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}
	openStarted := time.Now()
	db, err = dbpkg.Open(dbpkg.Options{
		DSN:          dsn,
		MaxOpenConns: 5,
		LogLevel:     "silent",
	})
	logFixturePhase(t, "open_pool", openStarted, err)
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	return db
}

// Migrate applies the migration set using the same runner as production. This
// keeps integration databases subject to the canonical discovery, bookkeeping,
// transaction, and advisory-lock semantics.
func Migrate(t *testing.T, db *gorm.DB, migrationsDir string) {
	t.Helper()
	started := time.Now()
	_, err := migrate.New(db, migrationsDir, nil).Up()
	logFixturePhase(t, "migrate", started, err)
	if err != nil {
		t.Fatalf("migrate up: %v", err)
	}
}

// RepoRoot finds the module root by walking up from the calling test file
// looking for a go.mod.
func RepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := wd; d != "/"; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	t.Fatal("go.mod not found")
	return ""
}
