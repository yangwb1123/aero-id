package grpc_test

// notification_test.go covers:
//
//   - ListNotifications  – cursor pagination param is forwarded
//   - UnreadCount        – success + error propagation
//   - MarkRead           – success + not-found + invalid-id
//   - MarkAllRead        – error propagation (success in parity_test.go)

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aero/aero-id/internal/domain"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

// notifEdgeSvc wires only the methods exercised in this file.
type notifEdgeSvc struct {
	service.NotificationService
	listFn     func(context.Context, domain.Page) (*service.NotificationList, error)
	markReadFn func(context.Context, domain.ID) error
	markAllFn  func(context.Context) error
	countFn    func(context.Context) (int64, error)
}

func (n notifEdgeSvc) List(ctx context.Context, p domain.Page) (*service.NotificationList, error) {
	return n.listFn(ctx, p)
}
func (n notifEdgeSvc) MarkRead(ctx context.Context, id domain.ID) error {
	return n.markReadFn(ctx, id)
}
func (n notifEdgeSvc) MarkAllRead(ctx context.Context) error          { return n.markAllFn(ctx) }
func (n notifEdgeSvc) UnreadCount(ctx context.Context) (int64, error) { return n.countFn(ctx) }

// ---- ListNotifications – cursor param forwarded ----

func TestGRPC_ListNotifications_CursorForwarded(t *testing.T) {
	var gotPage domain.Page
	svc := notifEdgeSvc{listFn: func(_ context.Context, p domain.Page) (*service.NotificationList, error) {
		gotPage = p
		return &service.NotificationList{}, nil
	}}
	conn := dial(t, grpcsrv.NewNotificationServer(svc))
	client := aeropb.NewNotificationServiceClient(conn)

	_, err := client.ListNotifications(authCtx(), &aeropb.ListNotificationsRequest{
		Page: &aeropb.Page{Limit: 10, Cursor: "tok123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPage.Limit != 10 || gotPage.Cursor != "tok123" {
		t.Fatalf("page = %+v, want {Limit:10 Cursor:tok123}", gotPage)
	}
}

func TestGRPC_ListNotifications_ErrorPropagated(t *testing.T) {
	svc := notifEdgeSvc{listFn: func(context.Context, domain.Page) (*service.NotificationList, error) {
		return nil, apperr.Internal("notif.err", "db down")
	}}
	conn := dial(t, grpcsrv.NewNotificationServer(svc))
	client := aeropb.NewNotificationServiceClient(conn)

	_, err := client.ListNotifications(authCtx(), &aeropb.ListNotificationsRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}

// ---- UnreadCount ----

func TestGRPC_NotificationUnreadCount_ReturnsCount(t *testing.T) {
	svc := notifEdgeSvc{countFn: func(context.Context) (int64, error) { return 5, nil }}
	conn := dial(t, grpcsrv.NewNotificationServer(svc))
	client := aeropb.NewNotificationServiceClient(conn)

	resp, err := client.UnreadCount(authCtx(), &aeropb.NotificationUnreadCountRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.UnreadCount != 5 {
		t.Fatalf("unread_count = %d, want 5", resp.UnreadCount)
	}
}

func TestGRPC_NotificationUnreadCount_ErrorPropagated(t *testing.T) {
	svc := notifEdgeSvc{countFn: func(context.Context) (int64, error) {
		return 0, apperr.Internal("notif.err", "db down")
	}}
	conn := dial(t, grpcsrv.NewNotificationServer(svc))
	client := aeropb.NewNotificationServiceClient(conn)

	_, err := client.UnreadCount(authCtx(), &aeropb.NotificationUnreadCountRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}

// ---- MarkRead ----

func TestGRPC_NotificationMarkRead_Success(t *testing.T) {
	var gotID domain.ID
	svc := notifEdgeSvc{markReadFn: func(_ context.Context, id domain.ID) error {
		gotID = id
		return nil
	}}
	conn := dial(t, grpcsrv.NewNotificationServer(svc))
	client := aeropb.NewNotificationServiceClient(conn)

	nid := idgen.NewID()
	if _, err := client.MarkRead(authCtx(), &aeropb.MarkNotificationReadRequest{Id: nid.String()}); err != nil {
		t.Fatal(err)
	}
	if gotID != nid {
		t.Fatalf("id forwarded = %v, want %v", gotID, nid)
	}
}

func TestGRPC_NotificationMarkRead_NotFound(t *testing.T) {
	svc := notifEdgeSvc{markReadFn: func(context.Context, domain.ID) error {
		return apperr.NotFound("notification.not_found", "gone")
	}}
	conn := dial(t, grpcsrv.NewNotificationServer(svc))
	client := aeropb.NewNotificationServiceClient(conn)

	_, err := client.MarkRead(authCtx(), &aeropb.MarkNotificationReadRequest{Id: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPC_NotificationMarkRead_InvalidID(t *testing.T) {
	conn := dial(t, grpcsrv.NewNotificationServer(notifEdgeSvc{}))
	client := aeropb.NewNotificationServiceClient(conn)

	_, err := client.MarkRead(authCtx(), &aeropb.MarkNotificationReadRequest{Id: "not-a-uuid"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// ---- MarkAllRead error path (success in parity_test.go) ----

func TestGRPC_MarkAllNotificationsRead_ErrorPropagated(t *testing.T) {
	svc := notifEdgeSvc{markAllFn: func(context.Context) error {
		return apperr.Internal("notif.err", "db down")
	}}
	conn := dial(t, grpcsrv.NewNotificationServer(svc))
	client := aeropb.NewNotificationServiceClient(conn)

	_, err := client.MarkAllRead(authCtx(), &aeropb.MarkAllNotificationsReadRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}
