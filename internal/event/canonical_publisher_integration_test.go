//go:build integration

package event_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

const publisherCanonicalSubject = "aero.events.canonical"

func TestNATSPublisherRedactsAtJetStreamBoundary(t *testing.T) {
	_, url := startNATS(t)
	createStream(t, url, "aero_events", publisherCanonicalSubject)
	sub := captureJetStream(t, url, publisherCanonicalSubject)
	publisher := newPublisher(t, url)

	envelope := publisherIntegrationEnvelope(idgen.NewID())
	data := envelope.Payload["data"].(map[string]any)
	data["authorization"] = "Bearer nats-wire-secret"
	data["nested"] = []any{map[string]any{
		"token": "nested-nats-token-secret", "safe_nested": "safe-nested-value",
	}}
	data["object_data"] = "object-bytes-secret"
	data["message_content"] = "message-content-secret"
	before, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}

	if err := publisher.Publish(t.Context(), envelope); err != nil {
		t.Fatalf("publish: %v", err)
	}
	raw := nextMessage(t, sub)
	assertRedactedWirePayload(t, raw)
	expected, err := json.Marshal(event.BoundedEnvelope(envelope))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, expected) {
		t.Fatalf("NATS payload differs from bounded serialization:\n got %s\nwant %s", raw, expected)
	}
	after, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("publisher mutated the input envelope:\n before %s\n after %s", before, after)
	}
}

func TestNATSPublisherRejectsMarshalFailureWithoutPublish(t *testing.T) {
	_, url := startNATS(t)
	createStream(t, url, "aero_events", publisherCanonicalSubject)
	sub := captureJetStream(t, url, publisherCanonicalSubject)
	publisher := newPublisher(t, url)
	envelope := publisherIntegrationEnvelope(idgen.NewID())
	envelope.OccurredAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)

	if err := publisher.Publish(t.Context(), envelope); err == nil {
		t.Fatal("out-of-range timestamp was accepted")
	} else if strings.Contains(err.Error(), "10000") || strings.Contains(err.Error(), "year") {
		t.Fatalf("marshal error exposed serialization details: %v", err)
	}
	if _, err := sub.NextMsg(750 * time.Millisecond); err == nil {
		t.Fatal("NATS publisher sent a message after serialization failed")
	}
}

