package notification_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/notification"
	"github.com/aero/aero-id/internal/sso"
)

type svcMockNotifRepo struct {
	CreateFn          func(ctx context.Context, n *domain.Notification) error
	ListByRecipientFn func(ctx context.Context, recipientUID string, page domain.Page) ([]*domain.Notification, string, error)
	MarkReadFn        func(ctx context.Context, id domain.ID, readAt time.Time) error
	MarkAllReadFn     func(ctx context.Context, recipientUID string, readAt time.Time) error
	CountUnreadFn     func(ctx context.Context, recipientUID string) (int64, error)
}

func (m *svcMockNotifRepo) Create(ctx context.Context, n *domain.Notification) error {
	if m.CreateFn != nil {
		return m.CreateFn(ctx, n)
	}
	return nil
}

func (m *svcMockNotifRepo) ListByRecipient(ctx context.Context, recipientUID string, page domain.Page) ([]*domain.Notification, string, error) {
	if m.ListByRecipientFn != nil {
		return m.ListByRecipientFn(ctx, recipientUID, page)
	}
	return nil, "", nil
}

func (m *svcMockNotifRepo) MarkRead(ctx context.Context, id domain.ID, readAt time.Time) error {
	if m.MarkReadFn != nil {
		return m.MarkReadFn(ctx, id, readAt)
	}
	return nil
}

func (m *svcMockNotifRepo) MarkAllRead(ctx context.Context, recipientUID string, readAt time.Time) error {
	if m.MarkAllReadFn != nil {
		return m.MarkAllReadFn(ctx, recipientUID, readAt)
	}
	return nil
}

func (m *svcMockNotifRepo) CountUnread(ctx context.Context, recipientUID string) (int64, error) {
	if m.CountUnreadFn != nil {
		return m.CountUnreadFn(ctx, recipientUID)
	}
	return 0, nil
}

type mockUserDir struct {
	GetUsersFn func(ctx context.Context, uids []string) (map[string]sso.User, error)
}

func (m *mockUserDir) GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error) {
	if m.GetUsersFn != nil {
		return m.GetUsersFn(ctx, uids)
	}
	return map[string]sso.User{}, nil
}

func TestServiceList_ReturnsNotifications(t *testing.T) {
	repo := &svcMockNotifRepo{
		ListByRecipientFn: func(_ context.Context, uid string, _ domain.Page) ([]*domain.Notification, string, error) {
			return []*domain.Notification{
				{ID: domain.ID(uuid.Must(uuid.NewV7())), RecipientUID: uid, Type: "test", ActorUID: "a1"},
			}, "next-cursor", nil
		},
	}
	dir := &mockUserDir{
		GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
			return map[string]sso.User{"a1": {UID: "a1", Nickname: "Alice"}}, nil
		},
	}
	svc := notification.NewService(repo, dir)
	ctx := domain.WithActor(context.Background(), "u1")
	list, err := svc.List(ctx, domain.Page{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Notifications) != 1 {
		t.Fatalf("got %d notifications", len(list.Notifications))
	}
	if list.NextCursor != "next-cursor" {
		t.Errorf("cursor = %q", list.NextCursor)
	}
	if list.Users["a1"].Nickname != "Alice" {
		t.Errorf("user = %+v", list.Users["a1"])
	}
}

func TestServiceList_Empty(t *testing.T) {
	repo := &svcMockNotifRepo{}
	svc := notification.NewService(repo, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "u1")
	list, err := svc.List(ctx, domain.Page{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Notifications) != 0 {
		t.Errorf("got %d notifications", len(list.Notifications))
	}
}

func TestServiceList_NoActor(t *testing.T) {
	svc := notification.NewService(&svcMockNotifRepo{}, &mockUserDir{})
	_, err := svc.List(context.Background(), domain.Page{Limit: 20})
	if err == nil {
		t.Fatal("expected error for missing actor")
	}
}

func TestServiceMarkRead_CallsRepo(t *testing.T) {
	called := false
	id := uuid.Must(uuid.NewV7())
	repo := &svcMockNotifRepo{
		MarkReadFn: func(_ context.Context, got domain.ID, _ time.Time) error {
			called = true
			if got != domain.ID(id) {
				t.Fatalf("id = %v", got)
			}
			return nil
		},
	}
	svc := notification.NewService(repo, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "u1")
	if err := svc.MarkRead(ctx, domain.ID(id)); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("MarkRead not called")
	}
}

func TestServiceMarkRead_NoActor(t *testing.T) {
	svc := notification.NewService(&svcMockNotifRepo{}, &mockUserDir{})
	if err := svc.MarkRead(context.Background(), domain.ID(uuid.Must(uuid.NewV7()))); err == nil {
		t.Fatal("expected error for missing actor")
	}
}

func TestServiceMarkAllRead_CallsRepo(t *testing.T) {
	called := false
	repo := &svcMockNotifRepo{
		MarkAllReadFn: func(_ context.Context, uid string, _ time.Time) error {
			called = true
			if uid != "u1" {
				t.Fatalf("uid = %q", uid)
			}
			return nil
		},
	}
	svc := notification.NewService(repo, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "u1")
	if err := svc.MarkAllRead(ctx); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("MarkAllRead not called")
	}
}

func TestServiceMarkAllRead_NoActor(t *testing.T) {
	svc := notification.NewService(&svcMockNotifRepo{}, &mockUserDir{})
	if err := svc.MarkAllRead(context.Background()); err == nil {
		t.Fatal("expected error for missing actor")
	}
}

func TestServiceUnreadCount_ReturnsCount(t *testing.T) {
	repo := &svcMockNotifRepo{
		CountUnreadFn: func(_ context.Context, uid string) (int64, error) {
			if uid != "u1" {
				t.Fatalf("uid = %q", uid)
			}
			return 5, nil
		},
	}
	svc := notification.NewService(repo, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "u1")
	n, err := svc.UnreadCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("count = %d", n)
	}
}

func TestServiceUnreadCount_NoActor(t *testing.T) {
	svc := notification.NewService(&svcMockNotifRepo{}, &mockUserDir{})
	_, err := svc.UnreadCount(context.Background())
	if err == nil {
		t.Fatal("expected error for missing actor")
	}
}
