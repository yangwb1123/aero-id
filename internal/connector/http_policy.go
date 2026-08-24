package connector

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

func (c *JSONSummaryConnector) begin(ctx context.Context) (func(), error) {
	c.breakerMu.Lock()
	if time.Now().Before(c.openedUntil) {
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.ObserveSourceCircuit(string(c.source), "open")
		}
		c.breakerMu.Unlock()
		return nil, &RemoteError{Source: c.source, Kind: ErrorUnavailable, Code: "source.circuit_open", Temporary: true}
	}
	c.breakerMu.Unlock()
	token, err := c.bulkhead.AcquireToken(ctx)
	if err != nil {
		return nil, &RemoteError{Source: c.source, Kind: ErrorUnavailable, Code: "source.bulkhead", Temporary: true, Cause: err}
	}
	return token, nil
}

func (c *JSONSummaryConnector) beginObserved(ctx context.Context, operation string, started time.Time) (func(), error) {
	release, err := c.begin(ctx)
	if err != nil {
		c.observeCall(operation, started, err)
		return nil, err
	}
	return release, nil
}

func (c *JSONSummaryConnector) finish(err error) {
	c.breakerMu.Lock()
	defer c.breakerMu.Unlock()
	remote, ok := IsRemote(err)
	if !ok || !remote.Temporary {
		c.consecutiveErr = 0
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.ObserveSourceCircuit(string(c.source), "closed")
		}
		return
	}
	c.consecutiveErr++
	if c.consecutiveErr >= 5 {
		c.openedUntil = time.Now().Add(10 * time.Second)
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.ObserveSourceCircuit(string(c.source), "open")
		}
	}
}

func (c *JSONSummaryConnector) doHTTP(ctx context.Context, factory func() (*http.Request, error)) (*http.Response, error) {
	authRetried := false
	for attempt := 0; attempt < c.cfg.MaxAttempts; attempt++ {
		request, err := factory()
		if err != nil {
			return nil, err
		}
		if err := c.authorize(ctx, request); err != nil {
			return nil, err
		}
		response, err := c.client.Do(request)
		retry, refreshed := c.retryDecision(response, err, retryableRequest(request), attempt, authRetried)
		if refreshed {
			authRetried = true
			attempt-- // token refresh does not consume the transport retry budget
			continue
		}
		if !retry {
			if err != nil {
				return nil, err
			}
			return response, nil
		}
		if err := waitRetry(ctx, c.cfg.RetryBackoff, attempt); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("source request retry budget exhausted")
}

func (c *JSONSummaryConnector) retryDecision(response *http.Response, requestErr error, canRetry bool,
	attempt int, authRetried bool) (retry, refreshed bool) {
	if requestErr != nil {
		return canRetry && attempt+1 < c.cfg.MaxAttempts, false
	}
	if response.StatusCode == http.StatusUnauthorized && c.tokens != nil && !authRetried {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, c.cfg.MaxBodyBytes))
		_ = response.Body.Close()
		c.tokens.invalidate()
		return false, true
	}
	if !canRetry || !retryableStatus(response.StatusCode) || attempt+1 >= c.cfg.MaxAttempts {
		return false, false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, c.cfg.MaxBodyBytes))
	_ = response.Body.Close()
	return true, false
}

func (c *JSONSummaryConnector) authorize(ctx context.Context, request *http.Request) error {
	if c.tokens == nil {
		setBearer(request, c.cfg.BearerToken)
		return nil
	}
	token, err := c.tokens.accessToken(ctx)
	if err != nil {
		return &RemoteError{Source: c.source, Kind: ErrorUnavailable, Code: "source.oauth_token_unavailable", Temporary: true, Cause: err}
	}
	setBearer(request, token)
	return nil
}

func retryableStatus(status int) bool { return status == http.StatusTooManyRequests || status >= 500 }

// retryableRequest keeps automatic retries within the at-least-once contract:
// safe reads may be retried, while command methods must explicitly carry an
// idempotency key before a transport failure or transient response can cause a
// second remote execution.
func retryableRequest(request *http.Request) bool {
	if request == nil {
		return false
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return strings.TrimSpace(request.Header.Get("Idempotency-Key")) != ""
	}
}

func waitRetry(ctx context.Context, base time.Duration, attempt int) error {
	delay := base << attempt
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