func TestHTTPAndNATSPublisherUseIdenticalWireBody(t *testing.T) {
	_, url := startNATS(t)
	createStream(t, url, "aero_events", publisherCanonicalSubject)
	sub := captureJetStream(t, url, publisherCanonicalSubject)
	transport := &captureHTTPTransport{}
	const signingKey = "publisher-parity-signing-key-0123456789"
	httpPublisher, err := event.NewAuthenticatedHTTPPublisher(
		"https://region.example/internal/v1/events", signingKey, "service-token",
		&http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	envelope := publisherIntegrationEnvelope(idgen.NewID())
	if err := httpPublisher.Publish(t.Context(), envelope); err != nil {
		t.Fatalf("HTTP publish: %v", err)
	}
	if transport.request == nil || len(transport.body) == 0 {
		t.Fatal("HTTP publisher did not capture a request body")
	}
	assertHTTPBodySignature(t, transport.request, transport.body, signingKey)

	natsPublisher := newPublisher(t, url)
	if err := natsPublisher.Publish(t.Context(), envelope); err != nil {
		t.Fatalf("NATS publish: %v", err)
	}
	natsBody := nextMessage(t, sub)
	if !bytes.Equal(transport.body, natsBody) {
		t.Fatalf("HTTP and NATS bodies differ:\n HTTP %s\nNATS %s", transport.body, natsBody)
	}
}

func TestNATSPublisherDuplicateDeliveryUsesPostgresState(t *testing.T) {
	fixture := newPublisherIngestFixture(t, "publisher-duplicate")
	envelope := publisherIntegrationEnvelope(fixture.account.ID)

	if err := fixture.publisher.Publish(t.Context(), envelope); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if err := fixture.publisher.Publish(t.Context(), envelope); err != nil {
		t.Fatalf("duplicate publish: %v", err)
	}
	waitForSuccessfulDeliveries(t, fixture.outcomes, 2)
	waitForStatus(t, fixture.statuses, string(repository.EventDuplicate))
	if fixture.attempts.Load() < 2 {
		t.Fatalf("handler attempts = %d, want at least two deliveries", fixture.attempts.Load())
	}
	assertEventStateCounts(t, fixture.db, envelope, 1)
}

func TestNATSPublisherSafeContentConflictIsDeadLettered(t *testing.T) {
	fixture := newPublisherIngestFixture(t, "publisher-integrity")
	sub := captureJetStream(t, fixture.url, publisherCanonicalSubject)
	deadLetters := captureJetStream(t, fixture.url, publisherCanonicalSubject+".dead-letter")
	first := publisherIntegrationEnvelope(fixture.account.ID)
	first.Payload["data"].(map[string]any)["safe_field"] = "before"

	firstHash, err := event.CanonicalContentHash(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.publisher.Publish(t.Context(), first); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	firstWire := nextMessage(t, sub)
	waitForSuccessfulDeliveries(t, fixture.outcomes, 1)

	second := clonePublisherEnvelope(first)
	second.Payload["data"].(map[string]any)["safe_field"] = "after"
	secondHash, err := event.CanonicalContentHash(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash == secondHash {
		t.Fatal("changing a safe field did not change the canonical content hash")
	}
	if err := fixture.publisher.Publish(t.Context(), second); err != nil {
		t.Fatalf("changed-content publish: %v", err)
	}
	secondWire := nextMessage(t, sub)
	if bytes.Equal(firstWire, secondWire) {
		t.Fatal("changing a safe field did not change the NATS payload")
	}
	waitForErrorCode(t, fixture.outcomes, "event.integrity_conflict")

	var record map[string]any
	if err := json.Unmarshal(nextMessage(t, deadLetters), &record); err != nil {
		t.Fatalf("decode dead-letter record: %v", err)
	}
	if record["reason"] != "handler_rejected" {
		t.Fatalf("dead-letter reason = %#v, want handler_rejected", record["reason"])
	}
	if record["event_id"] != first.EventID.String() {
		t.Fatalf("dead-letter event_id = %#v, want %s", record["event_id"], first.EventID)
	}
	if _, err := deadLetters.NextMsg(750 * time.Millisecond); err == nil {
		t.Fatal("changed content produced more than one dead-letter record")
	}
	assertEventStateCounts(t, fixture.db, first, 1)
	stored, _, err := fixture.store.ListByAccount(t.Context(), first.TenantID, first.Source,
		fixture.account.ID.String(), domain.ID{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Payload["data"].(map[string]any)["safe_field"] != "before" {
		t.Fatalf("projection changed after integrity conflict: %#v", stored)
	}
}

func newPublisher(t *testing.T, url string) *event.NATSPublisher {
	t.Helper()
	publisher, err := event.NewNATSPublisher(url, publisherCanonicalSubject)
	if err != nil {
		t.Fatalf("new NATS publisher: %v", err)
	}
	t.Cleanup(func() { _ = publisher.Close() })
	return publisher
}

func captureJetStream(t *testing.T, url, subject string) *nats.Subscription {
	t.Helper()
	conn, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("capture NATS connect: %v", err)
	}
	js, err := conn.JetStream()
	if err != nil {
		conn.Close()
		t.Fatalf("capture JetStream: %v", err)
	}
	sub, err := js.SubscribeSync(subject)
	if err != nil {
		conn.Close()
		t.Fatalf("capture subscription: %v", err)
	}
	if err := conn.Flush(); err != nil {
		_ = sub.Unsubscribe()
		conn.Close()
		t.Fatalf("flush capture subscription: %v", err)
	}
	t.Cleanup(func() {
		_ = sub.Unsubscribe()
		conn.Close()
	})
	return sub
}

func nextMessage(t *testing.T, sub *nats.Subscription) []byte {
	t.Helper()
	message, err := sub.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("next JetStream message: %v", err)
	}
	return append([]byte(nil), message.Data...)
}

func assertRedactedWirePayload(t *testing.T, raw []byte) {
	t.Helper()
	text := string(raw)
	for _, value := range []string{
		"nats-wire-secret", "nested-nats-token-secret", "object-bytes-secret", "message-content-secret",
	} {
		if strings.Contains(text, value) {
			t.Fatalf("NATS payload leaked %q: %s", value, raw)
		}
	}
	for _, key := range []string{"authorization", "token", "object_data", "message_content"} {
		if strings.Contains(strings.ToLower(text), `"`+key+`"`) {
			t.Fatalf("NATS payload retained sensitive key %q: %s", key, raw)
		}
	}
	var envelope event.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode NATS envelope: %v", err)
	}
	if envelope.EventID == (domain.ID{}) || envelope.Payload["account_id"] == nil {
		t.Fatalf("required envelope identity is missing: %#v", envelope)
	}
	data, ok := envelope.Payload["data"].(map[string]any)
	if !ok || data["safe_field"] != "before" || !strings.Contains(string(raw), "safe-nested-value") {
		t.Fatalf("safe nested value was not retained: %#v", envelope.Payload)
	}
}

func assertHTTPBodySignature(t *testing.T, request *http.Request, body []byte, key string) {
	t.Helper()
	timestamp := request.Header.Get("X-Aero-Event-Timestamp")
	provided := request.Header.Get("X-Aero-Event-Signature")
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(provided), []byte(want)) {
		t.Fatalf("HTTP signature does not cover the transmitted body: got %q want %q", provided, want)
	}
}

func publisherIntegrationEnvelope(accountID domain.ID) *event.Envelope {
	return &event.Envelope{
		EventID: idgen.NewID(), Source: "aero-id", SourceRegion: "west",
		EventType: "projection.snapshot", SchemaVersion: event.CurrentSchemaVersion,
		AggregateType: "projection", AggregateID: accountID.String(), AggregateVersion: 1,
		TenantID: "tenant-a", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{
			"account_id": accountID.String(), "dataset": "aero-vault.buckets",
			"status": "fresh", "version": float64(1),
			"safe": "safe-value", "data": map[string]any{"safe_field": "before", "usage_bytes": float64(10)},
		},
	}
}

func clonePublisherEnvelope(source *event.Envelope) *event.Envelope {
	copy := *source
	copy.Payload = clonePublisherMap(source.Payload)
	return &copy
}

func clonePublisherMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = clonePublisherValue(value)
	}
	return result
}

