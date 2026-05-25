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
}

func NewRegistry() *Registry {
	return &Registry{
		buckets:  defaultBuckets,
		requests: make(map[string]uint64),
		latency:  make(map[string]*histogram),
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
}

func sortedKeys(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
