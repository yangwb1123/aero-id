//go:build integration

package db_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/config"
	dbpkg "github.com/aero/aero-id/internal/pkg/db"
)

const (
	firstQueryConnectTimeout = 2 * time.Second
	firstQueryMaxElapsed     = 30 * time.Second
	testDatabasePassword     = "db-startup-hardening-test-secret"
)

func TestBoundedFirstQueryFailure(t *testing.T) {
	tests := []struct {
		name string
		host string
		port int
	}{
		{name: "connection refused", host: "127.0.0.1", port: 1},
		{name: "configurable SYN drop", host: synDropHost(), port: 5432},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBoundedFirstQueryFailure(t, tt.host, tt.port)
		})
	}
}

func synDropHost() string {
	if host := strings.TrimSpace(os.Getenv("AERO_ID_TEST_SYN_DROP_HOST")); host != "" {
		return host
	}
	return "192.0.2.1"
}

func assertBoundedFirstQueryFailure(t *testing.T, host string, port int) {
	t.Helper()
	dsn := config.DatabaseConfig{
		Host:           host,
		Port:           port,
		User:           "aero",
		Password:       testDatabasePassword,
		DBName:         "aero_id",
		SSLMode:        "disable",
		ConnectTimeout: firstQueryConnectTimeout,
	}.DSN()
	gdb, err := dbpkg.Open(dbpkg.Options{DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1, LogLevel: "silent"})
	if err != nil {
		assertNoPasswordLeak(t, err)
		t.Fatalf("open lazy database: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("get sql database: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

	started := time.Now()
	closeTimer := time.AfterFunc(firstQueryMaxElapsed, func() { _ = sqlDB.Close() })
	err = gdb.Exec("SELECT 1").Error
	closeTimer.Stop()
	elapsed := time.Since(started)
	if err == nil {
		t.Skipf("endpoint %s:%d unexpectedly accepted a query; cannot use it as an unreachable test endpoint", host, port)
	}
	assertNoPasswordLeak(t, err)
	if elapsed >= firstQueryMaxElapsed {
		t.Fatalf("first query took %v, want failure within %v: %v", elapsed, firstQueryMaxElapsed, err)
	}
}

func assertNoPasswordLeak(t *testing.T, err error) {
	t.Helper()
	message := err.Error()
	if strings.Contains(message, testDatabasePassword) || strings.Contains(strings.ToLower(message), "password=") {
		t.Fatalf("database error leaks credentials: %q", message)
	}
}
