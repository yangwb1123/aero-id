package logger

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/service"
)

func TestCtxFields_emptyCtxReturnsNoFields(t *testing.T) {
	if got := CtxFields(context.Background()); len(got) != 0 {
		t.Fatalf("CtxFields(background) = %d fields, want 0", len(got))
	}
}

func TestCtxFields_populatedCtxReturnsExactlyThreeFields(t *testing.T) {
	ctx := WithActor(WithRequestID(WithTraceID(context.Background(), "tr-1"), "rq-1"), "u-1")
	fields := CtxFields(ctx)
	if len(fields) != 3 {
		t.Fatalf("len = %d, want 3", len(fields))
	}
	want := []struct {
		key   string
		value string
	}{
		{"trace_id", "tr-1"},
		{"request_id", "rq-1"},
		{"actor_uid", "u-1"},
	}
	for i, w := range want {
		if fields[i].Key != w.key || fields[i].String != w.value {
			t.Fatalf("fields[%d] = %s=%q, want %s=%q", i, fields[i].Key, fields[i].String, w.key, w.value)
		}
	}
	// The returned slice must be usable as a zap field prefix: appending a
	// downstream field yields a 4-field line (proves it is not nil-poisoned).
	line := append(fields, zap.String("extra", "x"))
	if len(line) != 4 {
		t.Fatalf("appended line has %d fields, want 4", len(line))
	}
}

func TestCtxFields_partialPopulation(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want int
	}{
		{"trace only", WithTraceID(context.Background(), "tr"), 1},
		{"request only", WithRequestID(context.Background(), "rq"), 1},
		{"actor only", WithActor(context.Background(), "u"), 1},
		{"empty trace", WithTraceID(context.Background(), ""), 0},
		{"empty request", WithRequestID(context.Background(), ""), 0},
		{"empty actor", WithActor(context.Background(), ""), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CtxFields(tc.ctx); len(got) != tc.want {
				t.Fatalf("fields = %d, want %d (got %+v)", len(got), tc.want, got)
			}
		})
	}
}

// TestCtxFields_isolatedFromServiceCarrier pins the architectural reason the
// carrier cannot live in internal/service: infra layers (outbox, event,
// connector, region, sso) are banned from importing internal/service, so the
// log-correlation read API must be independent. Populating one carrier must
// never leak into the other.
func TestCtxFields_isolatedFromServiceCarrier(t *testing.T) {
	svcCtx := service.WithActor(
		service.WithRequestID(service.WithTraceID(context.Background(), "svc-tr"), "svc-rq"),
		"svc-u")
	if got := CtxFields(svcCtx); len(got) != 0 {
		t.Fatalf("CtxFields sees the service carrier: %d fields, want 0", len(got))
	}

	logCtx := WithActor(WithRequestID(WithTraceID(context.Background(), "lg-tr"), "lg-rq"), "lg-u")
	if got := service.TraceIDFrom(logCtx); got != "" {
		t.Fatalf("service.TraceIDFrom sees the logger carrier: %q", got)
	}
	if got := service.RequestIDFrom(logCtx); got != "" {
		t.Fatalf("service.RequestIDFrom sees the logger carrier: %q", got)
	}
	if got, ok := service.ActorFrom(logCtx); ok {
		t.Fatalf("service.ActorFrom sees the logger carrier: %q", got)
	}
}

// TestNew_unchanged guards against accidental New signature or behavior churn
// while the carrier is added.
func TestNew_unchanged(t *testing.T) {
	l, err := New("info", "json")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if l == nil {
		t.Fatal("nil logger")
	}
}
