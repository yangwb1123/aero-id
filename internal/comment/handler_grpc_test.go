package comment_test

import (
	"context"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aero/aero-id/internal/domain"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"

	"github.com/aero/aero-id/internal/comment"
)

type validator struct{ uid string }

func (v validator) ValidateToken(context.Context, string) (string, error) { return v.uid, nil }

func dial(t *testing.T, regs ...grpcsrv.ServiceRegistrar) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpcsrv.NewServer(validator{uid: "u1"}, zap.NewNop(), nil, regs...)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func authCtx() context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer t"))
}

type commentEdgeSvc struct {
	comment.Service
	unlikeFn func(context.Context, domain.ID) (*comment.ReactionResult, error)
	hideFn   func(context.Context, domain.ID) error
	unhideFn func(context.Context, domain.ID) error
	reportFn func(context.Context, domain.ID, string) error
	deleteFn func(context.Context, domain.ID) error
}

func (c commentEdgeSvc) Unlike(ctx context.Context, id domain.ID) (*comment.ReactionResult, error) {
	return c.unlikeFn(ctx, id)
}
func (c commentEdgeSvc) Hide(ctx context.Context, id domain.ID) error   { return c.hideFn(ctx, id) }
func (c commentEdgeSvc) Unhide(ctx context.Context, id domain.ID) error { return c.unhideFn(ctx, id) }
func (c commentEdgeSvc) Report(ctx context.Context, id domain.ID, reason string) error {
	return c.reportFn(ctx, id, reason)
}
func (c commentEdgeSvc) Delete(ctx context.Context, id domain.ID) error { return c.deleteFn(ctx, id) }

func TestGRPC_UnlikeComment_ReturnsCount(t *testing.T) {
	svc := commentEdgeSvc{unlikeFn: func(_ context.Context, _ domain.ID) (*comment.ReactionResult, error) {
		return &comment.ReactionResult{Liked: false, LikeCount: 3}, nil
	}}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)

	resp, err := client.UnlikeComment(authCtx(), &aeropb.UnlikeCommentRequest{CommentId: idgen.NewID().String()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.LikeCount != 3 {
		t.Fatalf("like_count = %d, want 3", resp.LikeCount)
	}
}

func TestGRPC_UnlikeComment_IdempotentAlreadyUnliked(t *testing.T) {
	calls := 0
	svc := commentEdgeSvc{unlikeFn: func(_ context.Context, _ domain.ID) (*comment.ReactionResult, error) {
		calls++
		return &comment.ReactionResult{Liked: false, LikeCount: 0}, nil
	}}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)

	cid := idgen.NewID().String()
	for i := 0; i < 2; i++ {
		if _, err := client.UnlikeComment(authCtx(), &aeropb.UnlikeCommentRequest{CommentId: cid}); err != nil {
			t.Fatalf("call %d failed: %v", i+1, err)
		}
	}
	if calls != 2 {
		t.Fatalf("service called %d times, want 2", calls)
	}
}

