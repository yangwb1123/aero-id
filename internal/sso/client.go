package sso

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/sso/authzpb"
	"github.com/aero/aero-id/internal/sso/ssopb"
)

// ssoTracerName identifies aero-id's SSO client instrumentation scope.
const ssoTracerName = "github.com/aero/aero-id/internal/sso"

const (
	defaultUserBatchSize = 128
	maxUserBatchSize     = 2048
)

// ErrPartialUsers means the upstream returned fewer users than requested in a
// bounded batch. The caller must not treat the result as a complete directory
// view because absent users are otherwise indistinguishable from a truncated
// response.
var ErrPartialUsers = errors.New("sso: partial user response")

// MetricsSink receives SSO dependency-health observations. *metrics.Registry
// satisfies it; the SSO client treats a nil sink as a no-op so existing
// NewClientFromConn callers (and tests) need not provide one.
type MetricsSink interface {
	// ObserveSSOCache records a cache lookup ("token" or "user") as hit or miss.
	ObserveSSOCache(cache string, hit bool)
	// ObserveSSOCall records an upstream RPC ("validate", "get_users", or "authorize")
	// latency and outcome.
	ObserveSSOCall(op string, dur time.Duration, ok bool)
}

type ClientConfig struct {
	AuthZClientID string
	TokenCacheTTL time.Duration
	UserCacheTTL  time.Duration
	StaleTTL      time.Duration // serve a last-known uid this long past expiry when SSO is down
	Timeout       time.Duration
	// BreakerThreshold is the number of consecutive availability failures that trip
	// the circuit breaker open; 0 disables it. BreakerCooldown is how long the
	// breaker fast-fails before admitting a recovery probe.
	BreakerThreshold int
	BreakerCooldown  time.Duration
	// MaxAttempts bounds the upstream tries per call (1 = no retry). RetryBackoff is
	// the base for exponential backoff between availability-error retries; both
	// fall back to sane defaults when non-positive.
	MaxAttempts  int
	RetryBackoff time.Duration

	// MaxConcurrent bounds concurrent in-flight upstream RPCs (validate, get_users,
	// authorize) through one shared semaphore; 0 or negative disables the cap.
	MaxConcurrent int
	// UserBatchSize bounds each GetUsers RPC. Zero uses the code default.
	UserBatchSize int

	// Keepalive ping cadence; non-positive values fall back to defaults.
	KeepaliveTime    time.Duration
	KeepaliveTimeout time.Duration

	// Metrics, when non-nil, receives SSO cache/latency/error observations. A nil
	// sink disables metrics entirely (used by tests).
	Metrics MetricsSink
}

type Client struct {
	conn          *grpc.ClientConn
	rpc           ssopb.SSOClient
	authz         authzpb.AuthorizerClient
	authzClientID string
	tokenCache    *TokenCache
	userCache     *UserCache
	sf            singleflight.Group // collapses concurrent validations of the same token
	breaker       *breaker           // trips on sustained SSO outage so upstream RPCs fast-fail
	sem           chan struct{}      // bounds concurrent in-flight upstream RPCs (nil = no cap)
	stopFn        func()
	timeout       time.Duration

	maxAttempts   int
	retryBackoff  time.Duration
	userBatchSize int

	metrics MetricsSink // nil-safe: observations are skipped when unset
	tracer  oteltrace.Tracer
}

// recordCache reports a token/user cache lookup outcome when a sink is configured.
func (c *Client) recordCache(cache string, hit bool) {
	if c.metrics != nil {
		c.metrics.ObserveSSOCache(cache, hit)
	}
}

// recordCall reports an upstream SSO RPC latency/outcome when a sink is configured.
func (c *Client) recordCall(op string, start time.Time, err error) {
	if c.metrics != nil {
		c.metrics.ObserveSSOCall(op, time.Since(start), err == nil)
	}
}

// acquireSlot reserves an upstream RPC slot bounded by ctx; it is a no-op when
// MaxConcurrent <= 0. A slot-wait that exceeds the deadline surfaces as
// context.DeadlineExceeded — availability-classified, consistent with the
// circuit-open fast-fail contract — and must not feed the breaker (no RPC was
// attempted). Callers must defer the returned release when err is nil.
func (c *Client) acquireSlot(ctx context.Context) (func(), error) {
	if c.sem == nil {
		return func() {}, nil
	}
	select {
	case c.sem <- struct{}{}:
		return func() { <-c.sem }, nil
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
}

// NewClient dials the SSO endpoint and returns a Client with caches running.
// tlsCfg carries the transport-security knobs (insecure for dev/bufconn, otherwise
// TLS/mTLS); its Insecure mirrors the legacy insecureConn flag callers passed.
func NewClient(ctx context.Context, endpoint string, tlsCfg TLSConfig, cfg ClientConfig) (*Client, error) {
	opts, err := dialOptions(tlsCfg, cfg.KeepaliveTime, cfg.KeepaliveTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial sso: %w", err)
	}
	conn, err := grpc.NewClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial sso: %w", err)
	}
	return NewClientFromConn(conn, cfg), nil
}

