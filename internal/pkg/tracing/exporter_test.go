package tracing

import (
	"context"
	"errors"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type recordingExporter struct {
	err      error
	calls    int
	shutdown int
}

func (e *recordingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	e.calls++
	return e.err
}

func (e *recordingExporter) Shutdown(context.Context) error {
	e.shutdown++
	return nil
}

func TestCountingExporterFeedsDynamicSampler(t *testing.T) {
	next := &recordingExporter{}
	sampler := NewDynamicSampler(0.5)
	exporter := newCountingExporter(next, sampler)

	if err := exporter.ExportSpans(context.Background(), nil); err != nil {
		t.Fatalf("successful export: %v", err)
	}
	next.err = errors.New("collector unavailable")
	if err := exporter.ExportSpans(context.Background(), nil); !errors.Is(err, next.err) {
		t.Fatalf("failed export = %v, want %v", err, next.err)
	}
	if next.calls != 2 {
		t.Fatalf("export calls = %d, want 2", next.calls)
	}
	if got := sampler.exportAttempts.Load(); got != 2 {
		t.Fatalf("export attempts = %d, want 2", got)
	}
	if got := sampler.exportErrors.Load(); got != 1 {
		t.Fatalf("export errors = %d, want 1", got)
	}
	if err := exporter.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if next.shutdown != 1 {
		t.Fatalf("shutdown calls = %d, want 1", next.shutdown)
	}
}

func TestDynamicSamplerDegradesAfterExportErrors(t *testing.T) {
	sampler := NewDynamicSampler(0.5)
	sampler.startMonitorWithIntervals(0.5, 5*time.Millisecond, 20*time.Millisecond)
	for i := 0; i < 10; i++ {
		sampler.RecordExport(errors.New("collector unavailable"))
	}
	deadline := time.Now().Add(time.Second)
	for sampler.loadRatio() > minRatio && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	sampler.stopMonitor()
	if got := sampler.loadRatio(); got != minRatio {
		t.Fatalf("degraded ratio = %v, want %v", got, minRatio)
	}
}

func TestDynamicSamplerStopIsSafeBeforeStartAndIdempotent(t *testing.T) {
	sampler := NewDynamicSampler(0.5)
	sampler.stopMonitor()
	sampler.stopMonitor()

	sampler = NewDynamicSampler(0.5)
	sampler.startMonitorWithIntervals(0.5, time.Hour, time.Hour)
	sampler.stopMonitor()
	sampler.stopMonitor()
}
