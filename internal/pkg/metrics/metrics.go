// Package metrics provides a tiny, dependency-free Prometheus text exposition of
// RED metrics (request rate, errors, duration) shared by the HTTP and gRPC
// transports. It deliberately avoids client_golang to keep the build self-contained.
package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// defaultBuckets are latency histogram upper bounds, in seconds.
var defaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type histogram struct {
	bucketCounts []uint64 // cumulative counts aligned with Registry.buckets
	sum          float64
	count        uint64
}

// Registry accumulates request counters and latency histograms in memory and renders
// them in Prometheus text exposition format. It is safe for concurrent use.
//
// Locking strategy:
//   - mu protects requests + latency (HTTP/gRPC RED metrics) and SSO metrics.
//   - outboxMu protects outbox-specific metrics (published, failed, drain histogram).
//   - outboxPending and outboxLagSecs are atomic gauges (last-write) so they can be
//     read/written without any lock, reducing contention on the outbox drain path
//     which runs every 5s and would otherwise block HTTP/gRPC metrics.
type Registry struct {
	buckets  []float64
	mu       sync.Mutex
	requests map[string]uint64     // "transport|method|route|status" -> count
	latency  map[string]*histogram // "transport|route" -> histogram

	sourceMu                sync.Mutex
	sourceCalls             map[string]*sourceMetric
	sourceCircuits          map[string]string
	sourceWorkloadCircuits  map[string]string
	sourceBulkheads         map[string]*sourceBulkheadMetric
	sourceAuthCompatibility uint64

	control controlPlaneMetrics
	idem    idempotencyMetrics

	// SSO dependency health, populated by the SSO client (see SSOSink).
	ssoCacheHits   map[string]uint64     // cache ("token"|"user") -> hits
	ssoCacheMisses map[string]uint64     // cache ("token"|"user") -> misses
	ssoErrors      map[string]uint64     // op ("validate"|"get_users"|"authorize") -> errors
	ssoLatency     map[string]*histogram // op ("validate"|"get_users"|"authorize") -> upstream latency

	// Transactional-outbox relay observability. outboxMu isolates these fields so
	// outbox drain operations (every 5s) never block HTTP/gRPC or SSO metrics.
	outboxMu                 sync.Mutex
	outboxPublished          map[string]uint64 // event_type -> durably published events
	outboxFailed             map[string]uint64 // event_type -> handler failures (left for retry)
	outboxDeadLetter         map[string]uint64 // event_type -> successful terminal transitions
	outboxClaimErrors        uint64
	outboxClaimLost          uint64
	outboxFinalizationErrors uint64
	outboxRenewalErrors      uint64
	outboxDrainPasses        uint64
	outboxDrain              *histogram // per-pass drain duration in seconds

	// Outbox gauges are last-write values stored as atomic uint64 bits
	// (math.Float64bits). The relay writes them every 5s without any lock;
	// /metrics reads them under the main mu (via WriteText).
	outboxPending          atomic.Uint64
	outboxClaimed          atomic.Uint64
	outboxLagSecs          atomic.Uint64
	outboxEligibilityKnown atomic.Uint64
	// outboxEligibilityValueKnown distinguishes an initial unknown observation
	// from a real zero backlog. It prevents first-scrape zeroes being mistaken
	// for evidence that the outbox is empty.
	outboxEligibilityValueKnown atomic.Uint64
}

func NewRegistry() *Registry {
	return &Registry{
		buckets:                defaultBuckets,
		requests:               make(map[string]uint64),
		latency:                make(map[string]*histogram),
		sourceCalls:            make(map[string]*sourceMetric),
		sourceCircuits:         make(map[string]string),
		sourceWorkloadCircuits: make(map[string]string),
		sourceBulkheads:        make(map[string]*sourceBulkheadMetric),
		ssoCacheHits:           make(map[string]uint64),
		ssoCacheMisses:         make(map[string]uint64),
		ssoErrors:              make(map[string]uint64),
		ssoLatency:             make(map[string]*histogram),

		outboxPublished:  make(map[string]uint64),
		outboxFailed:     make(map[string]uint64),
		outboxDeadLetter: make(map[string]uint64),
	}
}

// ObserveHTTP records one HTTP request. route should be the matched template
// (e.g. "/v1/files/:id"), not the raw path, to keep label cardinality bounded.
func (r *Registry) ObserveHTTP(method, route string, status int, dur time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	r.observe("http", method, route, strconv.Itoa(status), dur)
}

