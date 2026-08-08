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
	eventsApplied     counterMap // dataset -> count of applied source events
	eventsQuarantined counterMap
	auditIngestFail   counterMap // source -> audit fact write failures
	auditScopeMissing counterMap // source -> audit publications pended on a scope-missing classification
	auditStandalone   counterMap // action -> completed out-of-tx Record executions (sweep-and-lock REQ-3)
	operations        counterMap // status -> terminal operation outcomes
	callbacks         counterMap // status -> callback delivery outcomes (success/pending/dead_letter)
	reconcileDrift    counterMap // source -> drift repair count
	projectionLag     gaugeMap   // source|dataset -> seconds since last_synced_at
	consumerLag       gaugeMap   // source -> seconds behind (replication cursor)
	operationAge      gaugeMap   // status -> age seconds of oldest open operation
	auditFinishFail   counterMap // status -> audit publication finish failures
	auditPubOutcomes  counterMap // status -> audit publication settlements (completed/pending/dead_letter)
	auditPubSkipped   counterMap // count -> drain cycles skipped because the publication repo is unwired
	auditClaimFail    counterMap // count -> drain cycles whose ClaimPublication errored outright
	auditPurged       counterMap // kind -> dead-letter rows purged by the drain retention scan
	auditPurgeFail    counterMap // kind -> dead-letter purge scan failures
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

// ObserveAuditScopeMissing records one audit publication that pended because
// the governance endpoint/token rejected the required audit:event:write
// scope (FM-6/FM-13): the row stays pending at audit.binding_unavailable and
// re-probes on the retry ladder — never dead-lettered, never swallowed. A
// non-zero rate means the client-credentials grant lacks the scope and an
// operator must fix the issuer-side grant.
func (r *Registry) ObserveAuditScopeMissing(source string) {
	r.control.auditScopeMissing.add(source, 1)
}

// ObserveAuditStandaloneRecorded counts one completed out-of-tx audit Record
// execution per action vocabulary (sweep-and-lock REQ-3).
func (r *Registry) ObserveAuditStandaloneRecorded(action string) {
	r.control.auditStandalone.add(action, 1)
}

// ObserveAuditPublicationFinishFailure records a FinishPublication failure per
// target status (FM-4): the row is re-claimed after lease expiry, at-least-once
// delivery is preserved, and the metric is the observability signal.
func (r *Registry) ObserveAuditPublicationFinishFailure(status string, _ error) {
	r.control.auditFinishFail.add(status, 1)
}

// ObserveAuditPublicationOutcome records one settled audit publication per
// terminal-ish status (completed/pending/dead_letter). dead_letter is the
// only settlement with no further delivery: its rate is the FM-5 alert signal
// (governance sink non-delivery after MaxAttempts). Only actually committed
// settlements are counted — a failed finish is a finish-failure, not an
// outcome, because the row stays delivering and the decision is re-made after
// lease-expiry re-claim.
func (r *Registry) ObserveAuditPublicationOutcome(status string) {
	r.control.auditPubOutcomes.add(status, 1)
}

// ObserveAuditPublicationDrainSkipped records a drain cycle that returned
// early because the publication repository was never wired (FM-8): facts pend
// with no delivery attempt. The live composition root wires the repository
// unconditionally (fail-closed assertion), so a non-zero rate means wiring
// regressed and the outbox is silently stalled.
func (r *Registry) ObserveAuditPublicationDrainSkipped() {
	r.control.auditPubSkipped.add("total", 1)
}

// ObserveAuditPublicationClaimFailed records a drain cycle whose
// ClaimPublication errored (FM-1). phase splits two distinct semantics:
//
//	"claim_tx" — the claim tx rolled back atomically; nothing was leased,
//	  facts pend with no attempt, the next tick retries the claim.
//	"fact_hydration" — the claim tx committed (every row delivering) but
//	  post-commit fact hydration failed partway (FM-12); the hydrated prefix
//	  was still delivered and the remainder re-claims after lease expiry,
//	  attempt untouched (M-4).
//
// A non-zero rate means the claim path is persistently failing (schema
// mismatch, DB outage) and must be investigated; the phase label tells ops
// whether rows are stranded delivering (fact_hydration) or merely pending.
func (r *Registry) ObserveAuditPublicationClaimFailed(phase string, _ error) {
	r.control.auditClaimFail.add(phase, 1)
}

