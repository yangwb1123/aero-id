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

type fakeCheckpointCursorRepo struct {
	fakeCursorRepo
	lastUpdatedAt time.Time
}

func (f *fakeCheckpointCursorRepo) GetCursorCheckpoint(context.Context, string, string) (domain.ID, time.Time, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last, f.lastUpdatedAt, f.published, nil
}

func (f *fakeCheckpointCursorRepo) SaveCursorCheckpoint(_ context.Context, _, _ string, updatedAt time.Time,
	lastSnapshotID domain.ID, published int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastUpdatedAt = updatedAt
	f.last = lastSnapshotID
	f.published = published
	return nil
}

type fakeLeasingCursorRepo struct {
	fakeCursorRepo
	acquired   bool
	claimCalls int
	saveCalls  int
}

func (f *fakeLeasingCursorRepo) ClaimCursor(context.Context, string, string, string, time.Duration) (bool, error) {
	f.claimCalls++
	return f.acquired, nil
}

func (f *fakeLeasingCursorRepo) SaveCursorOwned(ctx context.Context, sourceRegion, target, owner string,
	lease time.Duration, lastSnapshotID domain.ID, published int64) error {
	f.saveCalls++
	return f.SaveCursor(ctx, sourceRegion, target, lastSnapshotID, published)
}

