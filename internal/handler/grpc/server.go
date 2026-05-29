// Package grpc hosts aero-id's internal gRPC transport: a server with metrics, auth
// and recovery interceptors plus handlers that delegate to the shared service layer.
package grpc

import (
	"context"
	stderrors "errors"
	"runtime/debug"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/metrics"
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

// NewServer builds a *grpc.Server with metrics + recovery + auth interceptors and
// registers each provided handler. A nil reg disables metrics (used by tests).
func NewServer(validator TokenValidator, log *zap.Logger, reg *metrics.Registry, regs ...ServiceRegistrar) *grpc.Server {
	interceptors := []grpc.UnaryServerInterceptor{
		RecoveryInterceptor(log),
		AuthInterceptor(validator),
	}
	if reg != nil {
		// Outermost (after tracing) so it measures the whole call, including auth and recovery.
		interceptors = append([]grpc.UnaryServerInterceptor{MetricsInterceptor(reg)}, interceptors...)
	}
	// Tracing is outermost so the server span parents metrics, auth, recovery and the
	// handler. It relies on the global tracer/propagator (no-op when tracing is off).
	interceptors = append([]grpc.UnaryServerInterceptor{TracingInterceptor()}, interceptors...)
	s := grpc.NewServer(grpc.ChainUnaryInterceptor(interceptors...))
	for _, r := range regs {
		r.Register(s)
	}
	return s
}

// grpcTracerName identifies aero-id's gRPC server instrumentation scope.
const grpcTracerName = "github.com/aero/aero-id/internal/handler/grpc"

// TracingInterceptor starts a server span per unary call, continuing any inbound
// trace context carried in the request metadata, and records the resulting gRPC
// status. It uses the globally-installed tracer/propagator, so it is a cheap no-op
// when tracing is disabled.
func TracingInterceptor() grpc.UnaryServerInterceptor {
	tracer := otel.Tracer(grpcTracerName)
	propagator := otel.GetTextMapPropagator()
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			ctx = propagator.Extract(ctx, metadataCarrier(md))
		}
		ctx, span := tracer.Start(ctx, info.FullMethod,
			oteltrace.WithSpanKind(oteltrace.SpanKindServer),
			oteltrace.WithAttributes(semconv.RPCMethodKey.String(info.FullMethod)),
		)
		defer span.End()

		resp, err := handler(ctx, req)
		code := status.Code(err)
		span.SetAttributes(attribute.String("rpc.grpc.status_code", code.String()))
		if err != nil {
			span.SetStatus(otelcodes.Error, code.String())
		}
		return resp, err
	}
}

// metadataCarrier adapts gRPC metadata to the OTel TextMapCarrier for trace
// context extraction.
type metadataCarrier metadata.MD

func (c metadataCarrier) Get(key string) string {
	if v := metadata.MD(c).Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

func (c metadataCarrier) Set(key, value string) { metadata.MD(c).Set(key, value) }

func (c metadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

var _ propagation.TextMapCarrier = metadataCarrier{}

// MetricsInterceptor records one RED observation per unary call, keyed by the gRPC
// method and resulting status code.
func MetricsInterceptor(reg *metrics.Registry) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		reg.ObserveGRPC(info.FullMethod, status.Code(err).String(), time.Since(start))
		return resp, err
	}
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
		if err != nil || uid == "" {
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
