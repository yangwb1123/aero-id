package tracing

import (
	"context"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// countingExporter keeps the adaptive sampler independent from the concrete
// OTLP exporter while observing each batch result returned by the SDK.
type countingExporter struct {
	next    sdktrace.SpanExporter
	sampler *DynamicSampler
}

func newCountingExporter(next sdktrace.SpanExporter, sampler *DynamicSampler) *countingExporter {
	return &countingExporter{next: next, sampler: sampler}
}

func newSpanExporter(next sdktrace.SpanExporter, sampler *DynamicSampler) sdktrace.SpanExporter {
	if sampler == nil {
		return next
	}
	return newCountingExporter(next, sampler)
}

func (e *countingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.next.ExportSpans(ctx, spans)
	e.sampler.RecordExport(err)
	return err
}

func (e *countingExporter) Shutdown(ctx context.Context) error {
	return e.next.Shutdown(ctx)
}
