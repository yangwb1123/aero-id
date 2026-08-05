package middleware

import (
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/aero/aero-id/internal/service"
)

// tracerName identifies aero-id's HTTP instrumentation scope.
const tracerName = "github.com/aero/aero-id/internal/middleware"

// Tracing starts a server span per request and makes it the parent for everything
// downstream (handlers, the SSO hop). It extracts any inbound W3C trace context so a
// caller's trace continues into aero-id, correlates the span with the existing
// request_id (as a span attribute and a trace_id response header), and records the
// HTTP status. It relies on the global propagator/tracer installed by pkg/tracing, so
// it degrades to a cheap no-op span when tracing is disabled.
//
// Place it AFTER RequestID (so request_id is available to tag the span) and before
// the rest of the chain so the span wraps auth, metrics, and the handler.
func Tracing() gin.HandlerFunc {
	tracer := otel.Tracer(tracerName)
	propagator := otel.GetTextMapPropagator()
	return func(c *gin.Context) {
		ctx := propagator.Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))

		route := c.FullPath()
		if route == "" {
			route = c.Request.URL.Path
		}
		ctx, span := tracer.Start(ctx, c.Request.Method+" "+route,
			oteltrace.WithSpanKind(oteltrace.SpanKindServer),
			oteltrace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(c.Request.Method),
				semconv.HTTPRoute(route),
				semconv.URLPath(c.Request.URL.Path),
			),
		)
		defer span.End()

		if rid := c.GetString("request_id"); rid != "" {
			span.SetAttributes(attribute.String("request_id", rid))
		}
		// Surface the trace id so logs/clients can stitch a request to its trace.
		if sc := span.SpanContext(); sc.HasTraceID() {
			c.Writer.Header().Set("X-Trace-ID", sc.TraceID().String())
			c.Set("trace_id", sc.TraceID().String())
			ctx = service.WithTraceID(ctx, sc.TraceID().String())
		}

		c.Request = c.Request.WithContext(ctx)
		c.Next()

		status := c.Writer.Status()
		span.SetAttributes(semconv.HTTPResponseStatusCode(status))
		if status >= 500 {
			span.SetStatus(codes.Error, "")
		}
	}
}
