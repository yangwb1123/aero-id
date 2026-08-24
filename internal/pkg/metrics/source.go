package metrics

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

type sourceMetric struct {
	calls   uint64
	errors  uint64
	latency *histogram
}

// ObserveSourceCall records bounded per-source connector latency and failures.
// source and operation are controlled values, never request paths or IDs.
func (r *Registry) ObserveSourceCall(source, operation string, duration time.Duration, callErr error) {
	key := source + "|" + operation
	r.sourceMu.Lock()
	defer r.sourceMu.Unlock()
	item := r.sourceCalls[key]
	if item == nil {
		item = &sourceMetric{latency: &histogram{bucketCounts: make([]uint64, len(r.buckets))}}
		r.sourceCalls[key] = item
	}
	item.calls++
	if callErr != nil {
		item.errors++
	}
	seconds := duration.Seconds()
	for i, bucket := range r.buckets {
		if seconds <= bucket {
			item.latency.bucketCounts[i]++
		}
	}
	item.latency.sum += seconds
	item.latency.count++
}

// ObserveSourceCircuit exposes the current local breaker state. It is advisory;
// the breaker itself remains the connector's correctness boundary.
func (r *Registry) ObserveSourceCircuit(source, state string) {
	if strings.TrimSpace(source) == "" {
		return
	}
	r.sourceMu.Lock()
	r.sourceCircuits[source] = state
	r.sourceMu.Unlock()
}

// ObserveSourceAuthCompatibility records the explicitly enabled, unauthenticated
// loopback compatibility mode. The accepted label pair is fixed so this signal
// cannot become an endpoint, token, or request-identity cardinality leak.
func (r *Registry) ObserveSourceAuthCompatibility(source, mode string) {
	if source != "aero-im" || mode != "loopback" {
		return
	}
	r.sourceMu.Lock()
	r.sourceAuthCompatibility++
	r.sourceMu.Unlock()
}

func (r *Registry) writeSourceText(w io.Writer) {
	r.sourceMu.Lock()
	defer r.sourceMu.Unlock()
	if len(r.sourceCalls) > 0 {
		fmt.Fprintln(w, "# HELP aeroid_source_calls_total Source connector calls by source and operation.")
		fmt.Fprintln(w, "# TYPE aeroid_source_calls_total counter")
		for _, key := range sortedKeysSource(r.sourceCalls) {
			parts := strings.SplitN(key, "|", 2)
			fmt.Fprintf(w, "aeroid_source_calls_total{source=%q,operation=%q} %d\n", parts[0], parts[1], r.sourceCalls[key].calls)
		}
		fmt.Fprintln(w, "# HELP aeroid_source_errors_total Source connector failures by source and operation.")
		fmt.Fprintln(w, "# TYPE aeroid_source_errors_total counter")
		for _, key := range sortedKeysSource(r.sourceCalls) {
			parts := strings.SplitN(key, "|", 2)
			fmt.Fprintf(w, "aeroid_source_errors_total{source=%q,operation=%q} %d\n", parts[0], parts[1], r.sourceCalls[key].errors)
		}
		fmt.Fprintln(w, "# HELP aeroid_source_call_duration_seconds Source connector latency.")
		fmt.Fprintln(w, "# TYPE aeroid_source_call_duration_seconds histogram")
		for _, key := range sortedKeysSource(r.sourceCalls) {
			parts := strings.SplitN(key, "|", 2)
			h := r.sourceCalls[key].latency
			for i, bucket := range r.buckets {
				fmt.Fprintf(w, "aeroid_source_call_duration_seconds_bucket{source=%q,operation=%q,le=%q} %d\n",
					parts[0], parts[1], strconv.FormatFloat(bucket, 'g', -1, 64), h.bucketCounts[i])
			}
			fmt.Fprintf(w, "aeroid_source_call_duration_seconds_bucket{source=%q,operation=%q,le=\"+Inf\"} %d\n", parts[0], parts[1], h.count)
			fmt.Fprintf(w, "aeroid_source_call_duration_seconds_sum{source=%q,operation=%q} %g\n", parts[0], parts[1], h.sum)
			fmt.Fprintf(w, "aeroid_source_call_duration_seconds_count{source=%q,operation=%q} %d\n", parts[0], parts[1], h.count)
		}
	}
	if len(r.sourceCircuits) > 0 {
		fmt.Fprintln(w, "# HELP aeroid_source_circuit_state Local source circuit-breaker state.")
		fmt.Fprintln(w, "# TYPE aeroid_source_circuit_state gauge")
		for _, source := range sortedStringKeys(r.sourceCircuits) {
			fmt.Fprintf(w, "aeroid_source_circuit_state{source=%q,state=%q} 1\n", source, r.sourceCircuits[source])
		}
	}
	if r.sourceAuthCompatibility > 0 {
		fmt.Fprintln(w, "# HELP aeroid_source_auth_compatibility_total Explicit unauthenticated source compatibility constructions.")
		fmt.Fprintln(w, "# TYPE aeroid_source_auth_compatibility_total counter")
		fmt.Fprintf(w, "aeroid_source_auth_compatibility_total{source=\"aero-im\",mode=\"loopback\"} %d\n", r.sourceAuthCompatibility)
	}
}

func sortedKeysSource(values map[string]*sourceMetric) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
