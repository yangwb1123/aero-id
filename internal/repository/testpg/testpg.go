//go:build integration

package testpg

import (
	"context"
	"testing"

	pg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/gorm"

	dbpkg "github.com/aero/aero-id/internal/pkg/db"
)

// Start launches a throwaway PostgreSQL container and returns a connected *gorm.DB.
// Cleanup is registered with t.Cleanup.
func Start(t *testing.T) *gorm.DB {
	t.Helper()
	ctx := context.Background()
	c, err := pg.Run(ctx,
		"postgres:16-alpine",
		pg.WithDatabase("aero_id_test"),
		pg.WithUsername("aero"),
		pg.WithPassword("aero"),
		pg.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start pg container: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	dsn, err := c.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}
	db, err := dbpkg.Open(dbpkg.Options{
		DSN:          dsn,
		MaxOpenConns: 5,
		LogLevel:     "silent",
	})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	return db
}
