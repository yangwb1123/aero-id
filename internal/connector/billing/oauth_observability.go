package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/pkg/metrics"
)

const (
	billingTokenFlightTracer    = "github.com/aero/aero-id/internal/connector/billing" //nolint:gosec // fixed tracer name, not a credential
	billingTokenFlightSpan      = "billing.oauth.token_flight"                         //nolint:gosec // fixed span name, not a credential
	billingTokenFlightEvent     = "billing.oauth.token_wait"                           //nolint:gosec // fixed event name, not a credential
	billingTokenFlightMetricSrc = "billing"
	billingTokenFlightMetricOp  = "oauth_token" //nolint:gosec // fixed metric operation, not a credential
)

// A flight records one bounded source observation; it never puts a trace,
// request, or credential identifier into a metric label.
// tokenFlightMetrics is an optional registry pointer. Keeping this as a
// concrete pointer makes a nil registry unambiguous; it also prevents a typed
// nil interface from reaching the metrics implementation.
type tokenFlightMetrics = *metrics.Registry

// tokenFlightCorrelation is the only caller data admitted to a shared flight.
// SpanContext is copied field-by-field below so vendor TraceState and all other
// context values (including baggage and credentials) are not retained.
type tokenFlightCorrelation struct {
	spanContext oteltrace.SpanContext
}

type tokenFlightResult struct {
	snapshot    billingTokenSnapshot
	flightID    string
	spanContext oteltrace.SpanContext
}

func tokenFlightCorrelationFromContext(ctx context.Context) tokenFlightCorrelation {
	if ctx == nil {
		return tokenFlightCorrelation{}
	}
	spanContext := oteltrace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return tokenFlightCorrelation{}
	}
	return tokenFlightCorrelation{spanContext: oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: spanContext.TraceID(), SpanID: spanContext.SpanID(),
		TraceFlags: spanContext.TraceFlags(), Remote: spanContext.IsRemote(),
	})}
}

func startTokenFlightSpan(correlation tokenFlightCorrelation, scope string, epoch uint64) (context.Context, oteltrace.Span, string) {
	flightID := idgen.NewID().String()
	attributes := []attribute.KeyValue{
		attribute.String("billing.oauth.flight_id", flightID),
		attribute.String("billing.oauth.scope", scope),
		attribute.String("billing.oauth.epoch", strconv.FormatUint(epoch, 10)),
	}
	options := []oteltrace.SpanStartOption{
		oteltrace.WithNewRoot(),
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(attributes...),
	}
	if correlation.spanContext.IsValid() {
		options = append(options, oteltrace.WithLinks(oteltrace.Link{SpanContext: correlation.spanContext}))
	}
	ctx, span := otel.Tracer(billingTokenFlightTracer).Start(context.Background(), billingTokenFlightSpan, options...)
	return ctx, span, flightID
}

// runTokenFlightWithCorrelation owns both the detached flight lifetime and its
// telemetry. The span is a new root because no one request owns a coalesced
// flight; the initiating caller is represented by a link rather than a parent.
// Every value captured by the DoChan closure is an allow-listed scalar or this
// immutable correlation snapshot, never the caller context itself.
func (source *tokenSource) runTokenFlightWithCorrelation(scope string, epoch uint64,
	correlation tokenFlightCorrelation) (result any, resultErr error) {
	started := time.Now()
	timeout := source.flightTimeout
	if timeout <= 0 {
		timeout = defaultBillingTimeout
	}
	flightCtx, span, flightID := startTokenFlightSpan(correlation, scope, epoch)
	flightSpanContext := span.SpanContext()
	flightCtx, cancel := context.WithTimeout(flightCtx, timeout)
	defer cancel()

	outcome := "unknown"
	remoteCall := false
	defer func() {
		if outcome == "unknown" {
			outcome = tokenFlightOutcome(resultErr)
		}
		span.SetAttributes(attribute.String("billing.oauth.outcome", outcome))
		if resultErr != nil {
			// Use the fixed outcome, not the dependency error, as the status
			// description. Error strings may contain endpoint-specific data.
			span.SetStatus(otelcodes.Error, outcome)
		}
		span.End()
		if remoteCall && source.metrics != nil {
			source.metrics.ObserveSourceCall(billingTokenFlightMetricSrc, billingTokenFlightMetricOp,
				time.Since(started), resultErr)
		}
	}()

	if snapshot, ok := source.cachedSnapshot(scope); ok {
		outcome = "cache_hit"
		return tokenFlightResult{snapshot: snapshot, flightID: flightID, spanContext: flightSpanContext}, nil
	}
	remoteCall = true
	snapshot, err := source.fetchSnapshotAtEpoch(flightCtx, scope, epoch)
	if errors.Is(err, errTokenFlightExpired) {
		outcome = "timeout"
		return tokenFlightResult{flightID: flightID, spanContext: flightSpanContext},
			fmt.Errorf("%w: token transport", ErrUnavailable)
	}
	if errors.Is(err, errEpochDiscarded) {
		outcome = "epoch_discarded"
	} else if err != nil {
		outcome = "error"
	} else {
		outcome = "success"
	}
	return tokenFlightResult{snapshot: snapshot, flightID: flightID, spanContext: flightSpanContext}, err
}

func tokenFlightOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, errEpochDiscarded):
		return "epoch_discarded"
	case errors.Is(err, errTokenFlightExpired):
		return "timeout"
	case errors.Is(err, ErrUnavailable):
		return "unavailable"
	default:
		return "error"
	}
}

// injectTokenFlightTraceContext intentionally uses TraceContext directly rather
// than the process-wide composite propagator. The OAuth request gets only the
// detached flight's W3C traceparent; baggage and arbitrary request metadata are
// never sent to the credential endpoint.
func injectTokenFlightTraceContext(ctx context.Context, request *http.Request) {
	if ctx == nil || request == nil {
		return
	}
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(request.Header))
}

func tokenFlightResultFromValue(value any) (tokenFlightResult, bool) {
	flight, ok := value.(tokenFlightResult)
	if ok {
		return flight, true
	}
	// Keep the decoder tolerant of the context-free result used by older
	// package helpers and tests.
	snapshot, ok := value.(billingTokenSnapshot)
	return tokenFlightResult{snapshot: snapshot}, ok
}

// recordTokenWaitForFlight annotates the caller's own span without copying its
// context into the shared flight. Scope and epoch are connector-controlled
// values; the event has no actor, tenant, request ID, baggage, token, or error
// text. A completed shared result also links the caller to that flight span.
func recordTokenWaitForFlight(ctx context.Context, scope string, epoch uint64, outcome string,
	flight tokenFlightResult, hasFlight bool) {
	if ctx == nil {
		return
	}
	span := oteltrace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	if hasFlight && flight.spanContext.IsValid() {
		span.AddLink(oteltrace.Link{SpanContext: flight.spanContext})
	}
	attributes := []attribute.KeyValue{attribute.String("billing.oauth.wait_outcome", outcome)}
	if scope != "" {
		attributes = append(attributes,
			attribute.String("billing.oauth.scope", scope),
			attribute.String("billing.oauth.epoch", strconv.FormatUint(epoch, 10)))
	}
	if hasFlight && flight.flightID != "" {
		attributes = append(attributes, attribute.String("billing.oauth.flight_id", flight.flightID))
	}
	span.AddEvent(billingTokenFlightEvent, oteltrace.WithAttributes(attributes...))
}

func tokenWaitOutcome(err error) string {
	if errors.Is(err, errEpochDiscarded) {
		return "epoch_discarded"
	}
	if errors.Is(err, ErrUnavailable) {
		return "unavailable"
	}
	return "error"
}
