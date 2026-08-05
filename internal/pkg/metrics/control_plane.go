package metrics

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Control-plane metrics (P6 release gates): projection lag, operation age and
// outcome, audit ingest failures, callback retries/DLQ, reconciliation drift,
// and replication cursor lag. All counters are bounded maps keyed by controlled
// values (source/dataset/status), never request paths or IDs.

type gauge struct {
	value float64
	at    time.Time
}

type counterMap struct {
	mu    sync.Mutex
	items map[string]uint64
}

func (m *counterMap) add(key string, delta uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.items == nil {
		m.items = map[string]uint64{}
	}
	m.items[key] += delta
}

func (m *counterMap) snapshot() map[string]uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]uint64, len(m.items))
	for key, value := range m.items {
		out[key] = value
	}
	return out
}

type gaugeMap struct {
	mu    sync.Mutex
	items map[string]gauge
}

func (m *gaugeMap) set(key string, value float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.items == nil {
		m.items = map[string]gauge{}
	}
	m.items[key] = gauge{value: value, at: time.Now()}
}

func (m *gaugeMap) snapshot() map[string]float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]float64, len(m.items))
	for key, item := range m.items {
		if time.Since(item.at) < 5*time.Minute {
			out[key] = item.value
		}
	}
	return out
}

// ── control-plane counters/gauges on the shared Registry ──────────────────

type controlPlaneMetrics struct {
	eventsApplied   counterMap // dataset -> count of applied source events
	eventsQuarantined counterMap
	auditIngestFail counterMap // source -> audit fact write failures
	operations      counterMap // status -> terminal operation outcomes
	callbacks       counterMap // status -> callback delivery outcomes (success/pending/dead_letter)
	reconcileDrift  counterMap // source -> drift repair count
	projectionLag   gaugeMap   // source|dataset -> seconds since last_synced_at
	consumerLag     gaugeMap   // source -> seconds behind (replication cursor)
	operationAge    gaugeMap   // status -> age seconds of oldest open operation
}

func (r *Registry) controlPlane() *controlPlaneMetrics {
	return &r.control
}

// ObserveEventApplied records one applied source event per dataset.
func (r *Registry) ObserveEventApplied(dataset string) {
	r.control.eventsApplied.add(dataset, 1)
}

// ObserveEventQuarantined records one quarantined source event.
func (r *Registry) ObserveEventQuarantined(dataset string) {
	r.control.eventsQuarantined.add(dataset, 1)
}

// ObserveAuditIngestFailure records a durable audit write failure per source.
func (r *Registry) ObserveAuditIngestFailure(source string) {
	r.control.auditIngestFail.add(source, 1)
}

// ObserveOperationOutcome records a terminal operation outcome.
func (r *Registry) ObserveOperationOutcome(status string) {
	r.control.operations.add(status, 1)
}

// ObserveCallbackOutcome records a callback delivery outcome.
func (r *Registry) ObserveCallbackOutcome(status string) {
	r.control.callbacks.add(status, 1)
}

// ObserveReconcileDrift records a reconciliation repair per source.
func (r *Registry) ObserveReconcileDrift(source string) {
	r.control.reconcileDrift.add(source, 1)
}

// ObserveProjectionLag records the freshness lag of a projection.
func (r *Registry) ObserveProjectionLag(source, dataset string, lag time.Duration) {
	r.control.projectionLag.set(source+"|"+dataset, lag.Seconds())
}

// ObserveConsumerLag records the replication consumer lag per source.
func (r *Registry) ObserveConsumerLag(source string, lag time.Duration) {
	r.control.consumerLag.set(source, lag.Seconds())
}

// ObserveOperationAge records the age of the oldest open operation per status.
func (r *Registry) ObserveOperationAge(status string, age time.Duration) {
	r.control.operationAge.set(status, age.Seconds())
}

func (r *Registry) writeControlPlaneText(w io.Writer) {
	for key, value := range r.control.eventsApplied.snapshot() {
		fmt.Fprintf(w, "aero_control_events_applied_total{dataset=%q} %d\n", key, value)
	}
	for key, value := range r.control.eventsQuarantined.snapshot() {
		fmt.Fprintf(w, "aero_control_events_quarantined_total{dataset=%q} %d\n", key, value)
	}
	for key, value := range r.control.auditIngestFail.snapshot() {
		fmt.Fprintf(w, "aero_control_audit_ingest_failures_total{source=%q} %d\n", key, value)
	}
	for key, value := range r.control.operations.snapshot() {
		fmt.Fprintf(w, "aero_control_operations_total{status=%q} %d\n", key, value)
	}
	for key, value := range r.control.callbacks.snapshot() {
		fmt.Fprintf(w, "aero_control_callback_deliveries_total{status=%q} %d\n", key, value)
	}
	for key, value := range r.control.reconcileDrift.snapshot() {
		fmt.Fprintf(w, "aero_control_reconcile_drift_total{source=%q} %d\n", key, value)
	}
	for key, value := range sortedGauges(r.control.projectionLag.snapshot()) {
		fmt.Fprintf(w, "aero_control_projection_lag_seconds{source_dataset=%q} %s\n", key, strconv.FormatFloat(value, 'g', -1, 64))
	}
	for key, value := range sortedGauges(r.control.consumerLag.snapshot()) {
		fmt.Fprintf(w, "aero_control_consumer_lag_seconds{source=%q} %s\n", key, strconv.FormatFloat(value, 'g', -1, 64))
	}
	for key, value := range sortedGauges(r.control.operationAge.snapshot()) {
		fmt.Fprintf(w, "aero_control_operation_age_seconds{status=%q} %s\n", key, strconv.FormatFloat(value, 'g', -1, 64))
	}
}

func sortedGauges(values map[string]float64) map[string]float64 {
	// deterministic output for tests and scrapers
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]float64, len(keys))
	for _, key := range keys {
		out[key] = values[key]
	}
	return out
}

var _ = strings.TrimSpace
