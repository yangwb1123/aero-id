package connector

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/metrics"
)

func TestWorkloadLimitsPartitionLegacyPool(t *testing.T) {
	limits := workloadLimits(HTTPConfig{MaxConcurrency: 32})
	if limits.query != 24 || limits.command != 4 || limits.health != 4 {
		t.Fatalf("limits = %+v, want query=24 command=4 health=4", limits)
	}
	limits = workloadLimits(HTTPConfig{MaxConcurrency: 1})
	if limits.query != 1 || limits.command != 1 || limits.health != 1 {
		t.Fatalf("minimum limits = %+v, want every class reserved at 1", limits)
	}
	limits = workloadLimits(HTTPConfig{MaxConcurrency: 32, QueryMaxConcurrency: 2,
		CommandMaxConcurrency: 3, HealthMaxConcurrency: 5})
	if limits.query != 2 || limits.command != 3 || limits.health != 5 {
		t.Fatalf("explicit limits = %+v, want 2/3/5", limits)
	}
}

func TestConnector_HealthDoesNotConsumeSummaryCapacity(t *testing.T) {
	healthStarted := make(chan struct{})
	releaseHealth := make(chan struct{})
	var healthOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			healthOnce.Do(func() { close(healthStarted) })
			<-releaseHealth
			return
		}
		writeWorkloadSummary(w)
	}))
	defer server.Close()

	client := newTestConnector(t, server.URL, HTTPConfig{MaxConcurrency: 1,
		QueryMaxConcurrency: 1, HealthMaxConcurrency: 1, MaxAttempts: 1})
	healthDone := make(chan error, 1)
	go func() { healthDone <- client.Health(t.Context()) }()
	<-healthStarted

	summaryDone := make(chan error, 1)
	go func() {
		_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1},
			Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
		summaryDone <- err
	}()
	select {
	case err := <-summaryDone:
		if err != nil {
			t.Fatalf("summary while health is blocked: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("summary was starved by a blocked health probe")
	}
	close(releaseHealth)
	if err := <-healthDone; err != nil {
		t.Fatalf("health: %v", err)
	}
}

func TestConnector_CommandDoesNotConsumeSummaryCapacity(t *testing.T) {
	eraseStarted := make(chan struct{})
	releaseErase := make(chan struct{})
	var eraseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/erase" {
			eraseOnce.Do(func() { close(eraseStarted) })
			<-releaseErase
			_, _ = w.Write([]byte(`{"status":"completed","source_operation_id":"erase-op"}`))
			return
		}
		writeWorkloadSummary(w)
	}))
	defer server.Close()

	client := newTestConnector(t, server.URL, HTTPConfig{ErasePath: "/erase", MaxConcurrency: 1,
		QueryMaxConcurrency: 1, CommandMaxConcurrency: 1, MaxAttempts: 1})
	eraseDone := make(chan error, 1)
	go func() {
		_, err := client.Erase(t.Context(), EraseRequest{AccountID: domain.ID{1},
			Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}, IdempotencyKey: "erase-1"})
		eraseDone <- err
	}()
	<-eraseStarted

	summaryDone := make(chan error, 1)
	go func() {
		_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1},
			Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
		summaryDone <- err
	}()
	select {
	case err := <-summaryDone:
		if err != nil {
			t.Fatalf("summary while erase is blocked: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("summary was starved by a blocked command")
	}
	close(releaseErase)
	if err := <-eraseDone; err != nil {
		t.Fatalf("erase: %v", err)
	}
}

func TestConnector_HealthCircuitDoesNotOpenSummaryCircuit(t *testing.T) {
	var healthCalls, summaryCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			healthCalls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		summaryCalls.Add(1)
		writeWorkloadSummary(w)
	}))
	defer server.Close()

	registry := metrics.NewRegistry()
	client := newTestConnector(t, server.URL, HTTPConfig{MaxAttempts: 1, Metrics: registry})
	for i := 0; i < 5; i++ {
		if err := client.Health(t.Context()); err == nil {
			t.Fatal("unhealthy probe unexpectedly succeeded")
		}
	}
	if err := client.Health(t.Context()); err == nil {
		t.Fatal("health circuit did not open")
	}
	if got := healthCalls.Load(); got != 5 {
		t.Fatalf("health calls after circuit open = %d, want 5", got)
	}
	if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1},
		Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}}); err != nil {
		t.Fatalf("summary after health circuit opened: %v", err)
	}
	if summaryCalls.Load() != 1 {
		t.Fatalf("summary calls = %d, want 1", summaryCalls.Load())
	}
	var exposition strings.Builder
	registry.WriteText(&exposition)
	text := exposition.String()
	if !strings.Contains(text, `aeroid_source_workload_circuit_state{source="aero-im",workload="health",state="open"} 1`) {
		t.Fatalf("health workload circuit metric missing:\n%s", text)
	}
	if !strings.Contains(text, `aeroid_source_bulkhead_acquisitions_total{source="aero-im",workload="query"}`) {
		t.Fatalf("query bulkhead metric missing:\n%s", text)
	}
}

func writeWorkloadSummary(w http.ResponseWriter) {
	_, _ = w.Write([]byte(`{"source_region":"west","version":1,"generated_at":"2026-08-20T00:00:00Z","datasets":{}}`))
}
