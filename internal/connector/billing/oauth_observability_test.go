package billing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/aero/aero-id/internal/pkg/metrics"
)

type billingObservabilityContextKey struct{}

func TestBillingTokenFlightUsesSafeTraceLinkAndTraceOnlyPropagation(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	previousProvider := otel.GetTracerProvider()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(recorder),
	)
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previousProvider)
	})

	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		headers <- request.Header.Clone()
		writeJSON(t, writer, http.StatusOK, billingTokenResponse("observed-token"))
	}))
	defer server.Close()

	callerContext, callerSpan := otel.Tracer("billing-observability-test").Start(
		context.Background(), "request")
	defer callerSpan.End()
	member, err := baggage.NewMember("secret-baggage", "must-not-forward")
	if err != nil {
		t.Fatal(err)
	}
	bag, err := baggage.New(member)
	if err != nil {
		t.Fatal(err)
	}
	callerContext = baggage.ContextWithBaggage(callerContext, bag)
	callerContext = context.WithValue(callerContext, billingObservabilityContextKey{}, "credential-like-value")

	baseClient := server.Client()
	transport := &billingObservabilityTransport{base: baseClient.Transport}
	source := newBillingOAuthTestSource(t, server.URL+"/token", time.Second,
		&http.Client{Transport: transport})
	token, err := source.accessToken(callerContext, ScopeEntitlementRead)
	if err != nil || token != "observed-token" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
	if transport.leaked.Load() {
		t.Fatal("detached OAuth context retained an arbitrary caller context value")
	}
	// End the caller span before inspecting the recorder; the wait event is
	// emitted on that span while the shared flight is running.
	callerSpan.End()

	var requestHeaders http.Header
	select {
	case requestHeaders = <-headers:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for token request headers")
	}
	if got := requestHeaders.Get("baggage"); got != "" {
		t.Fatalf("OAuth request propagated baggage %q", got)
	}
	traceContext := propagation.TraceContext{}.Extract(context.Background(), propagation.HeaderCarrier(requestHeaders))
	requestSpanContext := oteltrace.SpanContextFromContext(traceContext)
	if !requestSpanContext.IsValid() {
		t.Fatal("OAuth request did not carry a valid detached trace context")
	}

	var flightSpan sdktrace.ReadOnlySpan
	var callerRecorded sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		switch span.Name() {
		case billingTokenFlightSpan:
			flightSpan = span
		case "request":
			callerRecorded = span
		}
	}
	if flightSpan == nil {
		t.Fatal("detached token-flight span was not recorded")
	}
	if flightSpan.Parent().IsValid() {
		t.Fatalf("token-flight span has a parent %s; want a new root", flightSpan.Parent().SpanID())
	}
	flightContext := flightSpan.SpanContext()
	if requestSpanContext.TraceID() != flightContext.TraceID() || requestSpanContext.SpanID() != flightContext.SpanID() ||
		requestSpanContext.TraceFlags() != flightContext.TraceFlags() {
		t.Fatalf("request trace context = %s/%s, flight span = %s/%s; want exact flight context",
			requestSpanContext.TraceID(), requestSpanContext.SpanID(), flightContext.TraceID(), flightContext.SpanID())
	}
	links := flightSpan.Links()
	if len(links) != 1 {
		t.Fatalf("flight links = %d, want one safe caller link", len(links))
	}
	if links[0].SpanContext.TraceID() != callerSpan.SpanContext().TraceID() ||
		links[0].SpanContext.SpanID() != callerSpan.SpanContext().SpanID() {
		t.Fatalf("flight link = %s/%s, want caller %s/%s", links[0].SpanContext.TraceID(), links[0].SpanContext.SpanID(),
			callerSpan.SpanContext().TraceID(), callerSpan.SpanContext().SpanID())
	}
	if links[0].SpanContext.TraceState().Len() != 0 {
		t.Fatal("flight link retained caller TraceState")
	}
	if value, ok := billingSpanStringAttribute(flightSpan, "billing.oauth.flight_id"); !ok || value == "" {
		t.Fatal("flight span has no generated flight_id")
	}
	if value, ok := billingSpanStringAttribute(flightSpan, "billing.oauth.outcome"); !ok || value != "success" {
		t.Fatalf("flight outcome = %q/%v, want success", value, ok)
	}
	if callerRecorded == nil || !billingSpanHasEvent(callerRecorded, billingTokenFlightEvent) {
		t.Fatal("caller span has no token-wait correlation event")
	}
	for _, span := range recorder.Ended() {
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.AsString(), "credential-like-value") ||
				strings.Contains(attr.Value.AsString(), "must-not-forward") {
				t.Fatalf("span attribute %q retained caller context data", attr.Key)
			}
		}
	}
}

func TestBillingTokenFlightMetricsAreSharedAndBounded(t *testing.T) {
	registry := metrics.NewRegistry()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(t, writer, http.StatusOK, billingTokenResponse("metric-token"))
	}))
	defer server.Close()

	source, err := newTokenSource(oauthConfig{
		TokenURL: server.URL + "/token", ClientID: "client", ClientSecret: "secret",
		Scope: ScopeEntitlementRead + " " + ScopeMeteringWrite, Resources: []string{billingResource},
		Timeout: time.Second, AllowHTTP: true, metrics: registry,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.accessToken(context.Background(), ScopeEntitlementRead); err != nil {
		t.Fatal(err)
	}

	var exposition strings.Builder
	registry.WriteText(&exposition)
	output := exposition.String()
	want := `aeroid_source_calls_total{source="billing",operation="oauth_token"} 1`
	if !strings.Contains(output, want) {
		t.Fatalf("metrics missing one shared OAuth observation %q:\n%s", want, output)
	}
	for _, forbidden := range []string{"client", "secret", "metric-token", "trace_id", "request_id", "flight_id"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("metrics exposed forbidden correlation or credential value %q:\n%s", forbidden, output)
		}
	}
}

type billingObservabilityTransport struct {
	base   http.RoundTripper
	leaked atomic.Bool
}

func (transport *billingObservabilityTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Context().Value(billingObservabilityContextKey{}) != nil {
		transport.leaked.Store(true)
	}
	return transport.base.RoundTrip(request)
}

func billingSpanStringAttribute(span sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value.AsString(), true
		}
	}
	return "", false
}

func billingSpanHasEvent(span sdktrace.ReadOnlySpan, name string) bool {
	for _, event := range span.Events() {
		if event.Name == name {
			return true
		}
	}
	return false
}
