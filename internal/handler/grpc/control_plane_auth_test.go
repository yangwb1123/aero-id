package grpc

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type grpcClaimsValidator struct{}

func (grpcClaimsValidator) ValidateToken(context.Context, string) (string, error) { return "uid", nil }
func (grpcClaimsValidator) ValidateControlPlaneIdentity(context.Context, string) (*domain.TokenIdentity, error) {
	return &domain.TokenIdentity{UID: "uid", Issuer: "issuer", Subject: "subject", TenantID: "tenant", ClientID: "client"}, nil
}

func TestAuthInterceptorControlPlaneMethodsRejectUIDOnlyValidators(t *testing.T) {
	methods := []string{
		"/aero_id.v1.AccountService/GetMe",
		"/aero_id.v1.AggregationService/GetMeOverview",
		"/aero_id.v1.SourceIngestService/IngestEvents",
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			called := false
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer token"))
			_, err := AuthInterceptor(fakeValidator{uid: "uid"})(ctx, nil,
				&grpc.UnaryServerInfo{FullMethod: method}, func(context.Context, any) (any, error) {
					called = true
					return nil, nil
				})
			if status.Code(err) != codes.Unauthenticated || called {
				t.Fatalf("code=%v called=%v, want unauthenticated and no handler call", status.Code(err), called)
			}
		})
	}
}

func TestAuthInterceptorControlPlaneMethodsInjectCompleteIdentity(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer token"))
	var got *domain.TokenIdentity
	_, err := AuthInterceptor(grpcClaimsValidator{})(ctx, nil,
		&grpc.UnaryServerInfo{FullMethod: "/aero_id.v1.AccountService/GetMe"}, func(ctx context.Context, _ any) (any, error) {
			got, _ = service.IdentityFrom(ctx)
			return nil, nil
		})
	if err != nil || got == nil || got.TenantID != "tenant" || got.ClientID != "client" {
		t.Fatalf("identity=%+v err=%v", got, err)
	}
}

func TestAuthInterceptorLeavesLegacyMethodsUIDCompatible(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer token"))
	called := false
	_, err := AuthInterceptor(fakeValidator{uid: "uid"})(ctx, nil,
		&grpc.UnaryServerInfo{FullMethod: "/aero_id.v1.MessageService/SendMessage"}, func(ctx context.Context, _ any) (any, error) {
			called = true
			_, _ = service.ActorFrom(ctx)
			return nil, nil
		})
	if err != nil || !called {
		t.Fatalf("err=%v called=%v, want legacy method to remain compatible", err, called)
	}
}

var _ TokenValidator = grpcClaimsValidator{}
var _ interface {
	ValidateControlPlaneIdentity(context.Context, string) (*domain.TokenIdentity, error)
} = grpcClaimsValidator{}
