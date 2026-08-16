package logger

import (
	"context"

	"go.uber.org/zap"
)

// ctxKey is an unexported context key type so callers cannot collide with us,
// mirroring internal/service's key discipline. The carrier is deliberately
// independent of internal/service's carrier: infra layers (outbox, event,
// connector, region, sso) are banned from importing internal/service
// (scripts/check-architecture.sh rule d), so the log-correlation read API must
// live here where it is importable everywhere.
type ctxKey int

const traceIDKey ctxKey = iota

const requestIDKey ctxKey = iota + 1

const actorKey ctxKey = iota + 2

// WithTraceID returns a child context carrying the trace id for log correlation.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// WithRequestID returns a child context carrying the request id for log
// correlation.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// WithActor returns a child context carrying the authenticated actor uid for
// log correlation.
func WithActor(ctx context.Context, actorUID string) context.Context {
	return context.WithValue(ctx, actorKey, actorUID)
}

// CtxFields renders the correlation fields stored on ctx in the stable order
// trace_id, request_id, actor_uid. Empty values are omitted, so an unpopulated
// carrier yields zero fields (background workers, unsampled requests). Pass the
// result through append(...) to add downstream fields to a log line.
func CtxFields(ctx context.Context) []zap.Field {
	fields := make([]zap.Field, 0, 3)
	if traceID, _ := ctx.Value(traceIDKey).(string); traceID != "" {
		fields = append(fields, zap.String("trace_id", traceID))
	}
	if requestID, _ := ctx.Value(requestIDKey).(string); requestID != "" {
		fields = append(fields, zap.String("request_id", requestID))
	}
	if actor, _ := ctx.Value(actorKey).(string); actor != "" {
		fields = append(fields, zap.String("actor_uid", actor))
	}
	return fields
}
