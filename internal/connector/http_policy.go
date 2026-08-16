package connector

import (
	"context"
	"errors"
	"io"
	"net/http"
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
	for attempt := 0; attempt < c.cfg.MaxAttempts; attempt++ {
		request, err := factory()
		if err != nil {
			return nil, err
		}
		response, err := c.client.Do(request)
		if err == nil {
			if !retryableStatus(response.StatusCode) || attempt+1 >= c.cfg.MaxAttempts {
				return response, nil
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, c.cfg.MaxBodyBytes))
			_ = response.Body.Close()
		} else if attempt+1 >= c.cfg.MaxAttempts {
			return nil, err
		}
		if err := waitRetry(ctx, c.cfg.RetryBackoff, attempt); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("source request retry budget exhausted")
}

func retryableStatus(status int) bool { return status == http.StatusTooManyRequests || status >= 500 }

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
