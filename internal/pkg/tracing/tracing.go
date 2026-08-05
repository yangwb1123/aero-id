// Package tracing bootstraps OpenTelemetry tracing for aero-id. It is opt-in: when
// no OTLP endpoint is configured it installs a no-op tracer provider so dev, test
// and CI runs need no collector, yet every callsite can unconditionally start spans.
package tracing

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Config controls the tracer provider built by Init. It is intentionally a small,
// transport-agnostic struct so callers translate config.TracingConfig into it.
type Config struct {
	Enabled     bool
	Endpoint    string // OTLP/gRPC collector address, e.g. "localhost:4317"
	Insecure    bool   // dial the collector without TLS
	Timeout     time.Duration
	SampleRatio float64 // 0..1 parent-based ratio sampler; <=0 => always sample

	ServiceName    string
	ServiceVersion string
	Environment    string
}

// enabled reports whether real tracing should be wired. An explicit Enabled flag or
// a configured endpoint both turn it on; otherwise we stay no-op.
func (c Config) enabled() bool { return c.Enabled || c.Endpoint != "" }

// Init installs a global tracer provider and propagator and returns a shutdown func.
//
// When tracing is disabled it installs a no-op provider (W3C TraceContext propagation
// is still set so inbound trace headers are honoured) and returns a no-op shutdown,
// never an error — safe for dev/tests/CI. When enabled it builds an OTLP/gRPC exporter
// with a batching span processor; the returned shutdown flushes and closes it.
//
//nolint:funlen // tracing setup must configure propagation, exporter, resource, sampler, and shutdown together.
func Init(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	// Honour inbound W3C trace context on both transports regardless of mode.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if !cfg.enabled() {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	if cfg.Timeout > 0 {
		opts = append(opts, otlptracegrpc.WithTimeout(cfg.Timeout))
	}
	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otlp trace exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
		semconv.DeploymentEnvironment(cfg.Environment),
	))
	if err != nil {
		// A schema-URL mismatch is non-fatal; fall back to just our attributes.
		res = resource.NewSchemaless(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.DeploymentEnvironment(cfg.Environment),
		)
	}

	// Build the sampler. When sample ratio is between 0 and 1 (exclusive), use
	// an adaptive dynamic sampler that responds to export failures; otherwise
	// fall through to the static always-sample or fixed-ratio sampler.
	sampler := sdktrace.ParentBased(sdktrace.AlwaysSample())
	var dynamic *DynamicSampler
	if cfg.SampleRatio > 0 && cfg.SampleRatio < 1 {
		dynamic = NewDynamicSampler(cfg.SampleRatio)
		sampler = sdktrace.ParentBased(dynamic)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)
	otel.SetTracerProvider(tp)

	// If adaptive sampling is active, start the background monitor goroutine.
	if dynamic != nil {
		dynamic.startMonitor(cfg.SampleRatio)
	}

	// Return a shutdown that first stops the adaptive monitor if running.
	return func(ctx context.Context) error {
		if dynamic != nil {
			dynamic.stopMonitor()
		}
		return tp.Shutdown(ctx)
	}, nil
}

// Tracer returns the named tracer from the globally-installed provider. Callers use
// it instead of reaching for otel.Tracer directly so the import surface stays small.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }

// ─────────────────────────────────────────────────────────────────────
// DynamicSampler adapts the sampling ratio based on the OTLP exporter's
// health. When the export error rate exceeds a threshold the sampler
// reduces its ratio to protect the collector and the application from
// back-pressure; when the collector recovers the ratio is gradually
// restored to its original value.
//
// It implements sdktrace.Sampler and is safe for concurrent use.
// ─────────────────────────────────────────────────────────────────────

// samplingWindowDuration is the duration of each error-rate observation window.
const samplingWindowDuration = 10 * time.Second

// errRateThreshold is the export error rate (fraction of failed attempts) above
// which the sampler reduces its ratio.
const errRateThreshold = 0.10

// recoveryThreshold is the export error rate below which the sampler considers
// the collector healthy again and begins restoring the ratio.
const recoveryThreshold = 0.02

// recoveryWindows is the number of consecutive healthy windows needed to begin
// restoring the sample ratio.
const recoveryWindows = 5

// minRatio is the floor below which the adaptive sampler never drops, even when
// the collector is completely unreachable.
const minRatio = 0.10

// ratioStep is how much the ratio is bumped per recovery tick.
const ratioStep = 0.10

// recoveryInterval is how often the ratio is incremented during recovery.
const recoveryInterval = 2 * time.Minute

