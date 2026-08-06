//go:build integration

package event_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	tc "github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"github.com/aero/aero-id/internal/service"
)

// startNATS runs a JetStream-enabled NATS server via testcontainers and creates
// the stream the adapter consumes from.
func startNATS(t *testing.T) (*tcnats.NATSContainer, string) {
	t.Helper()
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx, tc.WithImage("nats:2.10-alpine"))
	if err != nil {
		t.Fatalf("start nats: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	url, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("nats url: %v", err)
	}
	return container, url
}

func createStream(t *testing.T, url, stream, subject string) {
	t.Helper()
	conn, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	defer conn.Close()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: stream, Subjects: []string{subject}}); err != nil {
		t.Fatalf("add stream: %v", err)
	}
}

func TestNATSBusConsumesCanonicalEnvelopes(t *testing.T) {
	_, url := startNATS(t)
	createStream(t, url, "aero_events", "aero.events.canonical")

	// Real PostgreSQL for the ingest side (receipts + event store).
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "u-nats", "u-nats", "local")
	if err != nil {
		t.Fatal(err)
	}
	store := repository.NewEventStoreRepository(db)
	receipts := repository.NewEventReceiptRepositoryWithStore(db, store, ledger)
	ingest := newIngestService(receipts)

	bus, err := event.NewNATSBus(event.NATSConfig{
		URL: url, Stream: "aero_events", Subject: "aero.events.canonical",
		Durable: "aero-id-ingest", MaxRedeliveries: 2, AckWait: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	defer bus.Close()
	stop, err := bus.Subscribe(context.Background(), func(ctx context.Context, envelope *event.Envelope) error {
		_, err := ingest.Ingest(ctx, envelope)
		return err
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer stop()
	time.Sleep(500 * time.Millisecond) // let the consumer attach

	envelope := &event.Envelope{
		EventID: idgen.NewID(), Source: "aero-vault", SourceRegion: "us-west",
		EventType: "vault.usage.updated", SchemaVersion: 1, AggregateType: "object",
		AggregateID: "obj-1", AggregateVersion: 7, TenantID: "tenant-a",
		OccurredAt: time.Now().UTC(),
		Payload: map[string]any{
			"account_id": account.ID.String(), "dataset": "aero-vault.buckets", "status": "fresh",
			"version": float64(7), "data": map[string]any{"usage_bytes": 99},
		},
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	publish := func() error {
		conn, err := nats.Connect(url)
		if err != nil {
			return err
		}
		defer conn.Close()
		js, err := conn.JetStream()
		if err != nil {
			return err
		}
		_, err = js.Publish("aero.events.canonical", raw)
		return err
	}
	if err := publish(); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// The consumer must apply the event: poll the event store until it lands.
	deadline := time.Now().Add(15 * time.Second)
	for {
		stored, _, listErr := store.ListByAccount(context.Background(), "tenant-a", "aero-vault",
			account.ID.String(), domain.ID{}, 10)
		if listErr == nil && len(stored) == 1 {
			if stored[0].AggregateVersion != 7 {
				t.Fatalf("stored version = %d, want 7", stored[0].AggregateVersion)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("event never landed in the event store (last err %v)", listErr)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Redelivery of the same envelope is a no-op (idempotent receipts).
	if err := publish(); err != nil {
		t.Fatalf("republish: %v", err)
	}
	time.Sleep(2 * time.Second)
	stored, _, err := store.ListByAccount(context.Background(), "tenant-a", "aero-vault",
		account.ID.String(), domain.ID{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored after duplicate = %d, want 1", len(stored))
	}
}

func TestNATSBusRejectsPoisonEnvelope(t *testing.T) {
	_, url := startNATS(t)
	createStream(t, url, "aero_events", "aero.events.canonical")
	bus, err := event.NewNATSBus(event.NATSConfig{
		URL: url, Stream: "aero_events", Subject: "aero.events.canonical",
		Durable: "aero-id-ingest-poison", MaxRedeliveries: 2, AckWait: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	received := make(chan struct{}, 10)
	stop, err := bus.Subscribe(context.Background(), func(ctx context.Context, envelope *event.Envelope) error {
		received <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	time.Sleep(500 * time.Millisecond)

	conn, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	js, err := conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	// Malformed JSON must be Term()inated, never delivered to the handler.
	if _, err := js.Publish("aero.events.canonical", []byte("{not-json")); err != nil {
		t.Fatal(err)
	}
	// Valid JSON but failing envelope validation must also be terminated.
	if _, err := js.Publish("aero.events.canonical", []byte(`{"event_id":"00000000-0000-0000-0000-000000000000","source":"x"}`)); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	select {
	case <-received:
		t.Fatal("poison envelopes must never reach the handler")
	case <-time.After(3 * time.Second):
		// expected: no handler invocation
	}
}

func newIngestService(receipts repository.EventReceiptRepository) service.EventIngestService {
	return service.NewEventIngestService(receipts)
}

// TestProjectionReplicationRoundTrip is the P6-03 end-to-end gate: a snapshot
// published by the replication worker is consumed by NATSBus, applied through
// the ingest pipeline (real PostgreSQL receipts + event store), and the
// consumer's projection reflects the replicated data.
func TestProjectionReplicationRoundTrip(t *testing.T) {
	_, url := startNATS(t)
	createStream(t, url, "aero_events", "aero.events.canonical")

	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "u-repl", "u-repl", "local")
	if err != nil {
		t.Fatal(err)
	}
	store := repository.NewEventStoreRepository(db)
	receipts := repository.NewEventReceiptRepositoryWithStore(db, store, ledger)
	ingest := newIngestService(receipts)

	bus, err := event.NewNATSBus(event.NATSConfig{
		URL: url, Stream: "aero_events", Subject: "aero.events.canonical",
		Durable: "aero-id-ingest-repl", MaxRedeliveries: 2, AckWait: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	stop, err := bus.Subscribe(context.Background(), func(ctx context.Context, envelope *event.Envelope) error {
		_, err := ingest.Ingest(ctx, envelope)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	time.Sleep(500 * time.Millisecond)

	publisher, err := event.NewNATSPublisher(url, "aero.events.canonical")
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()

	// A replica cell publishes an approved projection of a homed account.
	envelope := &event.Envelope{
		EventID: idgen.NewID(), Source: "aero-id", SourceRegion: "us-east",
		EventType: "projection.snapshot", SchemaVersion: 1, AggregateType: "projection",
		AggregateID: account.ID.String(), AggregateVersion: 5, TenantID: "tenant-a",
		OccurredAt: time.Now().UTC(),
		Payload: map[string]any{
			"account_id": account.ID.String(), "dataset": "aero-vault.buckets", "status": "fresh",
			"version": float64(5), "data": map[string]any{"usage_bytes": float64(555)},
		},
	}
	if err := publisher.Publish(context.Background(), envelope); err != nil {
		t.Fatalf("publish: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		stored, _, listErr := store.ListByAccount(context.Background(), "tenant-a", "aero-id",
			account.ID.String(), domain.ID{}, 10)
		if listErr == nil && len(stored) == 1 && stored[0].Payload["dataset"] == "aero-vault.buckets" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replicated projection never landed (err %v)", listErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestNATSBusRedeliveryAfterTransientFailure is the P6-09 broker-redelivery
// chaos gate: a handler that returns a transient error causes a Nak; JetStream
// redelivers the message (at-least-once), and once the handler succeeds the
// message is acked and stops redelivering.
func TestNATSBusRedeliveryAfterTransientFailure(t *testing.T) {
	_, url := startNATS(t)
	createStream(t, url, "aero_events", "aero.events.canonical")
	bus, err := event.NewNATSBus(event.NATSConfig{
		URL: url, Stream: "aero_events", Subject: "aero.events.canonical",
		Durable: "aero-id-ingest-redeliver", MaxRedeliveries: 5, AckWait: 1 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	var mu sync.Mutex
	attempts := 0
	stop, err := bus.Subscribe(context.Background(), func(ctx context.Context, envelope *event.Envelope) error {
		mu.Lock()
		attempts++
		first := attempts == 1
		mu.Unlock()
		if first {
			return errors.New("transient: db hiccup") // triggers Nak
		}
		return nil // second delivery succeeds
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	time.Sleep(500 * time.Millisecond)

	conn, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	js, err := conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	envelope := &event.Envelope{
		EventID: idgen.NewID(), Source: "aero-vault", SourceRegion: "us-west",
		EventType: "vault.usage.updated", SchemaVersion: 1, AggregateType: "object",
		AggregateID: "obj-1", AggregateVersion: 1, TenantID: "tenant-a",
		OccurredAt: time.Now().UTC(),
		Payload: map[string]any{
			"account_id": idgen.NewID().String(), "dataset": "aero-vault.buckets", "status": "fresh",
			"version": float64(1), "data": map[string]any{"usage_bytes": float64(1)},
		},
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.Publish("aero.events.canonical", raw); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	// The message must be delivered at least twice (first Nak'd, then acked).
	deadline := time.Now().Add(20 * time.Second)
	for {
		mu.Lock()
		got := attempts
		mu.Unlock()
		if got >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("redelivery never happened; attempts = %d", got)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Let the ack settle; then verify the consumer has no pending messages.
	time.Sleep(2 * time.Second)
	mu.Lock()
	defer mu.Unlock()
	if attempts > 3 {
		t.Errorf("attempts = %d, want <= 3 (acked after the second delivery)", attempts)
	}
}
