package grpc_test

import (
	"context"
	"net"
	"testing"

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
	"github.com/aero/aero-id/internal/service"
)

type validator struct{ uid string }

func (v validator) ValidateToken(context.Context, string) (string, error) { return v.uid, nil }

// Embedding the service interface lets each fake implement only the methods the
// test exercises; unimplemented ones are never called.
type fileSvc struct {
	service.FileService
	getFn func(context.Context, domain.ID) (*domain.File, error)
}

func (f fileSvc) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	return f.getFn(ctx, id)
}

type msgSvc struct {
	service.MessageService
	sendFn func(context.Context, service.SendMessageInput) (*domain.Message, error)
}

func (m msgSvc) Send(ctx context.Context, in service.SendMessageInput) (*domain.Message, error) {
	return m.sendFn(ctx, in)
}

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

func TestGRPC_SendMessage(t *testing.T) {
	svc := msgSvc{sendFn: func(_ context.Context, in service.SendMessageInput) (*domain.Message, error) {
		return &domain.Message{ID: idgen.NewID(), FromUID: "u1", ToUID: in.ToUID, Content: in.Content}, nil
	}}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
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
	conn := dial(t, grpcsrv.NewMessageServer(msgSvc{}))
	client := aeropb.NewMessageServiceClient(conn)
	_, err := client.SendMessage(context.Background(), &aeropb.SendMessageRequest{ToUid: "b", Content: "hi"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestGRPC_GetFileNotFound(t *testing.T) {
	svc := fileSvc{getFn: func(context.Context, domain.ID) (*domain.File, error) {
		return nil, apperr.NotFound("file.not_found", "nope")
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)
	_, err := client.GetFile(authCtx(), &aeropb.GetFileRequest{Id: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPC_InvalidUUIDReturnsInvalidArgument(t *testing.T) {
	// The id parse fails before the service layer is ever reached.
	conn := dial(t, grpcsrv.NewFileServer(fileSvc{}))
	client := aeropb.NewFileServiceClient(conn)
	_, err := client.GetFile(authCtx(), &aeropb.GetFileRequest{Id: "not-a-uuid"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

type panicMsgSvc struct {
	service.MessageService
}

func (panicMsgSvc) Send(context.Context, service.SendMessageInput) (*domain.Message, error) {
	panic("boom")
}

func TestGRPC_RecoveryInterceptorCatchesPanic(t *testing.T) {
	conn := dial(t, grpcsrv.NewMessageServer(panicMsgSvc{}))
	client := aeropb.NewMessageServiceClient(conn)
	_, err := client.SendMessage(authCtx(), &aeropb.SendMessageRequest{ToUid: "b", Content: "hi"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}