// NewClientFromConn lets tests inject a bufconn-backed *grpc.ClientConn.
func NewClientFromConn(conn *grpc.ClientConn, cfg ClientConfig) *Client {
	batchSize := cfg.UserBatchSize
	if batchSize <= 0 {
		batchSize = defaultUserBatchSize
	}
	if batchSize > maxUserBatchSize {
		batchSize = maxUserBatchSize
	}
	c := &Client{
		conn:          conn,
		rpc:           ssopb.NewSSOClient(conn),
		authz:         authzpb.NewAuthorizerClient(conn),
		authzClientID: cfg.AuthZClientID,
		tokenCache:    NewTokenCacheWithStale(cfg.TokenCacheTTL, cfg.StaleTTL),
		breaker:       newBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown),
		timeout:       cfg.Timeout,
		maxAttempts:   cfg.MaxAttempts,
		retryBackoff:  cfg.RetryBackoff,
		userBatchSize: batchSize,
		metrics:       cfg.Metrics,
		tracer:        otel.Tracer(ssoTracerName),
	}
	if c.maxAttempts < 1 {
		c.maxAttempts = 1
	}
	if c.retryBackoff <= 0 {
		c.retryBackoff = 50 * time.Millisecond
	}
	if cfg.MaxConcurrent > 0 {
		c.sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	c.userCache = NewUserCache(cfg.UserCacheTTL, c.fetchUsers)
	if c.metrics != nil {
		c.userCache.SetLookupObserver(func(hit bool) { c.recordCache("user", hit) })
	}
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
		c.recordCache("token", true)
		return uid, nil
	}
	c.recordCache("token", false)
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

func (c *Client) ValidateIdentity(ctx context.Context, token string) (*domain.TokenIdentity, error) {
	uid, err := c.ValidateToken(ctx, token)
	if err != nil {
		return nil, err
	}
	return &domain.TokenIdentity{UID: uid, Subject: uid}, nil
}

// InvalidateToken evicts a cached validation result after a source reports a
// business rejection (for example, revocation). It does not affect an
// in-flight validation's caller result, but its epoch fence prevents that
// stale response from being cached again.
func (c *Client) InvalidateToken(token string) {
	if c != nil && c.tokenCache != nil {
		c.tokenCache.Invalidate(token)
	}
}

// HasPermission delegates global authorization to Snaplink's stable
// Authorizer.Check RPC. The subject is the verified Snaplink subject; this
// method never accepts or stores a user credential.
func (c *Client) HasPermission(ctx context.Context, uid, permission string) (bool, error) {
	if c.authz == nil {
		return false, errors.New("snaplink authorizer is not configured")
	}
	return c.authorizeUpstream(ctx, uid, permission)
}

// authorizeUpstream performs the Authorizer.Check RPC under the caller's
// context, bounded by the configured timeout. It mirrors fetchUsers: the
// circuit breaker gates the call (availability failures trip it, business
// rejections close it), one shared slot bounds concurrent in-flight upstream
// RPCs, and availability errors are retried within the timeout budget. An
// Allowed=false response is a success for the breaker (the RPC completed) and
// is returned to the caller verbatim.
func (c *Client) authorizeUpstream(ctx context.Context, uid, permission string) (bool, error) {
	if !c.breaker.Allow() {
		return false, fmt.Errorf("snaplink authorization check: %w", errCircuitOpen)
	}
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	release, err := c.acquireSlot(ctx)
	if err != nil {
		return false, fmt.Errorf("snaplink authorization check: %w", err)
	}
	defer release()
	ctx, span := c.tracer.Start(ctx, "sso.Authorize", oteltrace.WithSpanKind(oteltrace.SpanKindClient))
	start := time.Now()
	allowed, err := callWithRetry(ctx, c.maxAttempts, c.retryBackoff, func(ctx context.Context) (bool, error) {
		resp, err := c.authz.Check(ctx, &authzpb.CheckRequest{SubjectId: uid, ClientId: c.authzClientID, Permission: permission})
		if err != nil {
			return false, err
		}
		return resp.GetAllowed(), nil
	})
	c.recordCall("authorize", start, err)
	if err != nil {
		span.SetStatus(otelcodes.Error, err.Error())
	}
	span.End()
	if err != nil {
		if isAvailabilityError(err) {
			c.breaker.Record(false)
		} else {
			// A non-availability error (e.g. PermissionDenied) proves SSO is alive.
			c.breaker.Record(true)
		}
		return false, fmt.Errorf("snaplink authorization check: %w", err)
	}
	c.breaker.Record(true)
	return allowed, nil
}

// NOTE: the inline Snaplink AuditWriter delivery path (Client.Publish) was
// deleted — it was dead in the composition root (no call site, no wiring) and
// the durable audit relay is the audit_publications drain (B2-2). The
// auditpb client stub remains available for a future scoped Snaplink audit
// connector; wiring one through the drain is the only sanctioned delivery path.

