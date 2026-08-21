package event

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

type publisherTransport struct {
	request *http.Request
	body    []byte
}

func (t *publisherTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.request = request.Clone(request.Context())
	t.body, _ = io.ReadAll(request.Body)
	return &http.Response{StatusCode: http.StatusAccepted, Status: "202 Accepted",
		Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
}

func replicationEnvelopeForTest() *Envelope {
	accountID := idgen.NewID()
	return &Envelope{EventID: idgen.NewID(), Source: "aero-id", SourceRegion: "west",
		EventType: "projection.snapshot", SchemaVersion: CurrentSchemaVersion, AggregateType: "projection",
		AggregateID: accountID.String(), AggregateVersion: 1, TenantID: "tenant-a", OccurredAt: nowUTC(),
		Payload: map[string]any{"account_id": accountID.String(), "dataset": "aero-vault.buckets",
			"status": "fresh", "version": float64(1), "data": map[string]any{}}}
}

func TestAuthenticatedHTTPPublisherAddsIdentityAndSignature(t *testing.T) {
	transport := &publisherTransport{}
	client := &http.Client{Transport: transport}
	publisher, err := NewAuthenticatedHTTPPublisher("https://region.example/internal/v1/events",
		"replication-hmac-key-00000000000000000000", "service-token", client)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(context.Background(), replicationEnvelopeForTest()); err != nil {
		t.Fatal(err)
	}
	if got := transport.request.Header.Get("Authorization"); got != "Bearer service-token" {
		t.Fatalf("authorization = %q, want bearer service token", got)
	}
	if !strings.HasPrefix(transport.request.Header.Get("X-Aero-Event-Signature"), "sha256=") {
		t.Fatal("event signature header is missing")
	}
	if transport.request.Header.Get("X-Aero-Event-Timestamp") == "" || len(transport.body) == 0 {
		t.Fatal("signed request metadata or body is missing")
	}
}

func TestHTTPPublisherRejectsUnsafeConfiguration(t *testing.T) {
	if _, err := NewAuthenticatedHTTPPublisher("http://remote.example/events", strings.Repeat("k", 32), "token", nil); err == nil {
		t.Fatal("non-loopback HTTP endpoint was accepted")
	}
	if _, err := NewAuthenticatedHTTPPublisher("https://remote.example/events", strings.Repeat("k", 32), "", nil); err == nil {
		t.Fatal("empty bearer token was accepted")
	}
	if _, err := NewAuthenticatedHTTPPublisher("https://remote.example/events", "short", "token", nil); err == nil {
		t.Fatal("short signing key was accepted")
	}
	if _, err := NewAuthenticatedHTTPPublisher("https://remote.example/events", strings.Repeat("k", 32), "token\x00", nil); err == nil {
		t.Fatal("control character in bearer token was accepted")
	}
}

func nowUTC() (result time.Time) {
	return time.Now().UTC()
}
