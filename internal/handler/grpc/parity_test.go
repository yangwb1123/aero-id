package grpc_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aero/aero-id/internal/domain"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
)

// ---- file sharing / presign fakes ----

type shareFileSvc struct {
	service.FileService
	shareFn   func(context.Context, domain.ID, []string) error
	sharedFn  func(context.Context, domain.Page) (*service.FileList, error)
	unshareFn func(context.Context, domain.ID, string) error
}

func (f shareFileSvc) ShareFile(ctx context.Context, id domain.ID, uids []string) error {
	return f.shareFn(ctx, id, uids)
}
func (f shareFileSvc) UnshareFile(ctx context.Context, id domain.ID, uid string) error {
	return f.unshareFn(ctx, id, uid)
}
func (f shareFileSvc) ListSharedWithMe(ctx context.Context, p domain.Page) (*service.FileList, error) {
	return f.sharedFn(ctx, p)
}

func TestGRPC_ShareFile_Success(t *testing.T) {
	var gotUIDs []string
	svc := shareFileSvc{shareFn: func(_ context.Context, _ domain.ID, uids []string) error {
		gotUIDs = uids
		return nil
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)
	_, err := client.ShareFile(authCtx(), &aeropb.ShareFileRequest{FileId: idgen.NewID().String(), GranteeUids: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotUIDs) != 2 || gotUIDs[0] != "a" {
		t.Fatalf("grantees = %v", gotUIDs)
	}
}

func TestGRPC_ShareFile_NonOwnerPermissionDenied(t *testing.T) {
	svc := shareFileSvc{shareFn: func(context.Context, domain.ID, []string) error {
		return apperr.Forbidden("file.forbidden", "not owner")
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)
	_, err := client.ShareFile(authCtx(), &aeropb.ShareFileRequest{FileId: idgen.NewID().String(), GranteeUids: []string{"a"}})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
	assertGRPCErrorReason(t, err, "file.forbidden")
}

func TestGRPC_ListSharedWithMe(t *testing.T) {
	svc := shareFileSvc{sharedFn: func(context.Context, domain.Page) (*service.FileList, error) {
		return &service.FileList{Files: []*domain.File{{ID: idgen.NewID(), OwnerUID: "other"}}, NextCursor: "c1"}, nil
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)
	resp, err := client.ListSharedWithMe(authCtx(), &aeropb.ListSharedWithMeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Files) != 1 || resp.NextCursor != "c1" || resp.Files[0].OwnerUid != "other" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

// presignFileSvc isolates PresignUpload/CompleteUpload since their service signatures
// carry a time.Duration the simpler fake above can't model cleanly.
type presignFileSvc struct {
	service.FileService
	presignFn  func(context.Context, service.PresignUploadInput) (*service.PresignUploadResult, error)
	completeFn func(context.Context, service.CompleteUploadInput) (*domain.File, error)
}

func (f presignFileSvc) PresignUpload(ctx context.Context, in service.PresignUploadInput, _ time.Duration) (*service.PresignUploadResult, error) {
	return f.presignFn(ctx, in)
}
func (f presignFileSvc) CompleteUpload(ctx context.Context, in service.CompleteUploadInput) (*domain.File, error) {
	return f.completeFn(ctx, in)
}

func TestGRPC_PresignUpload(t *testing.T) {
	fid := idgen.NewID()
	svc := presignFileSvc{presignFn: func(_ context.Context, in service.PresignUploadInput) (*service.PresignUploadResult, error) {
		if in.Name != "f.txt" {
			t.Fatalf("name = %q", in.Name)
		}
		return &service.PresignUploadResult{FileID: fid, UploadURL: "http://put", StorageKey: "k"}, nil
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)
	resp, err := client.PresignUpload(authCtx(), &aeropb.PresignUploadRequest{Name: "f.txt", MimeType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.FileId != fid.String() || resp.UploadUrl != "http://put" || resp.StorageKey != "k" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestGRPC_CompleteUpload(t *testing.T) {
	fid := idgen.NewID()
	svc := presignFileSvc{completeFn: func(_ context.Context, in service.CompleteUploadInput) (*domain.File, error) {
		return &domain.File{ID: in.FileID, OwnerUID: "u1", Name: in.Name}, nil
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)
	resp, err := client.CompleteUpload(authCtx(), &aeropb.CompleteUploadRequest{FileId: fid.String(), Name: "f.txt", StorageKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.File.Id != fid.String() || resp.File.Name != "f.txt" {
		t.Fatalf("unexpected file: %+v", resp.File)
	}
}

// ---- comment reactions / moderation ----

type reactCommentSvc struct {
	service.CommentService
	likeFn   func(context.Context, domain.ID) (*service.ReactionResult, error)
	reportFn func(context.Context, domain.ID, string) error
	hideFn   func(context.Context, domain.ID) error
}

func (c reactCommentSvc) Like(ctx context.Context, id domain.ID) (*service.ReactionResult, error) {
	return c.likeFn(ctx, id)
}
func (c reactCommentSvc) Report(ctx context.Context, id domain.ID, reason string) error {
	return c.reportFn(ctx, id, reason)
}
func (c reactCommentSvc) Hide(ctx context.Context, id domain.ID) error { return c.hideFn(ctx, id) }

func TestGRPC_LikeComment_ReturnsCount(t *testing.T) {
	svc := reactCommentSvc{likeFn: func(context.Context, domain.ID) (*service.ReactionResult, error) {
		return &service.ReactionResult{Liked: true, LikeCount: 7}, nil
	}}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	resp, err := client.LikeComment(authCtx(), &aeropb.LikeCommentRequest{CommentId: idgen.NewID().String()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.LikeCount != 7 {
		t.Fatalf("like_count = %d, want 7", resp.LikeCount)
	}
}

func TestGRPC_ReportComment_DuplicateConflict(t *testing.T) {
	svc := reactCommentSvc{reportFn: func(context.Context, domain.ID, string) error {
		return apperr.Conflict("comment.already_reported", "dup")
	}}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	_, err := client.ReportComment(authCtx(), &aeropb.ReportCommentRequest{CommentId: idgen.NewID().String(), Reason: "spam"})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("code = %v, want AlreadyExists", status.Code(err))
	}
	assertGRPCErrorReason(t, err, "comment.already_reported")
}

func assertGRPCErrorReason(t *testing.T, err error, want string) {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error is not a gRPC status: %v", err)
	}
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Reason == want {
			return
		}
	}
	t.Fatalf("gRPC error details = %#v, want ErrorInfo reason %q", st.Details(), want)
}

func TestGRPC_HideComment_Success(t *testing.T) {
	svc := reactCommentSvc{hideFn: func(context.Context, domain.ID) error { return nil }}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	if _, err := client.HideComment(authCtx(), &aeropb.HideCommentRequest{CommentId: idgen.NewID().String()}); err != nil {
		t.Fatal(err)
	}
}

// ---- message unread count ----

type unreadMsgSvc struct {
	service.MessageService
	countFn func(context.Context) (int64, error)
}

func (m unreadMsgSvc) UnreadCount(ctx context.Context) (int64, error) { return m.countFn(ctx) }

func TestGRPC_MessageUnreadCount(t *testing.T) {
	svc := unreadMsgSvc{countFn: func(context.Context) (int64, error) { return 42, nil }}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
	client := aeropb.NewMessageServiceClient(conn)
	resp, err := client.UnreadCount(authCtx(), &aeropb.UnreadCountRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.UnreadCount != 42 {
		t.Fatalf("unread_count = %d, want 42", resp.UnreadCount)
	}
}

// ---- notifications ----

type notifSvc struct {
	service.NotificationService
	listFn    func(context.Context, domain.Page) (*service.NotificationList, error)
	markAllFn func(context.Context) error
	countFn   func(context.Context) (int64, error)
}

func (n notifSvc) List(ctx context.Context, p domain.Page) (*service.NotificationList, error) {
	return n.listFn(ctx, p)
}
func (n notifSvc) MarkAllRead(ctx context.Context) error          { return n.markAllFn(ctx) }
func (n notifSvc) UnreadCount(ctx context.Context) (int64, error) { return n.countFn(ctx) }

func TestGRPC_ListNotifications_Shape(t *testing.T) {
	svc := notifSvc{listFn: func(context.Context, domain.Page) (*service.NotificationList, error) {
		return &service.NotificationList{
			Notifications: []*domain.Notification{{ID: idgen.NewID(), RecipientUID: "u1", Type: "comment.reply", ActorUID: "u2"}},
			Users:         map[string]sso.User{"u2": {UID: "u2", Nickname: "Bob"}},
			NextCursor:    "nc",
		}, nil
	}}
	conn := dial(t, grpcsrv.NewNotificationServer(svc))
	client := aeropb.NewNotificationServiceClient(conn)
	resp, err := client.ListNotifications(authCtx(), &aeropb.ListNotificationsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Notifications) != 1 || resp.Notifications[0].Type != "comment.reply" {
		t.Fatalf("notifications = %+v", resp.Notifications)
	}
	if resp.NextCursor != "nc" {
		t.Fatalf("next_cursor = %q", resp.NextCursor)
	}
	if u, ok := resp.Users["u2"]; !ok || u.Nickname != "Bob" {
		t.Fatalf("users = %+v", resp.Users)
	}
}

func TestGRPC_MarkAllNotificationsRead(t *testing.T) {
	called := false
	svc := notifSvc{markAllFn: func(context.Context) error { called = true; return nil }}
	conn := dial(t, grpcsrv.NewNotificationServer(svc))
	client := aeropb.NewNotificationServiceClient(conn)
	if _, err := client.MarkAllRead(authCtx(), &aeropb.MarkAllNotificationsReadRequest{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("MarkAllRead not called")
	}
}
