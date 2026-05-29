package metrics_test

import (
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

func TestRegistry_UnmatchedRouteLabel(t *testing.T) {
	r := metrics.NewRegistry()
	r.ObserveHTTP("GET", "", 404, time.Millisecond) // no matched template
	var b strings.Builder
	r.WriteText(&b)
	if !strings.Contains(b.String(), `route="unmatched"`) {
		t.Errorf("empty route should be labelled \"unmatched\":\n%s", b.String())
	}
}
