package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type fakeEventStore struct {
	events []*event.Envelope
}

func (f *fakeEventStore) Append(ctx context.Context, envelope *event.Envelope) error { return nil }
func (f *fakeEventStore) ListByAggregate(ctx context.Context, tenantID, source, aggregateType, aggregateID string,
	fromVersion int64, limit int) ([]*event.Envelope, error) {
	return f.events, nil
}
func (f *fakeEventStore) ListByCursor(ctx context.Context, tenantID, source string, cursor domain.ID, limit int) ([]*event.Envelope, domain.ID, error) {
	return f.events, domain.ID{}, nil
}
func (f *fakeEventStore) ListByAccount(ctx context.Context, tenantID, source, accountID string, cursor domain.ID, limit int) ([]*event.Envelope, domain.ID, error) {
	if cursor != (domain.ID{}) {
		return nil, domain.ID{}, nil
	}
	if len(f.events) > limit {
		return f.events[:limit], f.events[limit-1].EventID, nil
	}
	return f.events, domain.ID{}, nil
}

type fakeSnapshotStore struct {
	snapshots []*domain.AccountSnapshot
}

func (f *fakeSnapshotStore) GetSnapshot(ctx context.Context, accountID domain.ID, source string,
	dataset domain.AccountDataset) (*domain.AccountSnapshot, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeSnapshotStore) ListSnapshots(ctx context.Context, accountID domain.ID, datasets []domain.AccountDataset) ([]*domain.AccountSnapshot, error) {
	return f.snapshots, nil
}
func (f *fakeSnapshotStore) UpsertSnapshot(ctx context.Context, snapshot *domain.AccountSnapshot) error {
	return nil
}
func (f *fakeSnapshotStore) MarkUnavailable(ctx context.Context, accountID domain.ID, source string,
	dataset domain.AccountDataset, code string) error {
	return nil
}
func (f *fakeSnapshotStore) ListReplicableSnapshots(ctx context.Context, homeRegion string,
	afterID domain.ID, limit int) ([]repository.ReplicableSnapshot, error) {
	return nil, nil
}

func replayEnvelope(accountID domain.ID, dataset string, version int64, usage float64, occurred time.Time) *event.Envelope {
	return &event.Envelope{
		EventID: idgen.NewID(), Source: "aero-vault", SourceRegion: "us-west",
		EventType: "vault.usage.updated", SchemaVersion: 1, AggregateType: "object",
		AggregateID: "obj-1", AggregateVersion: version, TenantID: "tenant-a",
		OccurredAt: occurred,
		Payload: map[string]any{
			"account_id": accountID.String(), "dataset": dataset, "status": "fresh",
			"version": float64(version), "data": map[string]any{"usage_bytes": usage},
		},
	}
}

