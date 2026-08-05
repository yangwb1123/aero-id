package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// fakeReplicationPublisher records published envelopes and can fail on demand.
type fakeReplicationPublisher struct {
	mu        sync.Mutex
	published []*event.Envelope
	fail      bool
}

func (f *fakeReplicationPublisher) Target() string { return "fake:test" }
func (f *fakeReplicationPublisher) Publish(ctx context.Context, envelope *event.Envelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("remote ingest down")
	}
	f.published = append(f.published, envelope)
	return nil
}

type fakeCursorRepo struct {
	mu        sync.Mutex
	last      domain.ID
	published int64
}

func (f *fakeCursorRepo) GetCursor(ctx context.Context, sourceRegion, target string) (domain.ID, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last, f.published, nil
}
func (f *fakeCursorRepo) SaveCursor(ctx context.Context, sourceRegion, target string, lastSnapshotID domain.ID, published int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = lastSnapshotID
	f.published = published
	return nil
}

type fakeReplicationSnapshots struct {
	items []repository.ReplicableSnapshot
}

func (f *fakeReplicationSnapshots) GetSnapshot(ctx context.Context, accountID domain.ID, source string,
	dataset domain.AccountDataset) (*domain.AccountSnapshot, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeReplicationSnapshots) ListSnapshots(ctx context.Context, accountID domain.ID, datasets []domain.AccountDataset) ([]*domain.AccountSnapshot, error) {
	return nil, nil
}
func (f *fakeReplicationSnapshots) UpsertSnapshot(ctx context.Context, snapshot *domain.AccountSnapshot) error {
	return nil
}
func (f *fakeReplicationSnapshots) ListReplicableSnapshots(ctx context.Context, homeRegion string,
	afterID domain.ID, limit int) ([]repository.ReplicableSnapshot, error) {
	out := make([]repository.ReplicableSnapshot, 0, len(f.items))
	for _, item := range f.items {
		if item.Snapshot.ID.String() > afterID.String() {
			out = append(out, item)
		}
	}
	return out, nil
}

func replicationSnapshot(accountID domain.ID, dataset string, version int64) *domain.AccountSnapshot {
	return &domain.AccountSnapshot{
		ID: idgen.NewID(), AccountID: accountID, Source: "aero-vault", SourceRegion: "us-west",
		Dataset: domain.AccountDataset(dataset), Status: domain.SnapshotFresh, Version: version,
		GeneratedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Data: map[string]any{"usage_bytes": float64(version * 10)},
	}
}

// TestReplicationOnce_PublishesAndAdvancesCursor: approved local projections
// become canonical events and the durable cursor advances.
func TestReplicationOnce_PublishesAndAdvancesCursor(t *testing.T) {
	accountID := idgen.NewID()
	items := []repository.ReplicableSnapshot{
		{Snapshot: replicationSnapshot(accountID, "aero-vault.buckets", 1), TenantID: "tenant-a"},
		{Snapshot: replicationSnapshot(accountID, "aero-vault.buckets", 2), TenantID: "tenant-a"},
	}
	publisher := &fakeReplicationPublisher{}
	cursors := &fakeCursorRepo{}
	svc := NewProjectionReplicationService("local", &fakeReplicationSnapshots{items: items}, cursors, publisher)

	count, err := svc.ReplicateOnce(context.Background())
	if err != nil {
		t.Fatalf("replicate: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
	publisher.mu.Lock()
	envelopes := publisher.published
	publisher.mu.Unlock()
	if len(envelopes) != 2 {
		t.Fatalf("published = %d, want 2", len(envelopes))
	}
	if envelopes[0].Source != "aero-id" || envelopes[0].EventType != "projection.snapshot" ||
		envelopes[0].SourceRegion != "local" || envelopes[0].TenantID != "tenant-a" {
		t.Errorf("envelope context wrong: %+v", envelopes[0])
	}
	if envelopes[0].EventID == envelopes[1].EventID {
		t.Error("different versions must have different deterministic event ids")
	}
	// Deterministic: re-publishing the same version yields the same event id.
	if cursors.published != 2 {
		t.Errorf("cursor published = %d, want 2", cursors.published)
	}
}

// TestReplicationOnce_StopsOnPublisherFailure: a remote outage halts the batch
// with the cursor behind so nothing is skipped.
func TestReplicationOnce_StopsOnPublisherFailure(t *testing.T) {
	accountID := idgen.NewID()
	items := []repository.ReplicableSnapshot{
		{Snapshot: replicationSnapshot(accountID, "aero-vault.buckets", 1), TenantID: "tenant-a"},
		{Snapshot: replicationSnapshot(accountID, "aero-vault.buckets", 2), TenantID: "tenant-a"},
	}
	publisher := &fakeReplicationPublisher{fail: true}
	cursors := &fakeCursorRepo{}
	svc := NewProjectionReplicationService("local", &fakeReplicationSnapshots{items: items}, cursors, publisher)

	if _, err := svc.ReplicateOnce(context.Background()); err == nil {
		t.Fatal("publisher failure must surface")
	}
	if cursors.published != 0 {
		t.Errorf("cursor must not advance on failure, published = %d", cursors.published)
	}
}

// TestReplicationEnvelope_StablePerVersion: identical version re-publishes
// dedup at the consumer via the deterministic event id.
func TestReplicationEnvelope_StablePerVersion(t *testing.T) {
	accountID := idgen.NewID()
	first := replicationEnvelope("local", "tenant-a", replicationSnapshot(accountID, "aero-vault.buckets", 3))
	second := replicationEnvelope("local", "tenant-a", replicationSnapshot(accountID, "aero-vault.buckets", 3))
	if first.EventID != second.EventID {
		t.Fatalf("stable event ids differ: %s vs %s", first.EventID, second.EventID)
	}
	// A different tenant must produce a different id (receipts are tenant-scoped).
	other := replicationEnvelope("local", "tenant-b", replicationSnapshot(accountID, "aero-vault.buckets", 3))
	if other.EventID == first.EventID {
		t.Fatal("different tenants must not share an event id")
	}
}
