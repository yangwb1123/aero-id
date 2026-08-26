//go:build integration

package testpg

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	pg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/gorm"

	dbpkg "github.com/aero/aero-id/internal/pkg/db"
)

const postgresFixtureTimeout = time.Minute

// RegisterCleanup closes the SQL pool before terminating the container. The
// closePool callback may be nil while setup is still in progress; this lets
// callers register cleanup immediately after the container starts so setup
// failures do not leak it.
func RegisterCleanup(t *testing.T, terminate func(context.Context) error, closePool func() error) {
	t.Helper()
	t.Cleanup(func() {
		cleanupFixture(t, terminate, closePool)
	})
}

func cleanupFixture(t *testing.T, terminate func(context.Context) error, closePool func() error) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), postgresFixtureTimeout)
	defer cancel()
	if closePool != nil {
		if err := closePool(); err != nil {
			t.Errorf("close PostgreSQL pool: %v", err)
		}
	}
	if err := terminate(cleanupCtx); err != nil {
		t.Errorf("terminate PostgreSQL container: %v", err)
	}
}

// Start launches a throwaway PostgreSQL container and returns a connected *gorm.DB.
// Cleanup is registered with t.Cleanup.
func Start(t *testing.T) *gorm.DB {
	t.Helper()
	startCtx, cancelStart := context.WithTimeout(context.Background(), postgresFixtureTimeout)
	c, err := pg.Run(startCtx,
		"postgres:16-alpine",
		pg.WithDatabase("aero_id_test"),
		pg.WithUsername("aero"),
		pg.WithPassword("aero"),
		pg.BasicWaitStrategies(),
	)
	cancelStart()
	if err != nil {
		t.Fatalf("start pg container: %v", err)
	}
	var db *gorm.DB
	RegisterCleanup(t, c.Terminate, func() error {
		if db == nil {
			return nil
		}
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	})

	connectionCtx, cancelConnection := context.WithTimeout(context.Background(), postgresFixtureTimeout)
	dsn, err := c.ConnectionString(connectionCtx, "sslmode=disable", "TimeZone=UTC")
	cancelConnection()
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}
	db, err = dbpkg.Open(dbpkg.Options{
		DSN:          dsn,
		MaxOpenConns: 5,
		LogLevel:     "silent",
	})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	return db
}

// Migrate executes every *.up.sql file under migrationsDir in lexical order.
// Path is typically returned by RepoRoot helpers; we just take it raw.
func Migrate(t *testing.T, db *gorm.DB, migrationsDir string) {
	t.Helper()
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if err := db.Exec(string(b)).Error; err != nil {
			t.Fatalf("exec %s: %v", f, err)
		}
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
