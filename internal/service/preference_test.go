package service_test

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
)

func TestPreferenceService_ListEmpty(t *testing.T) {
	repo := &mocks.PreferenceRepo{ListAllFn: func(_ context.Context, uid string) ([]*domain.NotificationPreference, error) {
		return nil, nil
	}}
	svc := service.NewPreferenceService(repo)
	ctx := service.WithActor(context.Background(), "u1")
	prefs, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefs) != 0 {
		t.Fatalf("expected empty list, got %d items", len(prefs))
	}
}

func TestPreferenceService_SetRequiresActor(t *testing.T) {
	svc := service.NewPreferenceService(&mocks.PreferenceRepo{})
	if _, err := svc.List(context.Background()); err == nil {
		t.Fatal("expected unauthorized without actor")
	}
}

func TestPreferenceService_SetEnforcesUID(t *testing.T) {
	var gotUID string
	repo := &mocks.PreferenceRepo{UpsertFn: func(_ context.Context, prefs []*domain.NotificationPreference) error {
		gotUID = prefs[0].UID
		return nil
	}}
	svc := service.NewPreferenceService(repo)
	ctx := service.WithActor(context.Background(), "actor-u1")
	err := svc.Set(ctx, []*domain.NotificationPreference{
		{NotificationType: string(domain.NotifTypeMessageReceived), Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotUID != "actor-u1" {
		t.Fatalf("uid = %q, want actor-u1", gotUID)
	}
}

func TestPreferenceService_SetValidatesQuietHours(t *testing.T) {
	svc := service.NewPreferenceService(&mocks.PreferenceRepo{})
	ctx := service.WithActor(context.Background(), "u1")
	err := svc.Set(ctx, []*domain.NotificationPreference{
		{NotificationType: string(domain.NotifTypeMessageReceived), Enabled: true,
			QuietHoursStart: "invalid", QuietHoursEnd: "07:00"},
	})
	if err == nil {
		t.Fatal("expected error for invalid quiet_hours_start")
	}
}

// ── B3 Gap-4 audit site (standalone-fact, deterministic anchor) ─────────────

func TestPreferenceService_SetAuditFactCommitted(t *testing.T) {
	var logged *domain.AuditEvent
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		return nil
	}}
	repo := &mocks.PreferenceRepo{UpsertFn: func(_ context.Context, _ []*domain.NotificationPreference) error {
		return nil
	}}
	svc := service.NewPreferenceService(repo, service.WithPreferenceAuditLogger(audit))
	ctx := service.WithIdentity(context.Background(), &domain.TokenIdentity{UID: "u1", Issuer: "snaplink", Subject: "u1", TenantID: "tenant-a"})
	prefs := []*domain.NotificationPreference{{NotificationType: "message.sent", Enabled: true}}
	if err := svc.Set(ctx, prefs); err != nil {
		t.Fatal(err)
	}
	if logged == nil {
		t.Fatal("preference.set audit fact was not emitted")
	}
	if logged.Action != domain.AuditPreferenceSet {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditPreferenceSet)
	}
	if logged.ActorUID != "u1" {
		t.Fatalf("actor = %q, want u1 (ctx actor)", logged.ActorUID)
	}
	if logged.SubjectType != "preference" || logged.SubjectID != "u1" {
		t.Fatalf("subject = (%q,%q), want (preference,u1)", logged.SubjectType, logged.SubjectID)
	}
	if logged.ID == (domain.ID{}) {
		t.Fatal("event ID must be a deterministic anchor, not zero")
	}
	if count, ok := logged.Meta["count"].(int); !ok || count != 1 {
		t.Fatalf("payload count = %v (%T), want int(1)", logged.Meta["count"], logged.Meta["count"])
	}
}

func TestPreferenceService_SetAuditFailureFailsClosed(t *testing.T) {
	audit := &mocks.Auditor{LogFn: func(context.Context, *domain.AuditEvent) error {
		return stderrors.New("ledger down")
	}}
	repo := &mocks.PreferenceRepo{UpsertFn: func(_ context.Context, _ []*domain.NotificationPreference) error {
		return nil
	}}
	svc := service.NewPreferenceService(repo, service.WithPreferenceAuditLogger(audit))
	ctx := service.WithIdentity(context.Background(), &domain.TokenIdentity{UID: "u1", Issuer: "snaplink", Subject: "u1", TenantID: "tenant-a"})
	prefs := []*domain.NotificationPreference{{NotificationType: "message.sent", Enabled: true}}
	assertAuditIngestFailed(t, svc.Set(ctx, prefs))
}

// TestPreferenceService_SetAnchoredEventIDStableAcrossRetries: the derived
// anchor makes a retry of the same payload dedup at the ledger (identical
// EventID) while a distinct payload derives a distinct EventID — a later
// legitimate Set is never silently dropped by entity-anchor dedup.
func TestPreferenceService_SetAnchoredEventIDStableAcrossRetries(t *testing.T) {
	var ids []domain.ID
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		ids = append(ids, e.ID)
		return nil
	}}
	repo := &mocks.PreferenceRepo{UpsertFn: func(_ context.Context, _ []*domain.NotificationPreference) error {
		return nil
	}}
	svc := service.NewPreferenceService(repo, service.WithPreferenceAuditLogger(audit))
	ctx := service.WithIdentity(context.Background(), &domain.TokenIdentity{UID: "u1", Issuer: "snaplink", Subject: "u1", TenantID: "tenant-a"})

	same := []*domain.NotificationPreference{{NotificationType: "message.sent", Enabled: true, Timezone: "UTC"}}
	if err := svc.Set(ctx, same); err != nil {
		t.Fatal(err)
	}
	if err := svc.Set(ctx, same); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("facts recorded = %d, want 2", len(ids))
	}
	if ids[0] != ids[1] {
		t.Fatalf("same-payload retry must re-derive the same EventID: %v vs %v (ledger dedup contract)", ids[0], ids[1])
	}

	// A distinct payload (different enabled flag) must derive a distinct ID.
	different := []*domain.NotificationPreference{{NotificationType: "message.sent", Enabled: false, Timezone: "UTC"}}
	if err := svc.Set(ctx, different); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("facts recorded = %d, want 3", len(ids))
	}
	if ids[2] == ids[0] {
		t.Fatal("distinct payload must derive a distinct EventID (no silent drop of a later legitimate Set)")
	}
}
