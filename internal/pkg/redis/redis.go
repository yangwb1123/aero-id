// Package redis wraps a rueidis-powered Redis client with config, connection
// pooling, health-checking and connection-check so every aero-id subsystem
// (idempotency store, SSO cache, outbox leader election, rate limiter, etc.)
// shares one connection pool with sane defaults.
package redis

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/redis/rueidis"

	"github.com/aero/aero-id/internal/config"
)

// NewClient builds a rueidis.Client from the config. Returns (nil, nil) when
// the endpoint is empty so callers can safely fall back:
//
//	redisClient, _ := redis.NewClient(&cfg.Redis)
//	if redisClient != nil { … }
func NewClient(cfg *config.RedisConfig) (rueidis.Client, error) {
	if cfg == nil || !cfg.Enabled() {
		return nil, nil //nolint:nilnil
	}

	opts := clientOptions(cfg)

	client, err := rueidis.NewClient(opts)
	if err != nil {
		return nil, fmt.Errorf("rueidis new client: %w", err)
	}

	// Connection check: ping the server to catch configuration errors early.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Do(ctx, client.B().Ping().Build()).Error(); err != nil {
		client.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	return client, nil
}

func clientOptions(cfg *config.RedisConfig) rueidis.ClientOption {
	opts := rueidis.ClientOption{InitAddress: []string{cfg.Endpoint}, Password: cfg.Password, SelectDB: cfg.DB,
		Dialer: net.Dialer{Timeout: defaultDuration(cfg.DialTimeout, 5*time.Second), KeepAlive: defaultDuration(cfg.KeepAlive, time.Second)}}
	if cfg.BlockingPoolSize > 0 {
		opts.BlockingPoolSize = cfg.BlockingPoolSize
	}
	if cfg.BlockingPoolMinSize > 0 {
		opts.BlockingPoolMinSize = cfg.BlockingPoolMinSize
	}
	if cfg.BlockingPoolCleanup > 0 {
		opts.BlockingPoolCleanup = cfg.BlockingPoolCleanup
	}
	if cfg.ConnWriteTimeout > 0 {
		opts.ConnWriteTimeout = cfg.ConnWriteTimeout
	}
	if cfg.ConnLifetime > 0 {
		opts.ConnLifetime = cfg.ConnLifetime
	}
	return opts
}

// defaultDuration returns d when d > 0, otherwise fallback.
func defaultDuration(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}
