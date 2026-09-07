package event

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	timestamp := transport.request.Header.Get("X-Aero-Event-Timestamp")
	provided := transport.request.Header.Get("X-Aero-Event-Signature")
	if !strings.HasPrefix(provided, "sha256=") || timestamp == "" || len(transport.body) == 0 {
		t.Fatal("signed request metadata or body is missing")
	}
	mac := hmac.New(sha256.New, []byte("replication-hmac-key-00000000000000000000"))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(transport.body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(provided), []byte(want)) {
		t.Fatalf("signature does not cover the transmitted body: got %q want %q", provided, want)
	}
}

func TestHTTPPublisherRejectsUnserializablePayloadWithoutRequest(t *testing.T) {
	transport := &publisherTransport{}
	publisher, err := NewHTTPPublisher("https://region.example/internal/v1/events",
		"replication-hmac-key-00000000000000000000", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	const marker = "http-marshal-failure-value"
	envelope := serializationTestEnvelope()
	envelope.Payload["unserializable"] = serializationFailureValue{secret: marker}
	if err := publisher.Publish(context.Background(), envelope); err == nil {
		t.Fatal("unserializable payload was accepted")
	} else if strings.Contains(err.Error(), marker) {
		t.Fatalf("marshal error leaked source payload: %v", err)
	}
	if transport.request != nil {
		t.Fatal("HTTP publisher sent a request after serialization failed")
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