func TestGRPC_UnlikeComment_InvalidID(t *testing.T) {
	conn := dial(t, comment.NewCommentServer(commentEdgeSvc{}))
	client := aeropb.NewCommentServiceClient(conn)

	_, err := client.UnlikeComment(authCtx(), &aeropb.UnlikeCommentRequest{CommentId: "not-a-uuid"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGRPC_HideComment_NotFound(t *testing.T) {
	svc := commentEdgeSvc{hideFn: func(context.Context, domain.ID) error {
		return apperr.NotFound("comment.not_found", "gone")
	}}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)

	_, err := client.HideComment(authCtx(), &aeropb.HideCommentRequest{CommentId: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPC_UnhideComment_Success(t *testing.T) {
	called := false
	svc := commentEdgeSvc{unhideFn: func(context.Context, domain.ID) error { called = true; return nil }}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)

	if _, err := client.UnhideComment(authCtx(), &aeropb.UnhideCommentRequest{CommentId: idgen.NewID().String()}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Unhide not called")
	}
}

func TestGRPC_UnhideComment_NotFound(t *testing.T) {
	svc := commentEdgeSvc{unhideFn: func(context.Context, domain.ID) error {
		return apperr.NotFound("comment.not_found", "gone")
	}}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)

	_, err := client.UnhideComment(authCtx(), &aeropb.UnhideCommentRequest{CommentId: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPC_UnhideComment_InvalidID(t *testing.T) {
	conn := dial(t, comment.NewCommentServer(commentEdgeSvc{}))
	client := aeropb.NewCommentServiceClient(conn)

	_, err := client.UnhideComment(authCtx(), &aeropb.UnhideCommentRequest{CommentId: "bad"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGRPC_ReportComment_Success(t *testing.T) {
	var gotReason string
	svc := commentEdgeSvc{reportFn: func(_ context.Context, _ domain.ID, reason string) error {
		gotReason = reason
		return nil
	}}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)

	if _, err := client.ReportComment(authCtx(), &aeropb.ReportCommentRequest{
		CommentId: idgen.NewID().String(),
		Reason:    "spam",
	}); err != nil {
		t.Fatal(err)
	}
	if gotReason != "spam" {
		t.Fatalf("reason = %q, want spam", gotReason)
	}
}

func TestGRPC_DeleteComment_Success(t *testing.T) {
	called := false
	svc := commentEdgeSvc{deleteFn: func(context.Context, domain.ID) error { called = true; return nil }}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)

	if _, err := client.DeleteComment(authCtx(), &aeropb.DeleteCommentRequest{Id: idgen.NewID().String()}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Delete not called")
	}
}

func TestGRPC_DeleteComment_NotFound(t *testing.T) {
	svc := commentEdgeSvc{deleteFn: func(context.Context, domain.ID) error {
		return apperr.NotFound("comment.not_found", "gone")
	}}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)

	_, err := client.DeleteComment(authCtx(), &aeropb.DeleteCommentRequest{Id: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPC_DeleteComment_InvalidID(t *testing.T) {
	conn := dial(t, comment.NewCommentServer(commentEdgeSvc{}))
	client := aeropb.NewCommentServiceClient(conn)

	_, err := client.DeleteComment(authCtx(), &aeropb.DeleteCommentRequest{Id: "nope"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

type reactCommentSvc struct {
	comment.Service
	likeFn   func(context.Context, domain.ID) (*comment.ReactionResult, error)
	reportFn func(context.Context, domain.ID, string) error
	hideFn   func(context.Context, domain.ID) error
}

func (c reactCommentSvc) Like(ctx context.Context, id domain.ID) (*comment.ReactionResult, error) {
	return c.likeFn(ctx, id)
}
func (c reactCommentSvc) Report(ctx context.Context, id domain.ID, reason string) error {
	return c.reportFn(ctx, id, reason)
}
func (c reactCommentSvc) Hide(ctx context.Context, id domain.ID) error { return c.hideFn(ctx, id) }

func TestGRPC_LikeComment_ReturnsCount(t *testing.T) {
	svc := reactCommentSvc{likeFn: func(context.Context, domain.ID) (*comment.ReactionResult, error) {
		return &comment.ReactionResult{Liked: true, LikeCount: 7}, nil
	}}
	conn := dial(t, comment.NewCommentServer(svc))
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
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	_, err := client.ReportComment(authCtx(), &aeropb.ReportCommentRequest{CommentId: idgen.NewID().String(), Reason: "spam"})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("code = %v, want AlreadyExists", status.Code(err))
	}
}

func TestGRPC_HideComment_Success(t *testing.T) {
	svc := reactCommentSvc{hideFn: func(context.Context, domain.ID) error { return nil }}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	if _, err := client.HideComment(authCtx(), &aeropb.HideCommentRequest{CommentId: idgen.NewID().String()}); err != nil {
		t.Fatal(err)
	}
}

type editCommentSvc struct {
	comment.Service
	editFn    func(context.Context, domain.ID, string) (*comment.Comment, error)
	listFn    func(context.Context, domain.ID, *domain.ReportStatus, domain.Page) (*comment.ReportList, error)
	resolveFn func(context.Context, domain.ID, domain.ReportStatus) error
}

func (c editCommentSvc) Edit(ctx context.Context, id domain.ID, content string) (*comment.Comment, error) {
	return c.editFn(ctx, id, content)
}
func (c editCommentSvc) ListReports(ctx context.Context, id domain.ID, st *domain.ReportStatus, p domain.Page) (*comment.ReportList, error) {
	return c.listFn(ctx, id, st, p)
}
func (c editCommentSvc) ResolveReport(ctx context.Context, id domain.ID, st domain.ReportStatus) error {
	return c.resolveFn(ctx, id, st)
}

func TestGRPC_EditComment_Success(t *testing.T) {
	edited := time.Now().UTC()
	svc := editCommentSvc{editFn: func(_ context.Context, id domain.ID, content string) (*comment.Comment, error) {
		return &comment.Comment{ID: id, OwnerUID: "u1", Content: content, EditedAt: &edited}, nil
	}}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	resp, err := client.EditComment(authCtx(), &aeropb.EditCommentRequest{Id: idgen.NewID().String(), Content: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Comment.Content != "new" {
		t.Fatalf("content = %q", resp.Comment.Content)
	}
	if resp.Comment.EditedAt == nil {
		t.Fatal("edited_at not propagated")
	}
}

func TestGRPC_EditComment_NonAuthorPermissionDenied(t *testing.T) {
	svc := editCommentSvc{editFn: func(context.Context, domain.ID, string) (*comment.Comment, error) {
		return nil, apperr.Forbidden("comment.forbidden", "not author")
	}}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	_, err := client.EditComment(authCtx(), &aeropb.EditCommentRequest{Id: idgen.NewID().String(), Content: "x"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
}

func TestGRPC_EditComment_InvalidID(t *testing.T) {
	conn := dial(t, comment.NewCommentServer(editCommentSvc{}))
	client := aeropb.NewCommentServiceClient(conn)
	_, err := client.EditComment(authCtx(), &aeropb.EditCommentRequest{Id: "nope", Content: "x"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGRPC_ListReports_Shape(t *testing.T) {
	cid := idgen.NewID()
	resolved := time.Now().UTC()
	var gotStatus *domain.ReportStatus
	svc := editCommentSvc{listFn: func(_ context.Context, _ domain.ID, st *domain.ReportStatus, _ domain.Page) (*comment.ReportList, error) {
		gotStatus = st
		return &comment.ReportList{
			Reports: []*comment.CommentReport{{
				ID: idgen.NewID(), CommentID: cid, ReporterUID: "u2", Reason: "spam",
				Status: domain.ReportStatusResolved, ResolvedAt: &resolved, ResolvedBy: "u1",
			}},
			Users:      nil,
			NextCursor: "nc",
		}, nil
	}}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	filter := "resolved"
	resp, err := client.ListReports(authCtx(), &aeropb.ListReportsRequest{CommentId: cid.String(), Status: &filter})
	if err != nil {
		t.Fatal(err)
	}
	if gotStatus == nil || *gotStatus != domain.ReportStatusResolved {
		t.Fatalf("status filter not forwarded: %v", gotStatus)
	}
	if len(resp.Reports) != 1 || resp.Reports[0].Status != "resolved" || resp.Reports[0].Reason != "spam" {
		t.Fatalf("reports = %+v", resp.Reports)
	}
	if resp.Reports[0].ResolvedAt == nil || resp.Reports[0].ResolvedBy != "u1" {
		t.Fatalf("resolved fields = %+v", resp.Reports[0])
	}
	if resp.NextCursor != "nc" {
		t.Fatalf("next_cursor = %q", resp.NextCursor)
	}
}

func TestGRPC_ResolveReport_Success(t *testing.T) {
	var gotStatus domain.ReportStatus
	svc := editCommentSvc{resolveFn: func(_ context.Context, _ domain.ID, st domain.ReportStatus) error {
		gotStatus = st
		return nil
	}}
	conn := dial(t, comment.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	if _, err := client.ResolveReport(authCtx(), &aeropb.ResolveReportRequest{ReportId: idgen.NewID().String(), Status: "dismissed"}); err != nil {
		t.Fatal(err)
	}
	if gotStatus != domain.ReportStatusDismissed {
		t.Fatalf("status = %v, want dismissed", gotStatus)
	}
}

func TestGRPC_ResolveReport_InvalidStatus(t *testing.T) {
	conn := dial(t, comment.NewCommentServer(editCommentSvc{}))
	client := aeropb.NewCommentServiceClient(conn)
	_, err := client.ResolveReport(authCtx(), &aeropb.ResolveReportRequest{ReportId: idgen.NewID().String(), Status: "bogus"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}