func clonePublisherValue(value any) any {
	switch nested := value.(type) {
	case map[string]any:
		return clonePublisherMap(nested)
	case []any:
		result := make([]any, len(nested))
		for index, child := range nested {
			result[index] = clonePublisherValue(child)
		}
		return result
	default:
		return value
	}
}

type captureHTTPTransport struct {
	request *http.Request
	body    []byte
}

func (transport *captureHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.request = request.Clone(request.Context())
	transport.body, _ = io.ReadAll(request.Body)
	return &http.Response{StatusCode: http.StatusAccepted, Status: "202 Accepted",
		Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
}

type publisherIngestFixture struct {
	url       string
	db        *gorm.DB
	account   domain.Account
	store     repository.EventStoreRepository
	publisher *event.NATSPublisher
	attempts  atomic.Int32
	outcomes  chan error
	statuses  chan string
}

func newPublisherIngestFixture(t *testing.T, durable string) *publisherIngestFixture {
	t.Helper()
	_, url := startNATS(t)
	createStream(t, url, "aero_events", publisherCanonicalSubject)
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", durable, durable, "local")
	if err != nil {
		t.Fatal(err)
	}
	store := repository.NewEventStoreRepository(db)
	receipts := repository.NewEventReceiptRepositoryWithStore(db, store, ledger)
	ingest := newIngestService(receipts)
	fixture := &publisherIngestFixture{url: url, db: db, account: *account, store: store,
		outcomes: make(chan error, 16), statuses: make(chan string, 16)}
	bus, err := event.NewNATSBus(event.NATSConfig{
		URL: url, Stream: "aero_events", Subject: publisherCanonicalSubject,
		DeadLetterSubject: publisherCanonicalSubject + ".dead-letter", Durable: durable,
		MaxRedeliveries: 3, AckWait: time.Second,
	})
	if err != nil {
		t.Fatalf("new NATS bus: %v", err)
	}
	stop, err := bus.Subscribe(context.Background(), func(ctx context.Context, envelope *event.Envelope) error {
		fixture.attempts.Add(1)
		result, ingestErr := ingest.Ingest(ctx, envelope)
		if ingestErr == nil && result != nil {
			fixture.statuses <- result.Status
		}
		fixture.outcomes <- ingestErr
		return ingestErr
	})
	if err != nil {
		_ = bus.Close()
		t.Fatalf("subscribe NATS bus: %v", err)
	}
	fixture.publisher = newPublisher(t, url)
	t.Cleanup(func() {
		stop()
		_ = bus.Close()
	})
	time.Sleep(500 * time.Millisecond)
	return fixture
}

func waitForSuccessfulDeliveries(t *testing.T, outcomes <-chan error, want int) {
	t.Helper()
	successes := 0
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for successes < want {
		select {
		case err := <-outcomes:
			if err == nil {
				successes++
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d successful deliveries; got %d", want, successes)
		}
	}
}

func waitForStatus(t *testing.T, statuses <-chan string, want string) {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case status := <-statuses:
			if status == want {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for event status %s", want)
		}
	}
}

func waitForErrorCode(t *testing.T, outcomes <-chan error, code string) {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case err := <-outcomes:
			var appErr *apperr.AppError
			if errors.As(err, &appErr) && appErr.Code == code {
				return
			}
			if err != nil {
				t.Fatalf("handler error = %v, want %s", err, code)
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for handler error %s", code)
		}
	}
}

func assertEventStateCounts(t *testing.T, db *gorm.DB, envelope *event.Envelope, want int64) {
	t.Helper()
	if got := countEventRows(t, db, "event_receipts", envelope); got != want {
		t.Fatalf("event receipt rows = %d, want %d", got, want)
	}
	if got := countEventRows(t, db, "event_store", envelope); got != want {
		t.Fatalf("event-store rows = %d, want %d", got, want)
	}
	var snapshots int64
	if err := db.Raw("SELECT count(*) FROM account_snapshots WHERE account_id = ? AND source = ? AND dataset = ?",
		envelope.Payload["account_id"], envelope.Source, envelope.Payload["dataset"]).Scan(&snapshots).Error; err != nil {
		t.Fatalf("count account snapshots: %v", err)
	}
	if snapshots != want {
		t.Fatalf("account snapshot rows = %d, want %d", snapshots, want)
	}
}

func countEventRows(t *testing.T, db *gorm.DB, table string, envelope *event.Envelope) int64 {
	t.Helper()
	var count int64
	query := "SELECT count(*) FROM " + table + " WHERE tenant_id = ? AND source = ? AND event_id = ?"
	if err := db.Raw(query, envelope.TenantID, envelope.Source, envelope.EventID.String()).Scan(&count).Error; err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}
	return count
}
