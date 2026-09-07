package connector

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aero/aero-id/internal/pkg/bulkhead"
)

type sourceWorkload string

const (
	workloadQuery   sourceWorkload = "query"
	workloadCommand sourceWorkload = "command"
	workloadHealth  sourceWorkload = "health"
)

type sourceWorkloadPolicy struct {
	bulkhead       *bulkhead.Bulkhead
	mu             sync.Mutex
	consecutiveErr int
	openedUntil    time.Time
}

func newSourceWorkloadPolicy(limit int) *sourceWorkloadPolicy {
	return &sourceWorkloadPolicy{bulkhead: bulkhead.New(limit)}
}

type sourceWorkloadLimits struct {
	query   int
	command int
	health  int
}

func workloadLimits(cfg HTTPConfig) sourceWorkloadLimits {
	total := clampedSummaryConcurrency(cfg.MaxConcurrency)
	// Preserve most of the legacy pool for interactive summaries while
	// reserving non-zero command and probe capacity. Explicit class limits are
	// deployment overrides and may intentionally choose a different split.
	query := maxWorkloadLimit(1, total*3/4)
	command := maxWorkloadLimit(1, total/8)
	health := maxWorkloadLimit(1, total-query-command)
	if cfg.QueryMaxConcurrency > 0 {
		query = cfg.QueryMaxConcurrency
	}
	if cfg.CommandMaxConcurrency > 0 {
		command = cfg.CommandMaxConcurrency
	}
	if cfg.HealthMaxConcurrency > 0 {
		health = cfg.HealthMaxConcurrency
	}
	return sourceWorkloadLimits{query: query, command: command, health: health}
}

func maxWorkloadLimit(minimum, value int) int {
	if value < minimum {
		return minimum
	}
	return value
}

func (c *JSONSummaryConnector) policy(workload sourceWorkload) *sourceWorkloadPolicy {
	switch workload {
	case workloadCommand:
		return c.commandPolicy
	case workloadHealth:
		return c.healthPolicy
	default:
		return c.queryPolicy
	}
}

func (c *JSONSummaryConnector) observeCircuit(workload sourceWorkload, state string) {
	if c.cfg.Metrics == nil {
		return
	}
	c.cfg.Metrics.ObserveSourceCircuitWorkload(string(c.source), string(workload), state)
	if workload == workloadQuery {
		// Preserve the pre-partition metric as the interactive query circuit.
		c.cfg.Metrics.ObserveSourceCircuit(string(c.source), state)
	}
}

func (c *JSONSummaryConnector) begin(ctx context.Context, workload sourceWorkload) (func(), error) {
	policy := c.policy(workload)
	policy.mu.Lock()
	if time.Now().Before(policy.openedUntil) {
		c.observeCircuit(workload, "open")
		policy.mu.Unlock()
		return nil, &RemoteError{Source: c.source, Kind: ErrorUnavailable, Code: "source.circuit_open", Temporary: true}
	}
	policy.mu.Unlock()
	token, err := policy.bulkhead.AcquireToken(ctx)
	if err != nil {
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.ObserveSourceBulkhead(string(c.source), string(workload), false)
		}
		kind := ErrorUnavailable
		if requestTimedOut(ctx, err) {
			kind = ErrorTimeout
		}
		return nil, &RemoteError{Source: c.source, Kind: kind, Code: "source.bulkhead", Temporary: true, Cause: redactedRemoteCause(err)}
	}
	if c.cfg.Metrics != nil {
		c.cfg.Metrics.ObserveSourceBulkhead(string(c.source), string(workload), true)
	}
	return token, nil
}

func (c *JSONSummaryConnector) beginObserved(ctx context.Context, workload sourceWorkload, operation string, started time.Time) (func(), error) {
	release, err := c.begin(ctx, workload)
	if err != nil {
		c.observeCall(operation, started, err)
		return nil, err
	}
	return release, nil
}

func (c *JSONSummaryConnector) finish(workload sourceWorkload, err error) {
	policy := c.policy(workload)
	policy.mu.Lock()
	defer policy.mu.Unlock()
	remote, ok := IsRemote(err)
	if !ok || !remote.Temporary {
		policy.consecutiveErr = 0
		c.observeCircuit(workload, "closed")
		return
	}
	policy.consecutiveErr++
	if policy.consecutiveErr >= 5 {
		policy.openedUntil = time.Now().Add(10 * time.Second)
		c.observeCircuit(workload, "open")
	}
}

