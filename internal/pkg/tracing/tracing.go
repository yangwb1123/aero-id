// Package tracing bootstraps OpenTelemetry tracing for aero-id. It is opt-in: when
// no OTLP endpoint is configured it installs a no-op tracer provider so dev, test
// and CI runs need no collector, yet every callsite can unconditionally start spans.
package tracing

import (
	"context"
	"fmt"
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

	sampler := sdktrace.ParentBased(sdktrace.AlwaysSample())
	if cfg.SampleRatio > 0 && cfg.SampleRatio < 1 {
		sampler = sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Tracer returns the named tracer from the globally-installed provider. Callers use
// it instead of reaching for otel.Tracer directly so the import surface stays small.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }
