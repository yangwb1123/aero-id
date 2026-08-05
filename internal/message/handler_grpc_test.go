package message_test

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
	"github.com/aero/aero-id/internal/message"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type msgSvc struct {
	message.Service
	sendFn   func(context.Context, message.SendMessageInput) (*domain.Message, error)
	getFn    func(context.Context, domain.ID) (*domain.Message, error)
	inboxFn  func(context.Context, domain.Page, bool) (*message.MessageList, error)
	convFn   func(context.Context, string, domain.Page) (*message.MessageList, error)
	readFn   func(context.Context, domain.ID) error
	editFn   func(context.Context, domain.ID, string) (*domain.Message, error)
	deleteFn func(context.Context, domain.ID) error
	convRdfn func(context.Context, string) error
	countFn  func(context.Context) (int64, error)
	reactFn  func(context.Context, domain.ID, string) error
	unrctFn  func(context.Context, domain.ID, string) error
}

func (m msgSvc) Send(ctx context.Context, in message.SendMessageInput) (*domain.Message, error) {
	return m.sendFn(ctx, in)
}
func (m msgSvc) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
	return m.getFn(ctx, id)
}
func (m msgSvc) Inbox(ctx context.Context, p domain.Page, u bool) (*message.MessageList, error) {
	return m.inboxFn(ctx, p, u)
}
func (m msgSvc) Conversation(ctx context.Context, w string, p domain.Page) (*message.MessageList, error) {
	return m.convFn(ctx, w, p)
}
func (m msgSvc) MarkRead(ctx context.Context, id domain.ID) error { return m.readFn(ctx, id) }
func (m msgSvc) Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error) {
	return m.editFn(ctx, id, content)
}
func (m msgSvc) Delete(ctx context.Context, id domain.ID) error { return m.deleteFn(ctx, id) }
func (m msgSvc) MarkConversationRead(ctx context.Context, with string) error {
	return m.convRdfn(ctx, with)
}
func (m msgSvc) UnreadCount(ctx context.Context) (int64, error) { return m.countFn(ctx) }
func (m msgSvc) ReactToMessage(ctx context.Context, id domain.ID, actor string) error {
	return m.reactFn(ctx, id, actor)
}
func (m msgSvc) UnreactMessage(ctx context.Context, id domain.ID, actor string) error {
	return m.unrctFn(ctx, id, actor)
}

type validator struct{ uid string }

func (v validator) ValidateToken(context.Context, string) (string, error) { return v.uid, nil }

func dial(t *testing.T, h *message.GRPCHandler) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpcsrv.NewServer(validator{uid: "u1"}, zap.NewNop(), nil, h)
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

// ---- SendMessage ----

func TestGRPC_SendMessage(t *testing.T) {
	svc := msgSvc{sendFn: func(_ context.Context, in message.SendMessageInput) (*domain.Message, error) {
		return &domain.Message{ID: idgen.NewID(), FromUID: "u1", ToUID: in.ToUID, Content: in.Content}, nil
	}}
	conn := dial(t, message.NewGRPCHandler(svc))
	client := aeropb.NewMessageServiceClient(conn)

	resp, err := client.SendMessage(authCtx(), &aeropb.SendMessageRequest{ToUid: "b", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.ToUid != "b" || resp.Message.FromUid != "u1" || resp.Message.Content != "hi" {
		t.Fatalf("unexpected message: %+v", resp.Message)
	}
}

func TestGRPC_AuthRequired(t *testing.T) {
	svc := msgSvc{}
	conn := dial(t, message.NewGRPCHandler(svc))
	client := aeropb.NewMessageServiceClient(conn)
	_, err := client.SendMessage(context.Background(), &aeropb.SendMessageRequest{ToUid: "b", Content: "hi"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

// ---- EditMessage ----

func TestGRPC_EditMessage_ReturnsEditedAt(t *testing.T) {
	edited := time.Now().UTC()
	svc := msgSvc{editFn: func(_ context.Context, id domain.ID, content string) (*domain.Message, error) {
		return &domain.Message{ID: id, FromUID: "u1", ToUID: "b", Content: content, EditedAt: &edited}, nil
	}}
	conn := dial(t, message.NewGRPCHandler(svc))
	client := aeropb.NewMessageServiceClient(conn)
	resp, err := client.EditMessage(authCtx(), &aeropb.EditMessageRequest{Id: idgen.NewID().String(), Content: "fixed"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "fixed" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
	if resp.Message.EditedAt == nil {
		t.Fatal("edited_at not propagated")
	}
}

func TestGRPC_EditMessage_NonSenderPermissionDenied(t *testing.T) {
	svc := msgSvc{editFn: func(context.Context, domain.ID, string) (*domain.Message, error) {
		return nil, apperr.Forbidden("message.forbidden", "not sender")
	}}
	conn := dial(t, message.NewGRPCHandler(svc))
	client := aeropb.NewMessageServiceClient(conn)
	_, err := client.EditMessage(authCtx(), &aeropb.EditMessageRequest{Id: idgen.NewID().String(), Content: "x"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
}

func TestGRPC_EditMessage_InvalidID(t *testing.T) {
	conn := dial(t, message.NewGRPCHandler(msgSvc{}))
	client := aeropb.NewMessageServiceClient(conn)
	_, err := client.EditMessage(authCtx(), &aeropb.EditMessageRequest{Id: "not-a-uuid", Content: "x"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// ---- DeleteMessage ----

func TestGRPC_DeleteMessage_Success(t *testing.T) {
	called := false
	svc := msgSvc{deleteFn: func(context.Context, domain.ID) error { called = true; return nil }}
	conn := dial(t, message.NewGRPCHandler(svc))
	client := aeropb.NewMessageServiceClient(conn)
	if _, err := client.DeleteMessage(authCtx(), &aeropb.DeleteMessageRequest{Id: idgen.NewID().String()}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Delete not called")
	}
}

func TestGRPC_DeleteMessage_NotFound(t *testing.T) {
	svc := msgSvc{deleteFn: func(context.Context, domain.ID) error {
		return apperr.NotFound("message.not_found", "gone")
	}}
	conn := dial(t, message.NewGRPCHandler(svc))
	client := aeropb.NewMessageServiceClient(conn)
	_, err := client.DeleteMessage(authCtx(), &aeropb.DeleteMessageRequest{Id: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPC_DeleteMessage_InvalidID(t *testing.T) {
	conn := dial(t, message.NewGRPCHandler(msgSvc{}))
	client := aeropb.NewMessageServiceClient(conn)
	_, err := client.DeleteMessage(authCtx(), &aeropb.DeleteMessageRequest{Id: "bad"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// ---- MarkConversationRead ----

func TestGRPC_MarkConversationRead_Success(t *testing.T) {
	var gotWith string
	svc := msgSvc{convRdfn: func(_ context.Context, with string) error { gotWith = with; return nil }}
	conn := dial(t, message.NewGRPCHandler(svc))
	client := aeropb.NewMessageServiceClient(conn)
	if _, err := client.MarkConversationRead(authCtx(), &aeropb.MarkConversationReadRequest{WithUid: "b"}); err != nil {
		t.Fatal(err)
	}
	if gotWith != "b" {
		t.Fatalf("with_uid = %q, want b", gotWith)
	}
}

func TestGRPC_MarkConversationRead_PropagatesError(t *testing.T) {
	svc := msgSvc{convRdfn: func(context.Context, string) error {
		return apperr.NotFound("message.not_found", "no conversation")
	}}
	conn := dial(t, message.NewGRPCHandler(svc))
	client := aeropb.NewMessageServiceClient(conn)
	_, err := client.MarkConversationRead(authCtx(), &aeropb.MarkConversationReadRequest{WithUid: "b"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

// ---- UnreadCount ----

func TestGRPC_MessageUnreadCount(t *testing.T) {
	svc := msgSvc{countFn: func(context.Context) (int64, error) { return 42, nil }}
	conn := dial(t, message.NewGRPCHandler(svc))
	client := aeropb.NewMessageServiceClient(conn)
	resp, err := client.UnreadCount(authCtx(), &aeropb.UnreadCountRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.UnreadCount != 42 {
		t.Fatalf("unread_count = %d, want 42", resp.UnreadCount)
	}
}

func TestGRPC_MessageUnreadCount_PropagatesError(t *testing.T) {
	svc := msgSvc{countFn: func(context.Context) (int64, error) {
		return 0, apperr.Internal("msg.err", "db down")
	}}
	conn := dial(t, message.NewGRPCHandler(svc))
	client := aeropb.NewMessageServiceClient(conn)
	_, err := client.UnreadCount(authCtx(), &aeropb.UnreadCountRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}