// callWithRetry runs fn up to maxAttempts times, retrying ONLY on availability
// errors with exponential backoff, all within the caller's (timeout-bounded) ctx.
// Business rejections return immediately. It returns the last outcome so the caller
// records a single breaker outcome for the whole attempt sequence — never one per
// retry — keeping the breaker from tripping far too eagerly. Retries stop early if
// the deadline would not leave room for another backoff + attempt.
func callWithRetry[T any](ctx context.Context, maxAttempts int, backoff time.Duration, fn func(context.Context) (T, error)) (T, error) {
	var resp T
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err = fn(ctx)
		if err == nil || !isAvailabilityError(err) {
			return resp, err
		}
		if attempt == maxAttempts-1 {
			break
		}
		// Exponential backoff for this attempt; bail if it would breach the deadline.
		wait := backoff << attempt
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= wait {
			break
		}
		select {
		case <-ctx.Done():
			return resp, err
		case <-time.After(wait):
		}
	}
	return resp, err
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
	epoch := c.tokenCache.Epoch(token)
	ctx := context.Background()
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	release, err := c.acquireSlot(ctx)
	if err != nil {
		return "", fmt.Errorf("sso validate: %w", err)
	}
	defer release()
	// Client span over the SSO hop so a request trace extends into the dependency.
	ctx, span := c.tracer.Start(ctx, "sso.ValidateToken", oteltrace.WithSpanKind(oteltrace.SpanKindClient))
	start := time.Now()
	resp, err := callWithRetry(ctx, c.maxAttempts, c.retryBackoff, func(ctx context.Context) (*ssopb.ValidateTokenResponse, error) {
		return c.rpc.ValidateToken(ctx, &ssopb.ValidateTokenRequest{Token: token})
	})
	c.recordCall("validate", start, err)
	if err != nil {
		span.SetStatus(otelcodes.Error, err.Error())
	}
	span.End()
	if err != nil {
		if isAvailabilityError(err) {
			// SSO unreachable: record the failure (may trip the breaker) and serve a
			// recently-cached identity within the stale grace window.
			c.breaker.Record(false)
			if uid, ok := c.tokenCache.GetStale(token); ok {
				return uid, nil
			}
		} else {
			// A rejection proves SSO is alive: close the breaker; never serve stale for it.
			c.breaker.Record(true)
		}
		return "", fmt.Errorf("sso validate: %w", err)
	}
	c.breaker.Record(true)
	var exp time.Time
	if resp.ExpiresAtUnix > 0 {
		exp = time.Unix(resp.ExpiresAtUnix, 0)
	}
	c.tokenCache.SetAtEpoch(token, resp.Uid, exp, epoch)
	return resp.Uid, nil
}

// GetUsers returns user info for the given uids, batching uncached entries.
func (c *Client) GetUsers(ctx context.Context, uids []string) (map[string]User, error) {
	return c.userCache.GetMany(ctx, uids)
}

func (c *Client) fetchUsers(ctx context.Context, uids []string) (map[string]User, error) {
	if !c.breaker.Allow() {
		// Breaker open: the GetUsers RPC would fail availability-style, so fast-fail
		// without touching the upstream or consuming a concurrency slot.
		return nil, fmt.Errorf("sso get users: %w", errCircuitOpen)
	}
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	release, err := c.acquireSlot(ctx)
	if err != nil {
		return nil, fmt.Errorf("sso get users: %w", err)
	}
	defer release()
	ctx, span := c.tracer.Start(ctx, "sso.GetUsers", oteltrace.WithSpanKind(oteltrace.SpanKindClient))
	start := time.Now()
	out := make(map[string]User, len(uids))
	for start := 0; start < len(uids); start += c.userBatchSize {
		end := start + c.userBatchSize
		if end > len(uids) {
			end = len(uids)
		}
		batch := uids[start:end]
		resp, callErr := callWithRetry(ctx, c.maxAttempts, c.retryBackoff, func(ctx context.Context) (*ssopb.GetUsersResponse, error) {
			return c.rpc.GetUsers(ctx, &ssopb.GetUsersRequest{Uids: batch})
		})
		if callErr != nil {
			err = callErr
			break
		}
		if resp == nil {
			err = fmt.Errorf("%w: empty response", ErrPartialUsers)
			break
		}
		for _, u := range resp.Users {
			out[u.Uid] = User{UID: u.Uid, Nickname: u.Nickname, Avatar: u.Avatar}
		}
		for _, uid := range batch {
			if _, ok := out[uid]; !ok {
				err = fmt.Errorf("%w: requested uid %q was omitted", ErrPartialUsers, uid)
				break
			}
		}
		if err != nil {
			break
		}
	}
	c.recordCall("get_users", start, err)
	if err != nil {
		span.SetStatus(otelcodes.Error, err.Error())
	}
	span.End()
	if err != nil {
		if isAvailabilityError(err) {
			c.breaker.Record(false)
		} else {
			// A non-availability error (e.g. Unimplemented) proves SSO is reachable.
			c.breaker.Record(true)
		}
		return nil, fmt.Errorf("sso get users: %w", err)
	}
	c.breaker.Record(true)
	return out, nil
}
