package service_test

import (
	"context"
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