func (c *JSONSummaryConnector) doHTTP(ctx context.Context, factory func() (*http.Request, error)) (*http.Response, error) {
	authRetried := false
	for attempt := 0; attempt < c.cfg.MaxAttempts; attempt++ {
		request, err := factory()
		if err != nil {
			return nil, err
		}
		snapshot, err := c.authorize(ctx, request)
		if err != nil {
			return nil, err
		}
		response, err := c.client.Do(request)
		retry, refreshed := c.retryDecision(response, err, retryableRequest(request), attempt, authRetried, snapshot)
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
		if err := waitRetry(ctx, c.cfg.RetryBackoff, attempt, response); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("source request retry budget exhausted")
}

func (c *JSONSummaryConnector) retryDecision(response *http.Response, requestErr error, canRetry bool,
	attempt int, authRetried bool, snapshot oauthTokenSnapshot) (retry, refreshed bool) {
	if requestErr != nil {
		return canRetry && attempt+1 < c.cfg.MaxAttempts, false
	}
	if response.StatusCode == http.StatusUnauthorized && c.tokens != nil && !authRetried {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, c.cfg.MaxBodyBytes))
		_ = response.Body.Close()
		c.tokens.invalidateIf(snapshot)
		return false, true
	}
	if !canRetry || !retryableStatus(response.StatusCode) || attempt+1 >= c.cfg.MaxAttempts {
		return false, false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, c.cfg.MaxBodyBytes))
	_ = response.Body.Close()
	return true, false
}

func (c *JSONSummaryConnector) authorize(ctx context.Context, request *http.Request) (oauthTokenSnapshot, error) {
	if c.tokens == nil {
		setBearer(request, c.cfg.BearerToken)
		return oauthTokenSnapshot{value: c.cfg.BearerToken}, nil
	}
	snapshot, err := c.tokens.accessTokenSnapshot(ctx)
	if err != nil {
		kind := ErrorUnavailable
		if requestTimedOut(ctx, err) {
			kind = ErrorTimeout
		}
		return oauthTokenSnapshot{}, &RemoteError{Source: c.source, Kind: kind,
			Code: "source.oauth_token_unavailable", Temporary: true, Cause: redactedRemoteCause(err)}
	}
	setBearer(request, snapshot.value)
	return snapshot, nil
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

const maxRetryDelay = 5 * time.Minute

func waitRetry(ctx context.Context, base time.Duration, attempt int, response *http.Response) error {
	if ctx == nil {
		ctx = context.Background()
	}
	delay := retryDelay(response, base, attempt, time.Now().UTC())
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ctx.Err()
		}
		if delay >= remaining {
			if err := ctx.Err(); err != nil {
				return err
			}
			return context.DeadlineExceeded
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryDelay(response *http.Response, base time.Duration, attempt int, now time.Time) time.Duration {
	if base <= 0 {
		base = 50 * time.Millisecond
	}
	delay := base
	for i := 0; i < attempt && delay < maxRetryDelay; i++ {
		if delay > maxRetryDelay/2 {
			delay = maxRetryDelay
			break
		}
		delay *= 2
	}
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	if hint, ok := retryAfterDelay(response, now); ok {
		if hint > maxRetryDelay {
			hint = maxRetryDelay
		}
		// Retry-After is a lower bound from the source. Add only positive,
		// bounded jitter so a fleet does not retry on the same instant.
		jitter := hint / 4
		if jitter > 0 {
			hint += randomRetryJitter(jitter)
		}
		if hint > maxRetryDelay {
			return maxRetryDelay
		}
		return hint
	}
	return jitterRetryDelay(delay)
}

func retryAfterDelay(response *http.Response, now time.Time) (time.Duration, bool) {
	if response == nil {
		return 0, false
	}
	raw := strings.TrimSpace(response.Header.Get("Retry-After"))
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
		if seconds >= int64(maxRetryDelay/time.Second) {
			return maxRetryDelay, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if retryAfterNumeric(raw) {
		return maxRetryDelay, true
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	if when.Before(now) {
		return 0, true
	}
	return when.Sub(now), true
}

func retryAfterNumeric(value string) bool {
	if value == "" {
		return false
	}
	digits := 0
	for i, char := range value {
		if i == 0 && char == '+' {
			continue
		}
		if char < '0' || char > '9' {
			return false
		}
		digits++
	}
	return digits > 0
}

func jitterRetryDelay(delay time.Duration) time.Duration {
	if delay <= 1 {
		return delay
	}
	span := delay / 2
	return delay - span + randomRetryJitter(span*2)
}

func randomRetryJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(max)+1))
	if err != nil {
		return max / 2
	}
	return time.Duration(value.Int64())
}