// ObserveAuditPublicationPurged records dead-letter rows deleted by the drain
// retention scan (B2-5 MET-1), per kind (publication|delivery). Non-positive
// counts are ignored (RowsAffected is never negative).
func (r *Registry) ObserveAuditPublicationPurged(kind string, count int64) {
	if count <= 0 {
		return
	}
	r.control.auditPurged.add(kind, uint64(count))
}

// ObserveAuditPublicationPurgeFailed records a purge scan error per kind;
// never a silent drop (the scan error contract mirrors the drain's).
func (r *Registry) ObserveAuditPublicationPurgeFailed(kind string, _ error) {
	r.control.auditPurgeFail.add(kind, 1)
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
	for key, value := range r.control.auditScopeMissing.snapshot() {
		fmt.Fprintf(w, "aero_control_audit_scope_missing_total{source=%q} %d\n", key, value)
	}
	for key, value := range r.control.auditStandalone.snapshot() {
		fmt.Fprintf(w, "aero_control_audit_standalone_recorded_total{action=%q} %d\n", key, value)
	}
	writeAuditPublicationFinishFailures(w, r.control.auditFinishFail.snapshot())
	writeAuditPublicationOutcomes(w, r.control.auditPubOutcomes.snapshot())
	writeAuditPublicationDrainSkipped(w, r.control.auditPubSkipped.snapshot())
	writeAuditPublicationClaimFailed(w, r.control.auditClaimFail.snapshot())
	writeAuditPurgeMetrics(w, r.control.auditPurged.snapshot(), r.control.auditPurgeFail.snapshot())
	writeOperationAndCallbackCounters(w, r.control.operations.snapshot(), r.control.callbacks.snapshot())
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

func writeAuditPublicationFinishFailures(w io.Writer, values map[string]uint64) {
	for key, value := range values {
		fmt.Fprintf(w, "aero_control_audit_publication_finish_failures_total{status=%q} %d\n", key, value)
	}
}

func writeAuditPublicationOutcomes(w io.Writer, values map[string]uint64) {
	for key, value := range values {
		fmt.Fprintf(w, "aero_control_audit_publication_outcomes_total{status=%q} %d\n", key, value)
	}
}

func writeAuditPublicationDrainSkipped(w io.Writer, values map[string]uint64) {
	for _, value := range values {
		fmt.Fprintf(w, "aero_control_audit_publication_drain_skipped_total %d\n", value)
	}
}

func writeAuditPublicationClaimFailed(w io.Writer, values map[string]uint64) {
	for key, value := range values {
		fmt.Fprintf(w, "aero_control_audit_publication_claim_failed_total{phase=%q} %d\n", key, value)
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

// writeAuditPurgeMetrics emits the dead-letter purge counters (B2-5 MET-1):
// rows purged per kind and purge-scan failures per kind. Extracted so
// writeControlPlaneText stays under the cyclomatic gate.
func writeAuditPurgeMetrics(w io.Writer, purged, failed map[string]uint64) {
	for key, value := range purged {
		fmt.Fprintf(w, "aero_control_audit_dead_letter_purged_total{kind=%q} %d\n", key, value)
	}
	for key, value := range failed {
		fmt.Fprintf(w, "aero_control_audit_dead_letter_purge_failures_total{kind=%q} %d\n", key, value)
	}
}

// writeOperationAndCallbackCounters emits the operation and callback delivery
// outcome counters (extracted to keep writeControlPlaneText under the
// cyclomatic gate).
func writeOperationAndCallbackCounters(w io.Writer, operations, callbacks map[string]uint64) {
	for key, value := range operations {
		fmt.Fprintf(w, "aero_control_operations_total{status=%q} %d\n", key, value)
	}
	for key, value := range callbacks {
		fmt.Fprintf(w, "aero_control_callback_deliveries_total{status=%q} %d\n", key, value)
	}
}
