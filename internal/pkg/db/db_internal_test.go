package db

// Deterministic unit tests for the connection foundation package (no Docker,
// no network, no timing). The only test that touches Open uses a DSN that
// fails deterministically at pgx.ParseConfig ("sslmode=bogus") before any
// network I/O; a parse-valid DSN would make the test network-dependent and is
// deliberately never used here.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm/logger"
)

// TestLogLevel pins the logLevel mapping: "silent"|"error"|"info" map to the
// matching gorm logger constants; every other value (including "", "warn",
// case variants, and unknown strings) falls through to logger.Warn because the
// switch in db.go has no explicit case for them. Exact equality against the
// constants keeps the assertions independent of their numeric values.
func TestLogLevel(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  logger.LogLevel
	}{
		{"silent maps to Silent", "silent", logger.Silent},
		{"error maps to Error", "error", logger.Error},
		{"info maps to Info", "info", logger.Info},
		{"empty defaults to Warn", "", logger.Warn},
		{"warn defaults to Warn (no explicit case)", "warn", logger.Warn},
		{"uppercase WARN defaults to Warn", "WARN", logger.Warn},
		{"uppercase ERROR defaults to Warn", "ERROR", logger.Warn},
		{"unknown value defaults to Warn", "bogus", logger.Warn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logLevel(tt.input); got != tt.want {
				t.Fatalf("logLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// poolRecorder is a deterministic recording fake for the anonymous interface
// configurePool accepts (SetMaxOpenConns/SetMaxIdleConns/SetConnMaxLifetime).
// Boolean flags distinguish "setter never called" from "setter called with 0",
// which the int-valued setters alone cannot express.
type poolRecorder struct {
	setMaxOpen     bool
	maxOpen        int
	setMaxIdle     bool
	maxIdle        int
	setMaxLifetime bool
	maxLifetime    time.Duration
}

func (r *poolRecorder) SetMaxOpenConns(v int)              { r.setMaxOpen = true; r.maxOpen = v }
func (r *poolRecorder) SetMaxIdleConns(v int)              { r.setMaxIdle = true; r.maxIdle = v }
func (r *poolRecorder) SetConnMaxLifetime(v time.Duration) { r.setMaxLifetime = true; r.maxLifetime = v }

// TestConfigurePool pins the > 0-only semantics: each pool setting is applied
// exactly when its Options value is strictly positive; zero and negative
// values are skipped and leave the corresponding flag unset.
func TestConfigurePool(t *testing.T) {
	tests := []struct {
		name            string
		maxOpen         int
		maxIdle         int
		maxLifetime     time.Duration
		wantOpenSet     bool
		wantOpen        int
		wantIdleSet     bool
		wantIdle        int
		wantLifetimeSet bool
		wantLifetime    time.Duration
	}{
		{"all positive values applied", 10, 5, time.Minute, true, 10, true, 5, true, time.Minute},
		{"all zero values skipped", 0, 0, 0, false, 0, false, 0, false, 0},
		{"all negative values skipped", -1, -5, -time.Second, false, 0, false, 0, false, 0},
		{"only MaxIdleConns applied", 0, 3, 0, false, 0, true, 3, false, 0},
		{"MaxOpenConns and MaxLifetime applied", 7, 0, 250 * time.Millisecond, true, 7, false, 0, true, 250 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &poolRecorder{}
			configurePool(r, Options{
				MaxOpenConns:    tt.maxOpen,
				MaxIdleConns:    tt.maxIdle,
				ConnMaxLifetime: tt.maxLifetime,
			})
			if r.setMaxOpen != tt.wantOpenSet || r.maxOpen != tt.wantOpen {
				t.Fatalf("MaxOpenConns: set=%v value=%d, want set=%v value=%d", r.setMaxOpen, r.maxOpen, tt.wantOpenSet, tt.wantOpen)
			}
			if r.setMaxIdle != tt.wantIdleSet || r.maxIdle != tt.wantIdle {
				t.Fatalf("MaxIdleConns: set=%v value=%d, want set=%v value=%d", r.setMaxIdle, r.maxIdle, tt.wantIdleSet, tt.wantIdle)
			}
			if r.setMaxLifetime != tt.wantLifetimeSet || r.maxLifetime != tt.wantLifetime {
				t.Fatalf("ConnMaxLifetime: set=%v value=%v, want set=%v value=%v", r.setMaxLifetime, r.maxLifetime, tt.wantLifetimeSet, tt.wantLifetime)
			}
		})
	}
}

// TestOpenMalformedDSN pins db.Open's error contract for DSN-parse failures.
// "sslmode=bogus" fails deterministically inside pgx.ParseConfig before any
// network I/O, and gorm skips its automatic ping when Initialize fails, so the
// test is fully offline. The primary assertion checks db.Open's own "gorm open:"
// wrap; the secondary errors.As pin confirms the underlying parse error type
// survives db.Open's %w (gorm logs the pgx error via %v but returns it raw).
// The gorm default logger prints "[error] failed to initialize database ..."
// to stderr during this test — expected noise, not a failure.
func TestOpenMalformedDSN(t *testing.T) {
	_, err := Open(Options{DSN: "host=localhost sslmode=bogus"})
	if err == nil {
		t.Fatal("expected error for malformed DSN")
	}
	if !strings.Contains(err.Error(), "gorm open:") {
		t.Fatalf("error %q missing db.Open wrap prefix %q", err.Error(), "gorm open:")
	}
	var parseErr *pgconn.ParseConfigError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error %q does not wrap a *pgconn.ParseConfigError", err.Error())
	}
}