// DynamicSampler wraps an atomic ratio (stored as uint64 bits) and adjusts it
// based on exporter health. It implements sdktrace.Sampler.
type DynamicSampler struct {
	// ratio stores the current sampling ratio as math.Float64bits-encoded uint64.
	ratio         atomic.Uint64
	originalRatio float64

	exportErrors   atomic.Uint64
	exportAttempts atomic.Uint64

	stopCh  chan struct{}
	stopped chan struct{}
}

// NewDynamicSampler creates a DynamicSampler with the given initial ratio.
func NewDynamicSampler(initialRatio float64) *DynamicSampler {
	s := &DynamicSampler{
		originalRatio: initialRatio,
		stopCh:        make(chan struct{}),
		stopped:       make(chan struct{}),
	}
	s.setRatio(initialRatio)
	return s
}

// loadRatio safely reads the current ratio as a float64.
func (s *DynamicSampler) loadRatio() float64 {
	return math.Float64frombits(s.ratio.Load())
}

// setRatio safely stores a float64 ratio.
func (s *DynamicSampler) setRatio(r float64) {
	s.ratio.Store(math.Float64bits(r))
}

// ShouldSample implements sdktrace.Sampler. It returns a probabilistic sampling
// decision based on the current dynamic ratio. TraceID is treated as a random
// 128-bit value; we use the low 64 bits to decide.
func (s *DynamicSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	r := s.loadRatio()
	if r <= 0 {
		return sdktrace.SamplingResult{Decision: sdktrace.Drop}
	}
	if r >= 1 {
		return sdktrace.SamplingResult{Decision: sdktrace.RecordAndSample}
	}
	// Use the low 64 bits of TraceID as a random source for the ratio decision.
	// This is deterministic for the same trace (parent-based) but distributed
	// across traces according to the ratio.
	v := uint64(p.TraceID[8])<<56 | uint64(p.TraceID[9])<<48 |
		uint64(p.TraceID[10])<<40 | uint64(p.TraceID[11])<<32 |
		uint64(p.TraceID[12])<<24 | uint64(p.TraceID[13])<<16 |
		uint64(p.TraceID[14])<<8 | uint64(p.TraceID[15])
	// Scale to 0..1 by dividing by max uint64.
	if float64(v)/math.MaxUint64 < r {
		return sdktrace.SamplingResult{Decision: sdktrace.RecordAndSample}
	}
	return sdktrace.SamplingResult{Decision: sdktrace.Drop}
}

// Description returns a human-readable description of the sampler.
func (s *DynamicSampler) Description() string {
	return "DynamicSampler{adaptive}"
}

// startMonitor launches a background goroutine that periodically checks the
// export error rate and adjusts the sampling ratio accordingly.
//
//nolint:cyclop,funlen // adaptive sampling has explicit healthy, degraded, recovery, and stop states.
func (s *DynamicSampler) startMonitor(originalRatio float64) {
	go func() {
		defer close(s.stopped)

		windowTicker := time.NewTicker(samplingWindowDuration)
		recoveryTicker := time.NewTicker(recoveryInterval)
		defer windowTicker.Stop()
		defer recoveryTicker.Stop()

		var prevErrors uint64
		var prevAttempts uint64
		consecutiveHealthy := 0
		currentlyDegraded := false

		for {
			select {
			case <-s.stopCh:
				return

			case <-windowTicker.C:
				curErrors := s.exportErrors.Load()
				curAttempts := s.exportAttempts.Load()
				deltaErrors := curErrors - prevErrors
				deltaAttempts := curAttempts - prevAttempts
				prevErrors = curErrors
				prevAttempts = curAttempts

				if deltaAttempts == 0 {
					// No exports in this window; treat as healthy.
					consecutiveHealthy++
					continue
				}

				errRate := float64(deltaErrors) / float64(deltaAttempts)

				if errRate > errRateThreshold {
					// High error rate — degrade sampling.
					current := s.loadRatio()
					newRatio := math.Max(minRatio, current/10)
					s.setRatio(newRatio)
					currentlyDegraded = true
					consecutiveHealthy = 0
				} else if errRate < recoveryThreshold && currentlyDegraded {
					consecutiveHealthy++
					if consecutiveHealthy >= recoveryWindows {
						// Mark as recovered; the recovery ticker will
						// gradually restore the ratio.
						currentlyDegraded = false
					}
				} else {
					consecutiveHealthy = 0
				}

			case <-recoveryTicker.C:
				if currentlyDegraded {
					continue
				}
				current := s.loadRatio()
				if current < originalRatio {
					newRatio := math.Min(originalRatio, current+ratioStep)
					s.setRatio(newRatio)
				}
			}
		}
	}()
}

// stopMonitor signals the monitor goroutine to stop and waits for it to finish.
func (s *DynamicSampler) stopMonitor() {
	close(s.stopCh)
	<-s.stopped
}