func (f *fakeLeasingCursorRepo) ReleaseCursor(context.Context, string, string, string) error {
	return nil
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

type fakeVersionedReplicationSnapshots struct {
	fakeReplicationSnapshots
	lastUpdatedAt time.Time
	lastID        domain.ID
}

func (f *fakeVersionedReplicationSnapshots) ListReplicableSnapshotsAfter(_ context.Context, _ string,
	afterUpdatedAt time.Time, afterID domain.ID, _ int) ([]repository.ReplicableSnapshot, error) {
	f.lastUpdatedAt = afterUpdatedAt
	f.lastID = afterID
	result := make([]repository.ReplicableSnapshot, 0, len(f.items))
	for _, item := range f.items {
		updated := item.Snapshot.UpdatedAt
		if updated.After(afterUpdatedAt) || (updated.Equal(afterUpdatedAt) && item.Snapshot.ID.String() > afterID.String()) {
			result = append(result, item)
		}
	}
	return result, nil
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

func TestReplicationOnceHonorsDurableCursorLease(t *testing.T) {
	accountID := idgen.NewID()
	items := []repository.ReplicableSnapshot{{
		Snapshot: replicationSnapshot(accountID, "aero-vault.buckets", 1), TenantID: "tenant-a",
	}}
	cursors := &fakeLeasingCursorRepo{}
	publisher := &fakeReplicationPublisher{}
	svc := NewProjectionReplicationService("local", &fakeReplicationSnapshots{items: items}, cursors, publisher)

	count, err := svc.ReplicateOnce(context.Background())
	if err != nil {
		t.Fatalf("contended replication: %v", err)
	}
	if count != 0 || cursors.claimCalls != 1 || cursors.saveCalls != 0 || len(publisher.published) != 0 {
		t.Fatalf("contended run count=%d claims=%d saves=%d published=%d, want 0/1/0/0",
			count, cursors.claimCalls, cursors.saveCalls, len(publisher.published))
	}

	cursors.acquired = true
	count, err = svc.ReplicateOnce(context.Background())
	if err != nil {
		t.Fatalf("owned replication: %v", err)
	}
	if count != 1 || cursors.saveCalls != 1 || len(publisher.published) != 1 {
		t.Fatalf("owned run count=%d saves=%d published=%d, want 1/1/1", count, cursors.saveCalls, len(publisher.published))
	}
}

func TestReplicationOnceReplaysInPlaceSnapshotUpdates(t *testing.T) {
	accountID := idgen.NewID()
	snapshot := replicationSnapshot(accountID, "aero-vault.buckets", 1)
	items := &fakeVersionedReplicationSnapshots{fakeReplicationSnapshots: fakeReplicationSnapshots{
		items: []repository.ReplicableSnapshot{{Snapshot: snapshot, TenantID: "tenant-a"}},
	}}
	cursors := &fakeCheckpointCursorRepo{}
	publisher := &fakeReplicationPublisher{}
	svc := NewProjectionReplicationService("local", items, cursors, publisher)

	if count, err := svc.ReplicateOnce(context.Background()); err != nil || count != 1 {
		t.Fatalf("initial replication count=%d err=%v, want 1/nil", count, err)
	}
	initialUpdatedAt := snapshot.UpdatedAt
	updatedAt := snapshot.UpdatedAt.Add(time.Minute)
	snapshot.Version = 2
	snapshot.UpdatedAt = updatedAt
	snapshot.Data["usage_bytes"] = float64(20)

	if count, err := svc.ReplicateOnce(context.Background()); err != nil || count != 1 {
		t.Fatalf("updated replication count=%d err=%v, want 1/nil", count, err)
	}
	if len(publisher.published) != 2 {
		t.Fatalf("published=%d, want 2 after an in-place update", len(publisher.published))
	}
	if !items.lastUpdatedAt.Equal(initialUpdatedAt) || items.lastID != snapshot.ID {
		t.Fatalf("checkpoint query=(%s,%s), want (%s,%s)", items.lastUpdatedAt, items.lastID, initialUpdatedAt, snapshot.ID)
	}
	if !cursors.lastUpdatedAt.Equal(updatedAt) {
		t.Fatalf("saved checkpoint=%s, want %s", cursors.lastUpdatedAt, updatedAt)
	}
}

// TestReplicationEnvelope_StablePerRevision: identical projection revisions
// dedup at the consumer while a same-version data/status change gets a new id.
func TestReplicationEnvelope_StablePerRevision(t *testing.T) {
	accountID := idgen.NewID()
	snapshot := replicationSnapshot(accountID, "aero-vault.buckets", 3)
	first := replicationEnvelope("local", "tenant-a", snapshot)
	second := replicationEnvelope("local", "tenant-a", snapshot)
	if first.EventID != second.EventID {
		t.Fatalf("stable event ids differ: %s vs %s", first.EventID, second.EventID)
	}
	snapshot.Status = domain.SnapshotUnavailable
	changed := replicationEnvelope("local", "tenant-a", snapshot)
	if changed.EventID == first.EventID {
		t.Fatal("same-version status change must have a new event id")
	}
	// A different tenant must produce a different id (receipts are tenant-scoped).
	other := replicationEnvelope("local", "tenant-b", replicationSnapshot(accountID, "aero-vault.buckets", 3))
	if other.EventID == first.EventID {
		t.Fatal("different tenants must not share an event id")
	}
}

func TestReplicationEnvelope_CanonicalizesDatabaseTimestampPrecision(t *testing.T) {
	snapshot := replicationSnapshot(idgen.NewID(), "aero-vault.buckets", 1)
	snapshot.GeneratedAt = time.Date(2026, time.August, 20, 12, 30, 0, 123456789, time.UTC)
	fromDatabase := *snapshot
	fromDatabase.GeneratedAt = snapshot.GeneratedAt.Truncate(time.Microsecond)
	if got, want := replicationEnvelope("local", "tenant-a", &fromDatabase).EventID,
		replicationEnvelope("local", "tenant-a", snapshot).EventID; got != want {
		t.Fatalf("nanosecond timestamp changed event id after database round-trip: %s vs %s", got, want)
	}
}

// recordingLedgerSpy is a minimal AuditLedgerRepository recording every
// appended fact (the auditLedgerRepoSpy in audit_ledger_test.go keeps only the
// last one, which is insufficient for per-item assertions).
type recordingLedgerSpy struct {
	facts []*domain.AuditFact
	err   error
}

func (r *recordingLedgerSpy) Append(_ context.Context, fact *domain.AuditFact) error {
	r.facts = append(r.facts, fact)
	return r.err
}
func (r *recordingLedgerSpy) List(_ context.Context, _ domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	return nil, "", nil
}
func (r *recordingLedgerSpy) VerifyChain(_ context.Context, _, _ string) error { return nil }
func (r *recordingLedgerSpy) ChainPartitions(context.Context) ([]repository.ChainPartition, error) {
	return nil, nil
}
func (r *recordingLedgerSpy) ChainHead(context.Context, string, string) (*domain.AuditFact, error) {
	return nil, repository.ErrNotFound
}

// TestReplicateOnceAuditFactPerPublishedItem: one projection.replication.publish
// fact per published snapshot, with the deterministic envelope EventID and the
// item's tenant/account.
func TestReplicateOnceAuditFactPerPublishedItem(t *testing.T) {
	accountID := idgen.NewID()
	items := []repository.ReplicableSnapshot{
		{Snapshot: replicationSnapshot(accountID, "aero-vault.buckets", 1), TenantID: "tenant-a"},
		{Snapshot: replicationSnapshot(accountID, "aero-vault.buckets", 2), TenantID: "tenant-b"},
	}
	publisher := &fakeReplicationPublisher{}
	cursors := &fakeCursorRepo{}
	spy := &recordingLedgerSpy{}
	svc := NewProjectionReplicationService("local", &fakeReplicationSnapshots{items: items}, cursors, publisher,
		WithProjectionReplicationAudit(NewAuditLedgerService(spy)))

	count, err := svc.ReplicateOnce(context.Background())
	if err != nil {
		t.Fatalf("replicate: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
	if len(spy.facts) != 2 {
		t.Fatalf("audit facts = %d, want 2 (one per published item)", len(spy.facts))
	}
	publisher.mu.Lock()
	envelopes := publisher.published
	publisher.mu.Unlock()
	if len(envelopes) != 2 {
		t.Fatalf("published = %d, want 2", len(envelopes))
	}
	for i, fact := range spy.facts {
		if fact.EventID != envelopes[i].EventID {
			t.Errorf("fact %d EventID = %v, want envelope EventID %v (deterministic)", i, fact.EventID, envelopes[i].EventID)
		}
		if fact.TenantID != items[i].TenantID {
			t.Errorf("fact %d tenant = %q, want %q", i, fact.TenantID, items[i].TenantID)
		}
		if fact.AccountID != items[i].Snapshot.AccountID {
			t.Errorf("fact %d account = %v, want %v", i, fact.AccountID, items[i].Snapshot.AccountID)
		}
		if fact.ActorUID != "system" {
			t.Errorf("fact %d actor = %q, want system", i, fact.ActorUID)
		}
		if fact.Action != domain.AuditProjectionReplicationPublish {
			t.Errorf("fact %d action = %q, want %q", i, fact.Action, domain.AuditProjectionReplicationPublish)
		}
	}
}

// TestReplicateOnceAuditFailureFailsRun: an audit failure fails the run and
// the cursor stays behind (SaveCursor not called), so the next run re-publishes
// and the deterministic EventID dedups the fact — at-least-once consistent.
func TestReplicateOnceAuditFailureFailsRun(t *testing.T) {
	accountID := idgen.NewID()
	items := []repository.ReplicableSnapshot{
		{Snapshot: replicationSnapshot(accountID, "aero-vault.buckets", 1), TenantID: "tenant-a"},
	}
	cursors := &fakeCursorRepo{}
	spy := &recordingLedgerSpy{err: errors.New("ledger down")}
	svc := NewProjectionReplicationService("local", &fakeReplicationSnapshots{items: items}, cursors,
		&fakeReplicationPublisher{}, WithProjectionReplicationAudit(NewAuditLedgerService(spy)))

	if _, err := svc.ReplicateOnce(context.Background()); err == nil {
		t.Fatal("audit failure must fail the run")
	}
	if cursors.published != 0 {
		t.Errorf("cursor must not advance on audit failure, published = %d", cursors.published)
	}
}

// TestReplicateOnceAuditEventIDDeterministic: two runs publishing the same
// snapshot version derive the same EventID, so the ledger dedups the
// re-publish to the first fact.
func TestReplicateOnceAuditEventIDDeterministic(t *testing.T) {
	accountID := idgen.NewID()
	items := []repository.ReplicableSnapshot{
		{Snapshot: replicationSnapshot(accountID, "aero-vault.buckets", 1), TenantID: "tenant-a"},
	}
	spy := &recordingLedgerSpy{}
	for i := 0; i < 2; i++ {
		// A fresh cursor each run so both runs publish the same item.
		svc := NewProjectionReplicationService("local", &fakeReplicationSnapshots{items: items},
			&fakeCursorRepo{}, &fakeReplicationPublisher{}, WithProjectionReplicationAudit(NewAuditLedgerService(spy)))
		if _, err := svc.ReplicateOnce(context.Background()); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if len(spy.facts) != 2 {
		t.Fatalf("facts = %d, want 2 (two executions)", len(spy.facts))
	}
	if spy.facts[0].EventID != spy.facts[1].EventID {
		t.Fatalf("same snapshot version must re-derive the same EventID (dedup): %v vs %v",
			spy.facts[0].EventID, spy.facts[1].EventID)
	}
}