// ObserveGRPC records one gRPC unary call. code is the status code string (e.g. "OK").
func (r *Registry) ObserveGRPC(fullMethod, code string, dur time.Duration) {
	r.observe("grpc", "rpc", fullMethod, code, dur)
}

func (r *Registry) observe(transport, method, route, status string, dur time.Duration) {
	reqKey := strings.Join([]string{transport, method, route, status}, "|")
	latKey := transport + "|" + route
	secs := dur.Seconds()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests[reqKey]++
	h := r.latency[latKey]
	if h == nil {
		h = &histogram{bucketCounts: make([]uint64, len(r.buckets))}
		r.latency[latKey] = h
	}
	for i, b := range r.buckets {
		if secs <= b {
			h.bucketCounts[i]++
		}
	}
	h.sum += secs
	h.count++
}

// ObserveSSOCache records one SSO cache lookup. cache is "token" or "user"; hit
// distinguishes a cache hit from a miss. Together they expose a per-cache hit ratio.
func (r *Registry) ObserveSSOCache(cache string, hit bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if hit {
		r.ssoCacheHits[cache]++
	} else {
		r.ssoCacheMisses[cache]++
	}
}

// ObserveSSOCall records one upstream SSO RPC. op is "validate", "get_users", or
// "authorize", dur is the call latency, and ok is false when the RPC failed (counts
// toward the error rate). Latency is recorded for every call regardless of outcome.
func (r *Registry) ObserveSSOCall(op string, dur time.Duration, ok bool) {
	secs := dur.Seconds()
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.ssoLatency[op]
	if h == nil {
		h = &histogram{bucketCounts: make([]uint64, len(r.buckets))}
		r.ssoLatency[op] = h
	}
	for i, b := range r.buckets {
		if secs <= b {
			h.bucketCounts[i]++
		}
	}
	h.sum += secs
	h.count++
	if !ok {
		r.ssoErrors[op]++
	}
}

// ObserveOutboxPublished records one successfully published outbox event of the
// given event_type. eventType "" is bucketed under "unknown" to keep the series
// well-formed.
func (r *Registry) ObserveOutboxPublished(eventType string) {
	if eventType == "" {
		eventType = "unknown"
	}
	r.outboxMu.Lock()
	r.outboxPublished[eventType]++
	r.outboxMu.Unlock()
}

// ObserveOutboxFailed records one outbox event whose handler errored (the event
// stays unpublished and is retried on the next tick).
func (r *Registry) ObserveOutboxFailed(eventType string) {
	if eventType == "" {
		eventType = "unknown"
	}
	r.outboxMu.Lock()
	r.outboxFailed[eventType]++
	r.outboxMu.Unlock()
}

// ObserveOutboxDeadLetter records a terminal retry transition confirmed by
// MarkFailed. It is a counter, not a current-row gauge, because rows may later
// be purged or replayed.
func (r *Registry) ObserveOutboxDeadLetter(eventType string) {
	if eventType == "" {
		eventType = "unknown"
	}
	r.outboxMu.Lock()
	r.outboxDeadLetter[eventType]++
	r.outboxMu.Unlock()
}

func (r *Registry) ObserveOutboxClaimError() {
	r.outboxMu.Lock()
	r.outboxClaimErrors++
	r.outboxMu.Unlock()
}

func (r *Registry) ObserveOutboxClaimLost() {
	r.outboxMu.Lock()
	r.outboxClaimLost++
	r.outboxMu.Unlock()
}

func (r *Registry) ObserveOutboxFinalizationError() {
	r.outboxMu.Lock()
	r.outboxFinalizationErrors++
	r.outboxMu.Unlock()
}

func (r *Registry) ObserveOutboxRenewalError() {
	r.outboxMu.Lock()
	r.outboxRenewalErrors++
	r.outboxMu.Unlock()
}

