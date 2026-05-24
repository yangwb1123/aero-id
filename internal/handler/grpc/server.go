// Package grpc hosts aero-id's internal gRPC transport: a server with auth and
// recovery interceptors plus handlers that delegate to the shared service layer.
package grpc

import (
	"context"
	stderrors "errors"
	"runtime/debug"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

// TokenValidator is the SSO capability the auth interceptor needs. *sso.Client
// satisfies it.
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (string, error)
}

// ServiceRegistrar lets each handler register itself on the server without
// server.go importing the concrete handler types.
type ServiceRegistrar interface {
	Register(s *grpc.Server)
}

// NewServer builds a *grpc.Server with recovery + auth interceptors and registers
// each provided handler.
func NewServer(validator TokenValidator, log *zap.Logger, regs ...ServiceRegistrar) *grpc.Server {
	s := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			RecoveryInterceptor(log),
			AuthInterceptor(validator),
		),
	)
	for _, r := range regs {
		r.Register(s)
	}
	return s
}

// AuthInterceptor validates the bearer token from request metadata and injects
// the resulting uid as the request actor.
func AuthInterceptor(v TokenValidator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		token := bearerFromMD(ctx)
		if token == "" {
			return nil, status.Error(codes.Unauthenticated, "missing bearer token")
		}
		uid, err := v.ValidateToken(ctx, token)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "invalid or expired token")
		}
		return handler(service.WithActor(ctx, uid), req)
	}
}

// RecoveryInterceptor converts a panic into a logged Internal status.
func RecoveryInterceptor(log *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("grpc panic recovered",
					zap.Any("panic", r),
					zap.String("method", info.FullMethod),
					zap.ByteString("stack", debug.Stack()),
				)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}

// grpcError maps a service error to a gRPC status. *AppError carries its own code;
// anything else becomes Internal so internals are not leaked.
func grpcError(err error) error {
	if err == nil {
		return nil
	}
	var ae *apperr.AppError
	if stderrors.As(err, &ae) {
		return status.Error(ae.GRPC, ae.Message)
	}
	return status.Error(codes.Internal, "internal server error")
}

func bearerFromMD(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return ""
	}
	const prefix = "Bearer "
	h := vals[0]
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
