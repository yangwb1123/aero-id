// Package metrics provides a tiny, dependency-free Prometheus text exposition of
// RED metrics (request rate, errors, duration) shared by the HTTP and gRPC
// transports. It deliberately avoids client_golang to keep the build self-contained.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
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
type Registry struct {
	buckets  []float64
	mu       sync.Mutex
	requests map[string]uint64     // "transport|method|route|status" -> count
	latency  map[string]*histogram // "transport|route" -> histogram

	// SSO dependency health, populated by the SSO client (see SSOSink).
	ssoCacheHits   map[string]uint64     // cache ("token"|"user") -> hits
	ssoCacheMisses map[string]uint64     // cache ("token"|"user") -> misses
	ssoErrors      map[string]uint64     // op ("validate"|"get_users") -> errors
	ssoLatency     map[string]*histogram // op ("validate"|"get_users") -> upstream latency
}

func NewRegistry() *Registry {
	return &Registry{
		buckets:        defaultBuckets,
		requests:       make(map[string]uint64),
		latency:        make(map[string]*histogram),
		ssoCacheHits:   make(map[string]uint64),
		ssoCacheMisses: make(map[string]uint64),
		ssoErrors:      make(map[string]uint64),
		ssoLatency:     make(map[string]*histogram),
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

// ObserveSSOCall records one upstream SSO RPC. op is "validate" or "get_users", dur
// is the call latency, and ok is false when the RPC failed (counts toward the error
// rate). Latency is recorded for every call regardless of outcome.
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
}

// writeSSOText renders the SSO dependency-health series. Caller holds r.mu.
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
