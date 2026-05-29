package sso

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aero/aero-id/internal/sso/ssopb"
)

type ClientConfig struct {
	TokenCacheTTL time.Duration
	UserCacheTTL  time.Duration
	StaleTTL      time.Duration // serve a last-known uid this long past expiry when SSO is down
	Timeout       time.Duration
	// BreakerThreshold is the number of consecutive availability failures that trip
	// the circuit breaker open; 0 disables it. BreakerCooldown is how long the
	// breaker fast-fails before admitting a recovery probe.
	BreakerThreshold int
	BreakerCooldown  time.Duration
}

type Client struct {
	conn       *grpc.ClientConn
	rpc        ssopb.SSOClient
	tokenCache *TokenCache
	userCache  *UserCache
	sf         singleflight.Group // collapses concurrent validations of the same token
	breaker    *breaker           // trips on sustained SSO outage so new tokens fast-fail
	stopFn     func()
	timeout    time.Duration
}

// NewClient dials the SSO endpoint and returns a Client with caches running.
func NewClient(ctx context.Context, endpoint string, insecureConn bool, cfg ClientConfig) (*Client, error) {
	var opts []grpc.DialOption
	if insecureConn {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	conn, err := grpc.NewClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial sso: %w", err)
	}
	return NewClientFromConn(conn, cfg), nil
}

// NewClientFromConn lets tests inject a bufconn-backed *grpc.ClientConn.
func NewClientFromConn(conn *grpc.ClientConn, cfg ClientConfig) *Client {
	c := &Client{
		conn:       conn,
		rpc:        ssopb.NewSSOClient(conn),
		tokenCache: NewTokenCacheWithStale(cfg.TokenCacheTTL, cfg.StaleTTL),
		breaker:    newBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown),
		timeout:    cfg.Timeout,
	}
	c.userCache = NewUserCache(cfg.UserCacheTTL, c.fetchUsers)
	stop := c.tokenCache.RunCleaner(30 * time.Second)
	c.stopFn = stop
	return c
}

func (c *Client) Close() {
	if c.stopFn != nil {
		c.stopFn()
		c.stopFn = nil
	}
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// HealthCheck reports whether the SSO connection is usable, for readiness probes.
// It nudges a lazily-dialed connection out of Idle and treats a closed or
// hard-failed connection as unhealthy; Idle/Connecting/Ready are tolerated so a
// momentary reconnect does not flap readiness. It issues no RPC.
func (c *Client) HealthCheck(_ context.Context) error {
	if c.conn == nil {
		return errors.New("sso client closed")
	}
	c.conn.Connect()
	switch s := c.conn.GetState(); s {
	case connectivity.Shutdown, connectivity.TransientFailure:
		return fmt.Errorf("sso connection state %s", s)
	default:
		return nil
	}
}

// ValidateToken returns the uid for a Bearer token, consulting the cache first.
// Concurrent validations of the same uncached token are collapsed into a single
// upstream call via singleflight, so a cache-miss stampede cannot fan out to SSO.
func (c *Client) ValidateToken(ctx context.Context, token string) (string, error) {
	if uid, ok := c.tokenCache.Get(token); ok {
		return uid, nil
	}
	ch := c.sf.DoChan(token, func() (interface{}, error) {
		// A concurrent leader may have populated the cache while we queued.
		if uid, ok := c.tokenCache.Get(token); ok {
			return uid, nil
		}
		return c.validateUpstream(token)
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return "", res.Err
		}
		return res.Val.(string), nil
	}
}

// validateUpstream performs the actual SSO ValidateToken RPC under the configured
// timeout. It is only ever invoked by the singleflight leader; because the result is
// shared with all waiters it uses a detached, timeout-bounded context rather than any
// single caller's.
//
// A circuit breaker guards the RPC: after sustained SSO outages it skips the call
// entirely and serves a stale identity (or fast-fails), so new uncached tokens no
// longer each wait out a full timeout. Only availability failures (SSO unreachable
// or too slow) trip the breaker and trigger stale fallback; a business rejection
// (e.g. an invalid token) proves SSO is alive, so it neither trips the breaker nor
// gets served from stale cache.
func (c *Client) validateUpstream(token string) (string, error) {
	if !c.breaker.Allow() {
		// Breaker open: skip the doomed RPC and serve a last-known identity if we have one.
		if uid, ok := c.tokenCache.GetStale(token); ok {
			return uid, nil
		}
		return "", fmt.Errorf("sso validate: %w", errCircuitOpen)
	}
	ctx := context.Background()
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	resp, err := c.rpc.ValidateToken(ctx, &ssopb.ValidateTokenRequest{Token: token})
	if err != nil {
		if isAvailabilityError(err) {
			// SSO is unreachable: record the failure (may trip the breaker) and fall back
			// to a recently-cached identity within the stale grace window so a transient
			// outage doesn't reject every authenticated request.
			c.breaker.Record(false)
			if uid, ok := c.tokenCache.GetStale(token); ok {
				return uid, nil
			}
		} else {
			// A response that rejects the token: SSO is healthy, so close the breaker and
			// do NOT serve a stale identity for a token the upstream explicitly refused.
			c.breaker.Record(true)
		}
		return "", fmt.Errorf("sso validate: %w", err)
	}
	c.breaker.Record(true)
	var exp time.Time
	if resp.ExpiresAtUnix > 0 {
		exp = time.Unix(resp.ExpiresAtUnix, 0)
	}
	c.tokenCache.Set(token, resp.Uid, exp)
	return resp.Uid, nil
}

// GetUsers returns user info for the given uids, batching uncached entries.
func (c *Client) GetUsers(ctx context.Context, uids []string) (map[string]User, error) {
	return c.userCache.GetMany(ctx, uids)
}

func (c *Client) fetchUsers(ctx context.Context, uids []string) (map[string]User, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	resp, err := c.rpc.GetUsers(ctx, &ssopb.GetUsersRequest{Uids: uids})
	if err != nil {
		return nil, fmt.Errorf("sso get users: %w", err)
	}
	out := make(map[string]User, len(resp.Users))
	for _, u := range resp.Users {
		out[u.Uid] = User{UID: u.Uid, Nickname: u.Nickname, Avatar: u.Avatar}
	}
	return out, nil
}
