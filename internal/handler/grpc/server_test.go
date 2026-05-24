package grpc

import (
	"context"
	stderrors "errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

type fakeValidator struct {
	uid string
	err error
}

func (f fakeValidator) ValidateToken(context.Context, string) (string, error) { return f.uid, f.err }

func TestAuthInterceptor_missingToken(t *testing.T) {
	intc := AuthInterceptor(fakeValidator{uid: "u"})
	_, err := intc(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/x/Y"},
		func(context.Context, any) (any, error) { return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestAuthInterceptor_injectsActor(t *testing.T) {
	intc := AuthInterceptor(fakeValidator{uid: "u"})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer abc"))
	var got string
	_, err := intc(ctx, nil, &grpc.UnaryServerInfo{}, func(c context.Context, _ any) (any, error) {
		got, _ = service.ActorFrom(c)
		return "ok", nil
	})
	if err != nil || got != "u" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestAuthInterceptor_invalidToken(t *testing.T) {
	intc := AuthInterceptor(fakeValidator{err: stderrors.New("bad")})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer abc"))
	_, err := intc(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestGRPCError_mapsAppError(t *testing.T) {
	if status.Code(grpcError(apperr.NotFound("x.not_found", "nope"))) != codes.NotFound {
		t.Fatal("AppError should map to its gRPC code")
	}
}

func TestGRPCError_genericInternal(t *testing.T) {
	if status.Code(grpcError(stderrors.New("boom"))) != codes.Internal {
		t.Fatal("non-AppError should map to Internal")
	}
}