// ObserveOutboxDrain records one drain pass. Pending and oldestAge are updated
// only when eligibilityKnown is true; this prevents a stats outage from
// overwriting known gauges with fabricated zeroes. Claimed and eligibilityKnown
// are always updated. Gauges are atomics so they never block request metrics;
// the drain histogram remains under outboxMu.
func (r *Registry) ObserveOutboxDrain(pending, claimed int, oldestAge, dur time.Duration, eligibilityKnown bool) {
	if eligibilityKnown {
		r.outboxPending.Store(math.Float64bits(float64(pending)))
		r.outboxLagSecs.Store(math.Float64bits(oldestAge.Seconds()))
		r.outboxEligibilityValueKnown.Store(1)
	}
	r.outboxClaimed.Store(math.Float64bits(float64(claimed)))
	known := 0.0
	if eligibilityKnown {
		known = 1
	}
	r.outboxEligibilityKnown.Store(math.Float64bits(known))

	secs := dur.Seconds()
	r.outboxMu.Lock()
	r.outboxDrainPasses++
	if r.outboxDrain == nil {
		r.outboxDrain = &histogram{bucketCounts: make([]uint64, len(r.buckets))}
	}
	for i, b := range r.buckets {
		if secs <= b {
			r.outboxDrain.bucketCounts[i]++
		}
	}
	r.outboxDrain.sum += secs
	r.outboxDrain.count++
	r.outboxMu.Unlock()
}

// Handler serves the current metrics in Prometheus text format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.WriteText(w)
	})
}

// WriteText renders the exposition with keys sorted for stable, diffable output.
func (r *Registry) WriteText(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.writeControlPlaneText(w)

	fmt.Fprintln(w, "# HELP aeroid_requests_total Total requests handled.")
	fmt.Fprintln(w, "# TYPE aeroid_requests_total counter")
	for _, k := range sortedKeys(r.requests) {
		p := strings.SplitN(k, "|", 4)
		fmt.Fprintf(w, "aeroid_requests_total{transport=%q,method=%q,route=%q,status=%q} %d\n",
			p[0], p[1], p[2], p[3], r.requests[k])
	}

	fmt.Fprintln(w, "# HELP aeroid_request_duration_seconds Request latency in seconds.")
	fmt.Fprintln(w, "# TYPE aeroid_request_duration_seconds histogram")
	latKeys := make([]string, 0, len(r.latency))
	for k := range r.latency {
		latKeys = append(latKeys, k)
	}
	sort.Strings(latKeys)
	for _, k := range latKeys {
		p := strings.SplitN(k, "|", 2)
		h := r.latency[k]
		for i, b := range r.buckets {
			fmt.Fprintf(w, "aeroid_request_duration_seconds_bucket{transport=%q,route=%q,le=%q} %d\n",
				p[0], p[1], strconv.FormatFloat(b, 'g', -1, 64), h.bucketCounts[i])
		}
		fmt.Fprintf(w, "aeroid_request_duration_seconds_bucket{transport=%q,route=%q,le=\"+Inf\"} %d\n", p[0], p[1], h.count)
		fmt.Fprintf(w, "aeroid_request_duration_seconds_sum{transport=%q,route=%q} %g\n", p[0], p[1], h.sum)
		fmt.Fprintf(w, "aeroid_request_duration_seconds_count{transport=%q,route=%q} %d\n", p[0], p[1], h.count)
	}

	r.writeSSOText(w)
	r.writeSourceText(w)
	r.writeOutboxText(w)
	r.writeIdempotencyText(w)
}

// writeOutboxText renders the transactional-outbox relay series. Caller holds r.mu.
func (r *Registry) writeOutboxText(w io.Writer) {
	// Read atomic gauges — safe without outboxMu because float64 atomic reads and
	// writes are independent; the gauge values are last-write and slightly stale is
	// acceptable for Prometheus exposition.
	pending := math.Float64frombits(r.outboxPending.Load())
	claimed := math.Float64frombits(r.outboxClaimed.Load())
	lagSecs := math.Float64frombits(r.outboxLagSecs.Load())
	eligibilityKnown := math.Float64frombits(r.outboxEligibilityKnown.Load())
	eligibilityValueKnown := r.outboxEligibilityValueKnown.Load() == 1

	// Copy maps while holding outboxMu. The relay can update these maps while
	// /metrics is rendering, and iterating a live map would be a data race.
	r.outboxMu.Lock()
	pub := copyCounterMap(r.outboxPublished)
	fail := copyCounterMap(r.outboxFailed)
	dead := copyCounterMap(r.outboxDeadLetter)
	claimErrors := r.outboxClaimErrors
	claimLost := r.outboxClaimLost
	finalizationErrors := r.outboxFinalizationErrors
	renewalErrors := r.outboxRenewalErrors
	drainPasses := r.outboxDrainPasses
	drain := r.outboxDrain
	r.outboxMu.Unlock()

	if len(pub) > 0 {
		fmt.Fprintln(w, "# HELP aeroid_outbox_published_total Outbox events durably finalized as published by event_type.")
		fmt.Fprintln(w, "# TYPE aeroid_outbox_published_total counter")
		for _, k := range sortedKeys(pub) {
			fmt.Fprintf(w, "aeroid_outbox_published_total{event_type=%q} %d\n", k, pub[k])
		}
	}

	if len(fail) > 0 {
		fmt.Fprintln(w, "# HELP aeroid_outbox_failed_total Outbox events whose handler errored (retried) by event_type.")
		fmt.Fprintln(w, "# TYPE aeroid_outbox_failed_total counter")
		for _, k := range sortedKeys(fail) {
			fmt.Fprintf(w, "aeroid_outbox_failed_total{event_type=%q} %d\n", k, fail[k])
		}
	}

	if drain != nil {
		writeOutboxOperationalCounters(w, dead, claimErrors, claimLost, finalizationErrors, renewalErrors, drainPasses)
		writeOutboxBacklogMetrics(w, pending, claimed, eligibilityKnown, lagSecs, eligibilityValueKnown)
		writeOutboxDrainHistogram(w, r.buckets, drain)
	}
}

