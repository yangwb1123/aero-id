package tracing_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/aero/aero-id/internal/pkg/tracing"
)

func TestInit_DisabledInstallsNoopAndCallableShutdown(t *testing.T) {
	shutdown, err := tracing.Init(context.Background(), tracing.Config{
		ServiceName: "aero-id-test",
	})
	if err != nil {
		t.Fatalf("Init(disabled) err = %v, want nil", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned a nil shutdown func")
	}
	// A disabled provider still lets callers start a (no-op) span without panicking.
	_, span := otel.Tracer("test").Start(context.Background(), "noop")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown err = %v, want nil", err)
	}
}

func TestInit_EnabledBuildsProviderWithCallableShutdown(t *testing.T) {
	// An endpoint enables tracing. The OTLP/gRPC exporter dials lazily, so Init
	// succeeds without a live collector and the returned shutdown is callable.
	shutdown, err := tracing.Init(context.Background(), tracing.Config{
		Enabled:     true,
		Endpoint:    "localhost:4317",
		Insecure:    true,
		ServiceName: "aero-id-test",
		SampleRatio: 0.5,
	})
	if err != nil {
		t.Fatalf("Init(enabled) err = %v, want nil", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned a nil shutdown func")
	}
	_, span := tracing.Tracer("test").Start(context.Background(), "span")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown err = %v, want nil", err)
	}
}