func TestReplayProjection_MatchesLive(t *testing.T) {
	accountID := idgen.NewID()
	now := time.Now().UTC()
	events := []*event.Envelope{
		replayEnvelope(accountID, "aero-vault.buckets", 1, 10, now.Add(-2*time.Hour)),
		replayEnvelope(accountID, "aero-vault.buckets", 2, 20, now.Add(-time.Hour)),
		replayEnvelope(accountID, "aero-vault.buckets", 3, 30, now),
	}
	svc := NewProjectionReplayService(&fakeEventStore{events: events}, &fakeSnapshotStore{
		snapshots: []*domain.AccountSnapshot{{
			AccountID: accountID, Source: "aero-vault", SourceRegion: "us-west",
			Dataset: "aero-vault.buckets", Status: domain.SnapshotFresh, Version: 3,
			GeneratedAt: now, Data: map[string]any{"usage_bytes": float64(30)},
		}},
	})
	result, err := svc.ReplayProjection(context.Background(), ReplayProjectionInput{
		AccountID: accountID, TenantID: "tenant-a", Source: "aero-vault",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.ReplayedEvents != 3 {
		t.Errorf("replayed = %d, want 3", result.ReplayedEvents)
	}
	if !result.Completed {
		t.Error("completed = false, want true")
	}
	if len(result.Matched) != 1 || len(result.Divergent) != 0 ||
		len(result.ShadowOnly) != 0 || len(result.LiveOnly) != 0 {
		t.Errorf("matched/divergent/shadow/live = %d/%d/%d/%d, want 1/0/0/0",
			len(result.Matched), len(result.Divergent), len(result.ShadowOnly), len(result.LiveOnly))
	}
	if result.Matched[0].Version != 3 {
		t.Errorf("matched version = %d, want 3", result.Matched[0].Version)
	}
}

func TestReplayProjection_DetectsDrift(t *testing.T) {
	accountID := idgen.NewID()
	now := time.Now().UTC()
	events := []*event.Envelope{
		replayEnvelope(accountID, "aero-vault.buckets", 2, 20, now.Add(-time.Hour)),
		replayEnvelope(accountID, "aero-vault.buckets", 3, 30, now),
	}
	// Live projection missed the latest event (v3) — drifted at v2 with old data.
	svc := NewProjectionReplayService(&fakeEventStore{events: events}, &fakeSnapshotStore{
		snapshots: []*domain.AccountSnapshot{{
			AccountID: accountID, Source: "aero-vault", SourceRegion: "us-west",
			Dataset: "aero-vault.buckets", Status: domain.SnapshotFresh, Version: 2,
			GeneratedAt: now.Add(-time.Hour), Data: map[string]any{"usage_bytes": float64(20)},
		}},
	})
	result, err := svc.ReplayProjection(context.Background(), ReplayProjectionInput{
		AccountID: accountID, TenantID: "tenant-a", Source: "aero-vault",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(result.Divergent) != 1 {
		t.Fatalf("divergent = %d, want 1 (live v2 vs shadow v3)", len(result.Divergent))
	}
	if result.Divergent[0].Version != 3 || result.Divergent[0].LiveVersion != 2 {
		t.Errorf("divergent versions = shadow %d / live %d, want 3/2",
			result.Divergent[0].Version, result.Divergent[0].LiveVersion)
	}
}

func TestReplayProjection_OutOfOrderKeepsNewest(t *testing.T) {
	accountID := idgen.NewID()
	now := time.Now().UTC()
	events := []*event.Envelope{
		replayEnvelope(accountID, "aero-vault.buckets", 5, 50, now),
		replayEnvelope(accountID, "aero-vault.buckets", 4, 40, now.Add(-time.Minute)),
	}
	svc := NewProjectionReplayService(&fakeEventStore{events: events}, &fakeSnapshotStore{})
	result, err := svc.ReplayProjection(context.Background(), ReplayProjectionInput{
		AccountID: accountID, TenantID: "tenant-a", Source: "aero-vault",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(result.ShadowOnly) != 1 {
		t.Fatalf("shadow only = %d, want 1", len(result.ShadowOnly))
	}
	if result.ShadowOnly[0].Version != 5 {
		t.Errorf("shadow version = %d, want 5 (newest wins)", result.ShadowOnly[0].Version)
	}
}

func TestReplayProjection_Paginates(t *testing.T) {
	accountID := idgen.NewID()
	now := time.Now().UTC()
	events := []*event.Envelope{
		replayEnvelope(accountID, "aero-vault.buckets", 1, 10, now),
		replayEnvelope(accountID, "aero-vault.buckets", 2, 20, now),
	}
	svc := NewProjectionReplayService(&fakeEventStore{events: events}, &fakeSnapshotStore{})
	result, err := svc.ReplayProjection(context.Background(), ReplayProjectionInput{
		AccountID: accountID, TenantID: "tenant-a", Source: "aero-vault", Limit: 1,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Completed {
		t.Error("completed = true, want false (limit 1 of 2 events)")
	}
	if result.NextCursor == "" {
		t.Error("next_cursor missing")
	}
}

func TestReplayProjection_RequiresValidInput(t *testing.T) {
	svc := NewProjectionReplayService(&fakeEventStore{}, &fakeSnapshotStore{})
	if _, err := svc.ReplayProjection(context.Background(), ReplayProjectionInput{
		AccountID: domain.ID{}, TenantID: "tenant-a", Source: "aero-vault",
	}); err == nil {
		t.Fatal("empty account_id must be rejected")
	}
	if _, err := svc.ReplayProjection(context.Background(), ReplayProjectionInput{
		AccountID: idgen.NewID(), TenantID: "", Source: "aero-vault",
	}); err == nil {
		t.Fatal("empty tenant must be rejected")
	}
}

// ── B3 Gap-4 audit site (projection.replay.run) ─────────────────────────────

func TestReplayProjectionAuditFactCommitted(t *testing.T) {
	accountID := idgen.NewID()
	now := time.Now().UTC()
	events := []*event.Envelope{
		replayEnvelope(accountID, "aero-vault.buckets", 1, 10, now.Add(-time.Hour)),
	}
	spy := &recordingLedgerSpy{}
	svc := NewProjectionReplayService(&fakeEventStore{events: events}, &fakeSnapshotStore{},
		WithProjectionReplayAudit(NewAuditLedgerService(spy)))
	ctx := WithIdentity(context.Background(), &domain.TokenIdentity{UID: "admin-1", Issuer: "snaplink", Subject: "admin-1", TenantID: "tenant-a"})
	result, err := svc.ReplayProjection(ctx, ReplayProjectionInput{AccountID: accountID, TenantID: "tenant-a", Source: "aero-vault"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReplayedEvents != 1 {
		t.Fatalf("replayed = %d, want 1", result.ReplayedEvents)
	}
	if len(spy.facts) != 1 {
		t.Fatalf("audit facts = %d, want 1 (one per run)", len(spy.facts))
	}
	fact := spy.facts[0]
	if fact.Action != domain.AuditProjectionReplay {
		t.Fatalf("action = %q, want %q", fact.Action, domain.AuditProjectionReplay)
	}
	if fact.TenantID != "tenant-a" || fact.AccountID != accountID {
		t.Fatalf("tenant/account = (%q,%v), want (tenant-a,%v)", fact.TenantID, fact.AccountID, accountID)
	}
	if fact.ActorUID != "admin-1" {
		t.Fatalf("actor = %q, want admin-1 (ctx actor)", fact.ActorUID)
	}
	if fact.EventID != (domain.ID{}) {
		t.Fatalf("event ID = %v, want zero (fresh per-execution — no retry-dedup contract)", fact.EventID)
	}
	if fact.Payload["completed"] != true {
		t.Fatalf("payload completed = %v, want true", fact.Payload["completed"])
	}

	// A zero-event run records its fact through the same helper (single bare site).
	spy2 := &recordingLedgerSpy{}
	svc2 := NewProjectionReplayService(&fakeEventStore{}, &fakeSnapshotStore{},
		WithProjectionReplayAudit(NewAuditLedgerService(spy2)))
	if _, err := svc2.ReplayProjection(ctx, ReplayProjectionInput{AccountID: accountID, TenantID: "tenant-a", Source: "aero-vault"}); err != nil {
		t.Fatal(err)
	}
	if len(spy2.facts) != 1 {
		t.Fatalf("zero-event run facts = %d, want 1 (the run is still an audited sensitive-read)", len(spy2.facts))
	}
}

func TestReplayProjectionAuditFailureFailsClosed(t *testing.T) {
	accountID := idgen.NewID()
	now := time.Now().UTC()
	events := []*event.Envelope{
		replayEnvelope(accountID, "aero-vault.buckets", 1, 10, now.Add(-time.Hour)),
	}
	spy := &recordingLedgerSpy{err: errors.New("ledger down")}
	svc := NewProjectionReplayService(&fakeEventStore{events: events}, &fakeSnapshotStore{},
		WithProjectionReplayAudit(NewAuditLedgerService(spy)))
	ctx := WithIdentity(context.Background(), &domain.TokenIdentity{UID: "admin-1", Issuer: "snaplink", Subject: "admin-1", TenantID: "tenant-a"})
	_, err := svc.ReplayProjection(ctx, ReplayProjectionInput{AccountID: accountID, TenantID: "tenant-a", Source: "aero-vault"})
	var ae *apperr.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *AppError, got %v", err)
	}
	if ae.Code != "audit.ingest_failed" {
		t.Fatalf("code = %q, want audit.ingest_failed (fail-closed)", ae.Code)
	}
}
