package connector

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRetryDelayHonorsAndBoundsRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	response := &http.Response{Header: http.Header{"Retry-After": []string{"30"}}}
	delay := retryDelay(response, time.Second, 0, now)
	if delay < 30*time.Second || delay > 37*time.Second+500*time.Millisecond {
		t.Fatalf("delay = %s, want bounded jitter at or above Retry-After", delay)
	}

	response.Header.Set("Retry-After", "999999999999999999999")
	if got := retryDelay(response, time.Second, 0, now); got != maxRetryDelay {
		t.Fatalf("excessive Retry-After = %s, want cap %s", got, maxRetryDelay)
	}

	response.Header.Set("Retry-After", "not-a-delay")
	if got := retryDelay(response, time.Second, 0, now); got < 500*time.Millisecond || got > 1500*time.Millisecond {
		t.Fatalf("malformed Retry-After delay = %s, want jittered base delay", got)
	}
}

func TestRetryDelayParsesHTTPDateAndPastHint(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	response := &http.Response{Header: http.Header{"Retry-After": []string{"Tue, 25 Aug 2026 00:00:10 GMT"}}}
	if got := retryDelay(response, time.Second, 0, now); got < 10*time.Second || got > 12*time.Second+500*time.Millisecond {
		t.Fatalf("HTTP-date delay = %s, want about 10s", got)
	}
	response.Header.Set("Retry-After", "Tue, 24 Aug 2026 00:00:00 GMT")
	if got := retryDelay(response, time.Second, 0, now); got != 0 {
		t.Fatalf("past HTTP-date delay = %s, want 0", got)
	}
}

func TestWaitRetryHonorsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	response := &http.Response{Header: http.Header{"Retry-After": []string{"60"}}}
	start := time.Now()
	err := waitRetry(ctx, time.Second, 0, response)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitRetry error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waitRetry took %s, deadline was not honored", elapsed)
	}
	if strings.TrimSpace(response.Header.Get("Retry-After")) == "" {
		t.Fatal("test response lost Retry-After header")
	}
}