func writeOutboxDrainHistogram(w io.Writer, buckets []float64, h *histogram) {
	fmt.Fprintln(w, "# HELP aeroid_outbox_drain_duration_seconds Outbox relay drain-pass duration in seconds.")
	fmt.Fprintln(w, "# TYPE aeroid_outbox_drain_duration_seconds histogram")
	for i, b := range buckets {
		fmt.Fprintf(w, "aeroid_outbox_drain_duration_seconds_bucket{le=%q} %d\n",
			strconv.FormatFloat(b, 'g', -1, 64), h.bucketCounts[i])
	}
	fmt.Fprintf(w, "aeroid_outbox_drain_duration_seconds_bucket{le=\"+Inf\"} %d\n", h.count)
	fmt.Fprintf(w, "aeroid_outbox_drain_duration_seconds_sum %g\n", h.sum)
	fmt.Fprintf(w, "aeroid_outbox_drain_duration_seconds_count %d\n", h.count)
}

func writeOutboxOperationalCounters(w io.Writer, dead map[string]uint64, claimErrors, claimLost, finalizationErrors, renewalErrors, drainPasses uint64) {
	fmt.Fprintln(w, "# HELP aeroid_outbox_claim_errors_total Outbox claim query failures.")
	fmt.Fprintln(w, "# TYPE aeroid_outbox_claim_errors_total counter")
	fmt.Fprintf(w, "aeroid_outbox_claim_errors_total %d\n", claimErrors)
	fmt.Fprintln(w, "# HELP aeroid_outbox_claim_lost_total Outbox finalization attempts rejected because the lease/token was stale or expired.")
	fmt.Fprintln(w, "# TYPE aeroid_outbox_claim_lost_total counter")
	fmt.Fprintf(w, "aeroid_outbox_claim_lost_total %d\n", claimLost)
	fmt.Fprintln(w, "# HELP aeroid_outbox_finalization_errors_total Outbox publish or retry-state finalization failures excluding expected claim loss.")
	fmt.Fprintln(w, "# TYPE aeroid_outbox_finalization_errors_total counter")
	fmt.Fprintf(w, "aeroid_outbox_finalization_errors_total %d\n", finalizationErrors)
	fmt.Fprintln(w, "# HELP aeroid_outbox_renewal_errors_total Outbox claim renewal failures.")
	fmt.Fprintln(w, "# TYPE aeroid_outbox_renewal_errors_total counter")
	fmt.Fprintf(w, "aeroid_outbox_renewal_errors_total %d\n", renewalErrors)
	fmt.Fprintln(w, "# HELP aeroid_outbox_drain_passes_total Outbox drain passes completed, including empty and failed-claim passes.")
	fmt.Fprintln(w, "# TYPE aeroid_outbox_drain_passes_total counter")
	fmt.Fprintf(w, "aeroid_outbox_drain_passes_total %d\n", drainPasses)
	if len(dead) == 0 {
		return
	}
	fmt.Fprintln(w, "# HELP aeroid_outbox_dead_letter_total Outbox events whose terminal retry transition was durably recorded.")
	fmt.Fprintln(w, "# TYPE aeroid_outbox_dead_letter_total counter")
	for _, k := range sortedKeys(dead) {
		fmt.Fprintf(w, "aeroid_outbox_dead_letter_total{event_type=%q} %d\n", k, dead[k])
	}
}

