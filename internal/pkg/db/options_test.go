package db

import (
	"strings"
	"testing"
	"time"
)

type normalizedPoolRecorder struct {
	maxOpen        int
	maxIdle        int
	maxLifetime    time.Duration
	setMaxOpen     bool
	setMaxIdle     bool
	setMaxLifetime bool
}

func (r *normalizedPoolRecorder) SetMaxOpenConns(value int) {
	r.setMaxOpen = true
	r.maxOpen = value
}

func (r *normalizedPoolRecorder) SetMaxIdleConns(value int) {
	r.setMaxIdle = true
	r.maxIdle = value
}

func (r *normalizedPoolRecorder) SetConnMaxLifetime(value time.Duration) {
	r.setMaxLifetime = true
	r.maxLifetime = value
}

func TestNormalizeOptions(t *testing.T) {
	tests := []struct {
		name string
		in   Options
		want Options
		err  string
	}{
		{
			name: "negative max open is rejected",
			in:   Options{MaxOpenConns: -1, MaxIdleConns: 1},
			err:  "max_open_conns must not be negative",
		},
		{
			name: "zero pool values use bounded defaults",
			in:   Options{},
			want: Options{MaxOpenConns: DefaultMaxOpenConns, MaxIdleConns: DefaultMaxIdleConns},
		},
		{
			name: "negative max idle is rejected",
			in:   Options{MaxOpenConns: 10, MaxIdleConns: -1},
			err:  "max_idle_conns must not be negative",
		},
		{
			name: "idle is clamped to open",
			in:   Options{MaxOpenConns: 5, MaxIdleConns: 10},
			want: Options{MaxOpenConns: 5, MaxIdleConns: 5},
		},
		{
			name: "negative lifetime is rejected",
			in:   Options{MaxOpenConns: 10, MaxIdleConns: 2, ConnMaxLifetime: -time.Second},
			err:  "conn_max_lifetime must not be negative",
		},
		{
			name: "zero lifetime means no lifetime limit",
			in:   Options{MaxOpenConns: 10, MaxIdleConns: 2},
			want: Options{MaxOpenConns: 10, MaxIdleConns: 2},
		},
		{
			name: "valid options are unchanged",
			in:   Options{DSN: "host=db", MaxOpenConns: 50, MaxIdleConns: 10, ConnMaxLifetime: time.Hour, LogLevel: "silent"},
			want: Options{DSN: "host=db", MaxOpenConns: 50, MaxIdleConns: 10, ConnMaxLifetime: time.Hour, LogLevel: "silent"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeOptions(tt.in)
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("normalizeOptions error = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeOptions returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeOptions = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestConfigurePoolUsesNormalizedOptions(t *testing.T) {
	opts, err := normalizeOptions(Options{MaxOpenConns: 1, MaxIdleConns: 0})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &normalizedPoolRecorder{}
	configurePool(recorder, opts)
	if !recorder.setMaxOpen || recorder.maxOpen != 1 {
		t.Fatalf("MaxOpenConns: set=%v value=%d, want set=true value=1", recorder.setMaxOpen, recorder.maxOpen)
	}
	if !recorder.setMaxIdle || recorder.maxIdle != 1 {
		t.Fatalf("MaxIdleConns: set=%v value=%d, want set=true value=1", recorder.setMaxIdle, recorder.maxIdle)
	}
	if recorder.setMaxLifetime {
		t.Fatalf("ConnMaxLifetime setter called for zero lifetime: %v", recorder.maxLifetime)
	}
}

func TestOpenRejectsInvalidOptionsBeforeDSNParsing(t *testing.T) {
	const secret = "db-options-test-secret"
	_, err := Open(Options{DSN: "host=invalid password=" + secret, MaxOpenConns: -1})
	if err == nil {
		t.Fatal("expected invalid pool options to fail")
	}
	if want := "db options: max_open_conns must not be negative"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "password=") {
		t.Fatalf("error leaks DSN credentials: %q", err)
	}
}
