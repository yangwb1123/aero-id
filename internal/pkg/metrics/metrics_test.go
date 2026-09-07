package metrics_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/pkg/metrics"
)

func TestRegistry_HTTPExposition(t *testing.T) {
	r := metrics.NewRegistry()
	r.ObserveHTTP("GET", "/v1/files/:id", 200, 30*time.Millisecond)
	r.ObserveHTTP("GET", "/v1/files/:id", 200, 12*time.Millisecond)
	r.ObserveHTTP("POST", "/v1/files", 500, 5*time.Millisecond)

	var b strings.Builder
	r.WriteText(&b)
	out := b.String()

	for _, want := range []string{
		`# TYPE aeroid_requests_total counter`,
		`aeroid_requests_total{transport="http",method="GET",route="/v1/files/:id",status="200"} 2`,
		`aeroid_requests_total{transport="http",method="POST",route="/v1/files",status="500"} 1`,
		`# TYPE aeroid_request_duration_seconds histogram`,
		`aeroid_request_duration_seconds_count{transport="http",route="/v1/files/:id"} 2`,
		`aeroid_request_duration_seconds_bucket{transport="http",route="/v1/files/:id",le="+Inf"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing line:\n  %s\n--- full output ---\n%s", want, out)
		}
	}
}

func TestRegistry_GRPCExposition(t *testing.T) {
	r := metrics.NewRegistry()
	r.ObserveGRPC("/aero.v1.FileService/GetFile", "OK", 2*time.Millisecond)
	r.ObserveGRPC("/aero.v1.FileService/GetFile", "NotFound", 1*time.Millisecond)

	var b strings.Builder
	r.WriteText(&b)
	out := b.String()

	if !strings.Contains(out, `aeroid_requests_total{transport="grpc",method="rpc",route="/aero.v1.FileService/GetFile",status="OK"} 1`) {
		t.Errorf("missing grpc OK counter:\n%s", out)
	}
	if !strings.Contains(out, `status="NotFound"`) {
		t.Errorf("missing grpc NotFound counter:\n%s", out)
	}
}

func TestRegistry_SSOExposition(t *testing.T) {
	r := metrics.NewRegistry()
	r.ObserveSSOCache("token", true)
	r.ObserveSSOCache("token", false)
	r.ObserveSSOCache("user", true)
	r.ObserveSSOCall("validate", 3*time.Millisecond, true)
	r.ObserveSSOCall("validate", 2*time.Millisecond, false) // an error
	r.ObserveSSOCall("get_users", 1*time.Millisecond, true)

	var b strings.Builder
	r.WriteText(&b)
	out := b.String()

	for _, want := range []string{
		`# TYPE aeroid_sso_cache_hits_total counter`,
		`aeroid_sso_cache_hits_total{cache="token"} 1`,
		`aeroid_sso_cache_misses_total{cache="token"} 1`,
		`aeroid_sso_cache_hits_total{cache="user"} 1`,
		`# TYPE aeroid_sso_errors_total counter`,
		`aeroid_sso_errors_total{op="validate"} 1`,
		`# TYPE aeroid_sso_call_duration_seconds histogram`,
		`aeroid_sso_call_duration_seconds_count{op="validate"} 2`,
		`aeroid_sso_call_duration_seconds_count{op="get_users"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing line:\n  %s\n--- full output ---\n%s", want, out)
		}
	}
}

func TestRegistry_OutboxUnknownBeforeFirstKnownObservation(t *testing.T) {
	r := metrics.NewRegistry()
	r.ObserveOutboxDrain(42, 0, time.Hour, time.Millisecond, false)
	var b strings.Builder
	r.WriteText(&b)
	out := b.String()
	if strings.Contains(out, "aeroid_outbox_pending") || strings.Contains(out, "aeroid_outbox_oldest_age_seconds") {
		t.Fatalf("unknown first observation fabricated backlog/age values:\n%s", out)
	}
	for _, want := range []string{
		`aeroid_outbox_claimed 0`,
		`aeroid_outbox_eligibility_known 0`,
		`aeroid_outbox_drain_passes_total 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unknown observation missing %q:\n%s", want, out)
		}
	}
}

