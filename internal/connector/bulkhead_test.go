package connector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

// Bulkhead regression suite (2026-08-16 canonicalize-bounded-concurrency):
// the connector's private semaphore was retired in favor of
// internal/pkg/bulkhead.AcquireToken. These tests pin the behavior that must
// survive the swap: limit-1 serialization, cancelled-acquire error mapping,
// and the <=0 -> 32 default clamp.

// TestConnector_SummarySerializesAtMaxConcurrencyOne: at MaxConcurrency 1 the
// connector runs on a limit-1 bulkhead, so concurrent Summary calls must
// serialize — max in-flight on the source is exactly 1 and both calls still
// complete successfully.
func TestConnector_SummarySerializesAtMaxConcurrencyOne(t *testing.T) {
	var mu sync.Mutex
	var inFlight, maxInFlight int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		_, _ = w.Write([]byte(`{"source_region":"west","version":1,"datasets":{}}`))
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{MaxConcurrency: 1, MaxAttempts: 1})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < len(errs); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if maxInFlight != 1 {
		t.Errorf("max in-flight = %d, want 1 (limit-1 bulkhead must serialize)", maxInFlight)
	}
}

// TestConnector_CancelledAcquireMapsToSourceBulkhead: with the single slot
// held and a pre-cancelled context, the second call fails in the bulkhead
// with the identical error contract: ErrorUnavailable / "source.bulkhead" /
// Temporary, cause wrapped to the context error.
func TestConnector_CancelledAcquireMapsToSourceBulkhead(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(firstStarted)
		<-releaseFirst
		_, _ = w.Write([]byte(`{"source_region":"west","version":1,"datasets":{}}`))
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{MaxConcurrency: 1, MaxAttempts: 1})

	firstDone := make(chan error, 1)
	go func() {
		_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}})
		firstDone <- err
	}()
	<-firstStarted // first call holds the only slot

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.Summary(ctx, SummaryRequest{AccountID: domain.ID{1}})
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("err = %v, want RemoteError", err)
	}
	if remote.Kind != ErrorUnavailable || remote.Code != "source.bulkhead" || !remote.Temporary {
		t.Errorf("remote = %+v, want unavailable/source.bulkhead/temporary", remote)
	}
	if !errors.Is(remote.Cause, context.Canceled) {
		t.Errorf("cause = %v, want context.Canceled", remote.Cause)
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Errorf("first call: %v", err)
	}
}

// TestConnector_DefaultConcurrencyClamp: the <=0 -> 32 default survives the
// migration. The clamp is applied before bulkhead.New so the module's
// New(0) -> serial contract can never silently serialize the connector.
func TestConnector_DefaultConcurrencyClamp(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 32},
		{-5, 32},
		{1, 1},
		{64, 64},
		{128, 128}, // no cap — pass-through above 0
	}
	for _, tc := range cases {
		if got := clampedSummaryConcurrency(tc.in); got != tc.want {
			t.Errorf("clampedSummaryConcurrency(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestConnector_DefaultConcurrencyAllowsParallel: behavioral cross-check of
// the clamp — with MaxConcurrency 0 (the default), concurrent Summary calls
// run in parallel instead of falling into the bulkhead module's serial
// New(0) contract.
func TestConnector_DefaultConcurrencyAllowsParallel(t *testing.T) {
	var entered int32
	enter := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&entered, 1) == 2 {
			close(enter)
		}
		<-release
		_, _ = w.Write([]byte(`{"source_region":"west","version":1,"datasets":{}}`))
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{MaxConcurrency: 0, MaxAttempts: 1})

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := 0; i < len(errs); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}})
		}(i)
	}
	select {
	case <-enter:
	case <-time.After(2 * time.Second):
		t.Fatal("both calls never entered the source concurrently — default clamp did not apply")
	}
	close(release)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}
}
