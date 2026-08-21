package sso

import (
	"context"
	"fmt"
	"time"

	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/aero/aero-id/internal/sso/ssopb"
)

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
	ctx, span := c.tracer.Start(ctx, "sso.GetUsers", trace.WithSpanKind(trace.SpanKindClient))
	started := time.Now()
	out, err := c.fetchUserBatches(ctx, uids)
	c.recordCall("get_users", started, err)
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

func (c *Client) fetchUserBatches(ctx context.Context, uids []string) (map[string]User, error) {
	out := make(map[string]User, len(uids))
	for offset := 0; offset < len(uids); offset += c.userBatchSize {
		end := offset + c.userBatchSize
		if end > len(uids) {
			end = len(uids)
		}
		batch := uids[offset:end]
		resp, err := c.fetchUserBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		if err := mergeUserBatch(out, batch, resp); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *Client) fetchUserBatch(ctx context.Context, batch []string) (*ssopb.GetUsersResponse, error) {
	return callWithRetry(ctx, c.maxAttempts, c.retryBackoff, func(ctx context.Context) (*ssopb.GetUsersResponse, error) {
		return c.rpc.GetUsers(ctx, &ssopb.GetUsersRequest{Uids: batch})
	})
}

func mergeUserBatch(out map[string]User, requested []string, resp *ssopb.GetUsersResponse) error {
	if resp == nil {
		return fmt.Errorf("%w: empty response", ErrPartialUsers)
	}
	for _, u := range resp.Users {
		out[u.Uid] = User{UID: u.Uid, Nickname: u.Nickname, Avatar: u.Avatar}
	}
	for _, uid := range requested {
		if _, ok := out[uid]; !ok {
			return fmt.Errorf("%w: requested uid %q was omitted", ErrPartialUsers, uid)
		}
	}
	return nil
}