func writeOutboxBacklogMetrics(w io.Writer, pending, claimed, eligibilityKnown, lagSecs float64, valueKnown bool) {
	if valueKnown {
		fmt.Fprintln(w, "# HELP aeroid_outbox_pending Eligible unpublished outbox events seen on the last drain pass.")
		fmt.Fprintln(w, "# TYPE aeroid_outbox_pending gauge")
		fmt.Fprintf(w, "aeroid_outbox_pending %g\n", pending)
	}

	fmt.Fprintln(w, "# HELP aeroid_outbox_claimed Claimed outbox events in the last drain pass.")
	fmt.Fprintln(w, "# TYPE aeroid_outbox_claimed gauge")
	fmt.Fprintf(w, "aeroid_outbox_claimed %g\n", claimed)

	fmt.Fprintln(w, "# HELP aeroid_outbox_eligibility_known Whether eligible backlog statistics were available (1 or 0).")
	fmt.Fprintln(w, "# TYPE aeroid_outbox_eligibility_known gauge")
	fmt.Fprintf(w, "aeroid_outbox_eligibility_known %g\n", eligibilityKnown)

	if valueKnown {
		fmt.Fprintln(w, "# HELP aeroid_outbox_oldest_age_seconds Age of the oldest eligible unpublished outbox event in seconds.")
		fmt.Fprintln(w, "# TYPE aeroid_outbox_oldest_age_seconds gauge")
		fmt.Fprintf(w, "aeroid_outbox_oldest_age_seconds %g\n", lagSecs)
	}
}

func copyCounterMap(src map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

// writeSSOText renders the SSO dependency-health series. Caller holds r.mu.
//
//nolint:cyclop // the text exposition format has independent cache, error, and latency sections.
func (r *Registry) writeSSOText(w io.Writer) {
	if len(r.ssoCacheHits) > 0 || len(r.ssoCacheMisses) > 0 {
		fmt.Fprintln(w, "# HELP aeroid_sso_cache_hits_total SSO cache hits by cache.")
		fmt.Fprintln(w, "# TYPE aeroid_sso_cache_hits_total counter")
		for _, k := range sortedKeys(r.ssoCacheHits) {
			fmt.Fprintf(w, "aeroid_sso_cache_hits_total{cache=%q} %d\n", k, r.ssoCacheHits[k])
		}
		fmt.Fprintln(w, "# HELP aeroid_sso_cache_misses_total SSO cache misses by cache.")
		fmt.Fprintln(w, "# TYPE aeroid_sso_cache_misses_total counter")
		for _, k := range sortedKeys(r.ssoCacheMisses) {
			fmt.Fprintf(w, "aeroid_sso_cache_misses_total{cache=%q} %d\n", k, r.ssoCacheMisses[k])
		}
	}

	if len(r.ssoErrors) > 0 {
		fmt.Fprintln(w, "# HELP aeroid_sso_errors_total SSO upstream errors by op.")
		fmt.Fprintln(w, "# TYPE aeroid_sso_errors_total counter")
		for _, k := range sortedKeys(r.ssoErrors) {
			fmt.Fprintf(w, "aeroid_sso_errors_total{op=%q} %d\n", k, r.ssoErrors[k])
		}
	}

	if len(r.ssoLatency) > 0 {
		fmt.Fprintln(w, "# HELP aeroid_sso_call_duration_seconds SSO upstream call latency in seconds.")
		fmt.Fprintln(w, "# TYPE aeroid_sso_call_duration_seconds histogram")
		ops := make([]string, 0, len(r.ssoLatency))
		for k := range r.ssoLatency {
			ops = append(ops, k)
		}
		sort.Strings(ops)
		for _, op := range ops {
			h := r.ssoLatency[op]
			for i, b := range r.buckets {
				fmt.Fprintf(w, "aeroid_sso_call_duration_seconds_bucket{op=%q,le=%q} %d\n",
					op, strconv.FormatFloat(b, 'g', -1, 64), h.bucketCounts[i])
			}
			fmt.Fprintf(w, "aeroid_sso_call_duration_seconds_bucket{op=%q,le=\"+Inf\"} %d\n", op, h.count)
			fmt.Fprintf(w, "aeroid_sso_call_duration_seconds_sum{op=%q} %g\n", op, h.sum)
			fmt.Fprintf(w, "aeroid_sso_call_duration_seconds_count{op=%q} %d\n", op, h.count)
		}
	}
}

func sortedKeys(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
