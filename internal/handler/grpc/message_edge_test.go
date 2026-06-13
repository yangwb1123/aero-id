package grpc_test

// message_edge_test.go covers gaps not addressed by parity_edit_test.go:
//
//   - EditMessage        – invalid-id path
//   - DeleteMessage      – not-found path + invalid-id path
//   - MarkConversationRead – error-propagation path
//   - UnreadCount        – already in parity_test.go; no new test needed
//
// The success paths for Edit/Delete/MarkConversationRead and the UnreadCount
// happy path live in parity_edit_test.go / parity_test.go respectively.

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

// msgEdgeSvc wires only the methods exercised in this file.
type msgEdgeSvc struct {
	service.MessageService
	editFn     func(context.Context, domain.ID, string) (*domain.Message, error)
	deleteFn   func(context.Context, domain.ID) error
	convReadFn func(context.Context, string) error
}

func (m msgEdgeSvc) Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error) {
	return m.editFn(ctx, id, content)
}
func (m msgEdgeSvc) Delete(ctx context.Context, id domain.ID) error { return m.deleteFn(ctx, id) }
func (m msgEdgeSvc) MarkConversationRead(ctx context.Context, with string) error {
	return m.convReadFn(ctx, with)
}

// ---- EditMessage edge cases ----

func TestGRPC_EditMessage_InvalidID(t *testing.T) {
	conn := dial(t, grpcsrv.NewMessageServer(msgEdgeSvc{}))
	client := aeropb.NewMessageServiceClient(conn)

	_, err := client.EditMessage(authCtx(), &aeropb.EditMessageRequest{Id: "not-a-uuid", Content: "x"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// ---- DeleteMessage edge cases ----

func TestGRPC_DeleteMessage_NotFound(t *testing.T) {
	svc := msgEdgeSvc{deleteFn: func(context.Context, domain.ID) error {
		return apperr.NotFound("message.not_found", "gone")
	}}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
	client := aeropb.NewMessageServiceClient(conn)

	_, err := client.DeleteMessage(authCtx(), &aeropb.DeleteMessageRequest{Id: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPC_DeleteMessage_InvalidID(t *testing.T) {
	conn := dial(t, grpcsrv.NewMessageServer(msgEdgeSvc{}))
	client := aeropb.NewMessageServiceClient(conn)

	_, err := client.DeleteMessage(authCtx(), &aeropb.DeleteMessageRequest{Id: "bad"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// ---- MarkConversationRead error propagation ----

func TestGRPC_MarkConversationRead_PropagatesError(t *testing.T) {
	svc := msgEdgeSvc{convReadFn: func(context.Context, string) error {
		return apperr.NotFound("message.not_found", "no conversation")
	}}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
	client := aeropb.NewMessageServiceClient(conn)

	_, err := client.MarkConversationRead(authCtx(), &aeropb.MarkConversationReadRequest{WithUid: "b"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

// ---- UnreadCount error propagation ----

func TestGRPC_MessageUnreadCount_PropagatesError(t *testing.T) {
	fakeSvc := unreadCountErrSvc{countFn: func(context.Context) (int64, error) {
		return 0, apperr.Internal("msg.err", "db down")
	}}

	conn := dial(t, grpcsrv.NewMessageServer(fakeSvc))
	client := aeropb.NewMessageServiceClient(conn)

	_, err := client.UnreadCount(authCtx(), &aeropb.UnreadCountRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}

// unreadCountErrSvc is a minimal fake whose only meaningful method is UnreadCount.
type unreadCountErrSvc struct {
	service.MessageService
	countFn func(context.Context) (int64, error)
}

func (s unreadCountErrSvc) UnreadCount(ctx context.Context) (int64, error) {
	return s.countFn(ctx)
}
