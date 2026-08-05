package notification_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	"github.com/aero/aero-id/internal/notification"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/sso"
)

type mockNotificationService struct {
	listFn        func(ctx context.Context, page domain.Page) (*notification.NotificationList, error)
	markReadFn    func(ctx context.Context, id domain.ID) error
	markAllReadFn func(ctx context.Context) error
	unreadCountFn func(ctx context.Context) (int64, error)
}

func (m *mockNotificationService) List(ctx context.Context, page domain.Page) (*notification.NotificationList, error) {
	if m.listFn != nil {
		return m.listFn(ctx, page)
	}
	return nil, nil
}

func (m *mockNotificationService) MarkRead(ctx context.Context, id domain.ID) error {
	if m.markReadFn != nil {
		return m.markReadFn(ctx, id)
	}
	return nil
}

func (m *mockNotificationService) MarkAllRead(ctx context.Context) error {
	if m.markAllReadFn != nil {
		return m.markAllReadFn(ctx)
	}
	return nil
}

func (m *mockNotificationService) UnreadCount(ctx context.Context) (int64, error) {
	if m.unreadCountFn != nil {
		return m.unreadCountFn(ctx)
	}
	return 0, nil
}

func TestNotificationServer_List(t *testing.T) {
	srv := notification.NewNotificationServer(&mockNotificationService{
		listFn: func(_ context.Context, page domain.Page) (*notification.NotificationList, error) {
			return &notification.NotificationList{
				Notifications: []*domain.Notification{
					{ID: idgen.NewID(), Type: "message.received", ActorUID: "bob", SubjectID: "msg-1"},
				},
				Users:      map[string]sso.User{"bob": {UID: "bob", Nickname: "Bob"}},
				NextCursor: "next",
			}, nil
		},
	})
	ctx := domain.WithActor(context.Background(), "u1")
	resp, err := srv.ListNotifications(ctx, &aeropb.ListNotificationsRequest{
		Page: &aeropb.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(resp.Notifications))
	}
	if resp.NextCursor != "next" {
		t.Errorf("next_cursor = %q", resp.NextCursor)
	}
}

func TestNotificationServer_MarkRead(t *testing.T) {
	id := idgen.NewID()
	srv := notification.NewNotificationServer(&mockNotificationService{
		markReadFn: func(_ context.Context, gotID domain.ID) error {
			if gotID != id {
				t.Errorf("id = %s, want %s", gotID, id)
			}
			return nil
		},
	})
	ctx := domain.WithActor(context.Background(), "u1")
	_, err := srv.MarkRead(ctx, &aeropb.MarkNotificationReadRequest{Id: id.String()})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNotificationServer_MarkRead_InvalidID(t *testing.T) {
	srv := notification.NewNotificationServer(&mockNotificationService{})
	ctx := domain.WithActor(context.Background(), "u1")
	_, err := srv.MarkRead(ctx, &aeropb.MarkNotificationReadRequest{Id: "not-a-uuid"})
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
	st := status.Convert(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", st.Code())
	}
}

func TestNotificationServer_MarkAllRead(t *testing.T) {
	called := false
	srv := notification.NewNotificationServer(&mockNotificationService{
		markAllReadFn: func(_ context.Context) error {
			called = true
			return nil
		},
	})
	ctx := domain.WithActor(context.Background(), "u1")
	_, err := srv.MarkAllRead(ctx, &aeropb.MarkAllNotificationsReadRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("MarkAllRead not called")
	}
}

func TestNotificationServer_UnreadCount(t *testing.T) {
	srv := notification.NewNotificationServer(&mockNotificationService{
		unreadCountFn: func(_ context.Context) (int64, error) {
			return 42, nil
		},
	})
	ctx := domain.WithActor(context.Background(), "u1")
	resp, err := srv.UnreadCount(ctx, &aeropb.NotificationUnreadCountRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.UnreadCount != 42 {
		t.Errorf("unread_count = %d, want 42", resp.UnreadCount)
	}
}
