package db

import "fmt"

// DefaultMaxOpenConns and DefaultMaxIdleConns mirror the shipped config. A
// zero pool value therefore uses a bounded code default rather than database/sql's
// unbounded MaxOpenConns behavior.
const (
	DefaultMaxOpenConns = 50
	DefaultMaxIdleConns = 10
)

// normalizeOptions validates and clamps pool options before gorm is opened.
// Negative values are rejected; zero values use bounded defaults; an idle pool
// larger than the open pool is reduced to the open-pool limit. A zero lifetime
// is retained and means that connections have no lifetime limit.
func normalizeOptions(opts Options) (Options, error) {
	if opts.MaxOpenConns < 0 {
		return Options{}, fmt.Errorf("max_open_conns must not be negative")
	}
	if opts.MaxOpenConns == 0 {
		opts.MaxOpenConns = DefaultMaxOpenConns
	}
	if opts.MaxIdleConns < 0 {
		return Options{}, fmt.Errorf("max_idle_conns must not be negative")
	}
	if opts.MaxIdleConns == 0 {
		opts.MaxIdleConns = DefaultMaxIdleConns
	}
	if opts.MaxIdleConns > opts.MaxOpenConns {
		opts.MaxIdleConns = opts.MaxOpenConns
	}
	if opts.ConnMaxLifetime < 0 {
		return Options{}, fmt.Errorf("conn_max_lifetime must not be negative")
	}
	return opts, nil
}