func TestRegistry_OutboxExposition(t *testing.T) {
	r := metrics.NewRegistry()
	r.ObserveOutboxPublished("message.sent")
	r.ObserveOutboxPublished("message.sent")
	r.ObserveOutboxFailed("comment.replied")
	r.ObserveOutboxDeadLetter("comment.replied")
	r.ObserveOutboxClaimError()
	r.ObserveOutboxClaimLost()
	r.ObserveOutboxFinalizationError()
	r.ObserveOutboxRenewalError()
	r.ObserveOutboxDrain(3, 2, 90*time.Second, 4*time.Millisecond, true)
	// An eligibility stats outage must not erase the last known backlog/lag,
	// while claimed and known still reflect the current drain.
	r.ObserveOutboxDrain(999, 1, 0, 5*time.Millisecond, false)

	var b strings.Builder
	r.WriteText(&b)
	out := b.String()

	for _, want := range []string{
		`# TYPE aeroid_outbox_published_total counter`,
		`aeroid_outbox_published_total{event_type="message.sent"} 2`,
		`# TYPE aeroid_outbox_failed_total counter`,
		`aeroid_outbox_failed_total{event_type="comment.replied"} 1`,
		`aeroid_outbox_dead_letter_total{event_type="comment.replied"} 1`,
		`aeroid_outbox_claim_errors_total 1`,
		`aeroid_outbox_claim_lost_total 1`,
		`aeroid_outbox_finalization_errors_total 1`,
		`aeroid_outbox_renewal_errors_total 1`,
		`# TYPE aeroid_outbox_pending gauge`,
		`aeroid_outbox_pending 3`,
		`# TYPE aeroid_outbox_claimed gauge`,
		`aeroid_outbox_claimed 1`,
		`# TYPE aeroid_outbox_eligibility_known gauge`,
		`aeroid_outbox_eligibility_known 0`,
		`# TYPE aeroid_outbox_oldest_age_seconds gauge`,
		`aeroid_outbox_oldest_age_seconds 90`,
		`# TYPE aeroid_outbox_drain_duration_seconds histogram`,
		`aeroid_outbox_drain_duration_seconds_count 2`,
		`aeroid_outbox_drain_duration_seconds_bucket{le="+Inf"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing line:\n  %s\n--- full output ---\n%s", want, out)
		}
	}
}

func TestRegistry_UnmatchedRouteLabel(t *testing.T) {
	r := metrics.NewRegistry()
	r.ObserveHTTP("GET", "", 404, time.Millisecond) // no matched template
	var b strings.Builder
	r.WriteText(&b)
	if !strings.Contains(b.String(), `route="unmatched"`) {
		t.Errorf("empty route should be labelled \"unmatched\":\n%s", b.String())
	}
}

func TestSourceAuthCompatibilityMetricsAreBounded(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.ObserveSourceAuthCompatibility("aero-im", "loopback")
	reg.ObserveSourceAuthCompatibility("aero-vault", "loopback")
	reg.ObserveSourceAuthCompatibility("snaplink", "loopback")
	reg.ObserveSourceAuthCompatibility("aero-vault", "https://secret.example")

	var buf strings.Builder
	reg.WriteText(&buf)
	out := buf.String()
	for _, want := range []string{
		`aeroid_source_auth_compatibility_total{source="aero-im",mode="loopback"} 1`,
		`aeroid_source_auth_compatibility_total{source="aero-vault",mode="loopback"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "snaplink") || strings.Contains(out, "secret.example") {
		t.Fatalf("metrics exposed an unapproved source or mode: %s", out)
	}
}

func TestControlPlaneMetrics_TextOutput(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.ObserveEventApplied("aero-vault.buckets")
	reg.ObserveEventApplied("aero-vault.buckets")
	reg.ObserveEventQuarantined("aero-im.rooms")
	reg.ObserveOperationOutcome("completed")
	reg.ObserveCallbackOutcome("dead_letter")
	reg.ObserveReconcileDrift("aero-vault")
	reg.ObserveProjectionLag("aero-vault", "aero-vault.buckets", 5)
	reg.ObserveConsumerLag("aero-im", 12)
	reg.ObserveOperationAge("unknown", 90)
	reg.ObserveAuditIngestFailure("aero-id")
	reg.ObserveAuditPublicationOutcome("dead_letter")
	reg.ObserveAuditPublicationOutcome("completed")
	reg.ObserveAuditPublicationDrainSkipped()
	const sensitiveError = "Bearer token-secret https://governance.internal.example:8443/audit"
	reg.ObserveAuditPublicationClaimFailed("claim_tx", errors.New(sensitiveError))
	reg.ObserveAuditPublicationFinishFailure("pending", errors.New(sensitiveError))

	var buf strings.Builder
	reg.WriteText(&buf)
	out := buf.String()
	if strings.Contains(out, sensitiveError) {
		t.Fatalf("metrics exposed dependency error details: %q", sensitiveError)
	}
	for _, want := range []string{
		`aero_control_events_applied_total{dataset="aero-vault.buckets"} 2`,
		`aero_control_events_quarantined_total{dataset="aero-im.rooms"} 1`,
		`aero_control_operations_total{status="completed"} 1`,
		`aero_control_callback_deliveries_total{status="dead_letter"} 1`,
		`aero_control_reconcile_drift_total{source="aero-vault"} 1`,
		`aero_control_audit_ingest_failures_total{source="aero-id"} 1`,
		`aero_control_audit_publication_outcomes_total{status="dead_letter"} 1`,
		`aero_control_audit_publication_outcomes_total{status="completed"} 1`,
		`aero_control_audit_publication_drain_skipped_total 1`,
		`aero_control_audit_publication_claim_failed_total{phase="claim_tx"} 1`,
		`aero_control_audit_publication_finish_failures_total{status="pending"} 1`,
		`aero_control_projection_lag_seconds`,
		`aero_control_consumer_lag_seconds`,
		`aero_control_operation_age_seconds`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}
