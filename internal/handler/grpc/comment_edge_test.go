package grpc_test

// comment_edge_test.go covers gaps not addressed by parity_test.go /
// parity_edit_test.go:
//
//   - UnlikeComment     – success + idempotent second call behaviour
//   - UnhideComment     – success + not-found + invalid-id
//   - HideComment       – not-found path (success already in parity_test.go)
//   - ReportComment     – happy path (duplicate/Conflict already in parity_test.go)
//   - DeleteComment     – success + not-found + invalid-id

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

// commentEdgeSvc wires only the methods exercised in this file.
type commentEdgeSvc struct {
	service.CommentService
	unlikeFn func(context.Context, domain.ID) (*service.ReactionResult, error)
	hideFn   func(context.Context, domain.ID) error
	unhideFn func(context.Context, domain.ID) error
	reportFn func(context.Context, domain.ID, string) error
	deleteFn func(context.Context, domain.ID) error
}

func (c commentEdgeSvc) Unlike(ctx context.Context, id domain.ID) (*service.ReactionResult, error) {
	return c.unlikeFn(ctx, id)
}
func (c commentEdgeSvc) Hide(ctx context.Context, id domain.ID) error   { return c.hideFn(ctx, id) }
func (c commentEdgeSvc) Unhide(ctx context.Context, id domain.ID) error { return c.unhideFn(ctx, id) }
func (c commentEdgeSvc) Report(ctx context.Context, id domain.ID, reason string) error {
	return c.reportFn(ctx, id, reason)
}
func (c commentEdgeSvc) Delete(ctx context.Context, id domain.ID) error { return c.deleteFn(ctx, id) }

// ---- UnlikeComment ----

func TestGRPC_UnlikeComment_ReturnsCount(t *testing.T) {
	svc := commentEdgeSvc{unlikeFn: func(_ context.Context, _ domain.ID) (*service.ReactionResult, error) {
		return &service.ReactionResult{Liked: false, LikeCount: 3}, nil
	}}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)

	resp, err := client.UnlikeComment(authCtx(), &aeropb.UnlikeCommentRequest{CommentId: idgen.NewID().String()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.LikeCount != 3 {
		t.Fatalf("like_count = %d, want 3", resp.LikeCount)
	}
}

// Idempotent second Unlike: service returns the same count without error on both calls.
func TestGRPC_UnlikeComment_IdempotentAlreadyUnliked(t *testing.T) {
	calls := 0
	svc := commentEdgeSvc{unlikeFn: func(_ context.Context, _ domain.ID) (*service.ReactionResult, error) {
		calls++
		return &service.ReactionResult{Liked: false, LikeCount: 0}, nil
	}}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
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
	conn := dial(t, grpcsrv.NewCommentServer(commentEdgeSvc{}))
	client := aeropb.NewCommentServiceClient(conn)

	_, err := client.UnlikeComment(authCtx(), &aeropb.UnlikeCommentRequest{CommentId: "not-a-uuid"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// ---- HideComment (not-found path; success covered in parity_test.go) ----

func TestGRPC_HideComment_NotFound(t *testing.T) {
	svc := commentEdgeSvc{hideFn: func(context.Context, domain.ID) error {
		return apperr.NotFound("comment.not_found", "gone")
	}}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)

	_, err := client.HideComment(authCtx(), &aeropb.HideCommentRequest{CommentId: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

// ---- UnhideComment ----

func TestGRPC_UnhideComment_Success(t *testing.T) {
	called := false
	svc := commentEdgeSvc{unhideFn: func(context.Context, domain.ID) error { called = true; return nil }}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
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
	conn := dial(t, grpcsrv.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)

	_, err := client.UnhideComment(authCtx(), &aeropb.UnhideCommentRequest{CommentId: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPC_UnhideComment_InvalidID(t *testing.T) {
	conn := dial(t, grpcsrv.NewCommentServer(commentEdgeSvc{}))
	client := aeropb.NewCommentServiceClient(conn)

	_, err := client.UnhideComment(authCtx(), &aeropb.UnhideCommentRequest{CommentId: "bad"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// ---- ReportComment (success path; duplicate→AlreadyExists in parity_test.go) ----

func TestGRPC_ReportComment_Success(t *testing.T) {
	var gotReason string
	svc := commentEdgeSvc{reportFn: func(_ context.Context, _ domain.ID, reason string) error {
		gotReason = reason
		return nil
	}}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
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

// ---- DeleteComment ----

func TestGRPC_DeleteComment_Success(t *testing.T) {
	called := false
	svc := commentEdgeSvc{deleteFn: func(context.Context, domain.ID) error { called = true; return nil }}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
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
	conn := dial(t, grpcsrv.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)

	_, err := client.DeleteComment(authCtx(), &aeropb.DeleteCommentRequest{Id: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPC_DeleteComment_InvalidID(t *testing.T) {
	conn := dial(t, grpcsrv.NewCommentServer(commentEdgeSvc{}))
	client := aeropb.NewCommentServiceClient(conn)

	_, err := client.DeleteComment(authCtx(), &aeropb.DeleteCommentRequest{Id: "nope"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}
