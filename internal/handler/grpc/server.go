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

	"google.golang.org/grpc/peer"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/pkg/ratelimit"
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

// ServerOption tunes optional NewServer behaviour (rate limiting, etc.) without
// growing the positional signature that tests rely on.
type ServerOption func(*serverOptions)

type serverOptions struct {
	limiter *ratelimit.Limiter
}

// WithRateLimit installs a per-actor rate-limit interceptor. A nil limiter (or
// one disabled by config) is a no-op, so this is always safe to pass.
func WithRateLimit(l *ratelimit.Limiter) ServerOption {
	return func(o *serverOptions) { o.limiter = l }
}

// NewServer builds a *grpc.Server with metrics + recovery + auth interceptors and
// registers each provided handler. A nil reg disables metrics (used by tests).
// Options can add a rate-limit interceptor.
func NewServer(validator TokenValidator, log *zap.Logger, reg *metrics.Registry, regs ...ServiceRegistrar) *grpc.Server {
	return NewServerWithOptions(validator, log, reg, nil, regs...)
}

// NewServerWithOptions is NewServer with extra tuning options (e.g. rate limiting).
func NewServerWithOptions(validator TokenValidator, log *zap.Logger, reg *metrics.Registry, opts []ServerOption, regs ...ServiceRegistrar) *grpc.Server {
	var o serverOptions
	for _, fn := range opts {
		fn(&o)
	}

	// Chain executes in slice order. Recovery wraps everything below it; auth runs
	// next; the rate limiter runs AFTER auth so it can key on the resolved actor uid.
	interceptors := []grpc.UnaryServerInterceptor{
		RecoveryInterceptor(log),
		AuthInterceptor(validator),
	}
	if o.limiter != nil {
		interceptors = append(interceptors, RateLimitInterceptor(o.limiter))
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

// infraMethod reports whether a fully-qualified gRPC method belongs to an
// infrastructure service (health, reflection) that must bypass auth and rate
// limiting — these are operational surfaces probed by load balancers and ops
// tooling that carry no bearer token and should never be throttled.
func infraMethod(fullMethod string) bool {
	return strings.HasPrefix(fullMethod, "/grpc.health.v1.") ||
		strings.HasPrefix(fullMethod, "/grpc.reflection.")
}

// AuthInterceptor validates the bearer token from request metadata and injects
// the resulting uid as the request actor. Infrastructure methods (health,
// reflection) bypass auth so probes and ops tooling work without a token.
func AuthInterceptor(v TokenValidator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info != nil && infraMethod(info.FullMethod) {
			return handler(ctx, req)
		}
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

// RateLimitInterceptor throttles unary calls using an in-memory per-key token
// bucket. It keys on the authenticated actor uid when present (so it must be
// chained AFTER auth), falling back to the client's peer address otherwise.
// Over-limit calls get codes.ResourceExhausted. A nil/disabled limiter is a
// no-op (fail-open by configuration).
func RateLimitInterceptor(l *ratelimit.Limiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if l == nil || (info != nil && infraMethod(info.FullMethod)) {
			return handler(ctx, req)
		}
		if l.Allow(rateLimitKey(ctx)) {
			return handler(ctx, req)
		}
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
}

// rateLimitKey prefers the authenticated actor uid (set by AuthInterceptor) so a
// single user is limited consistently; it falls back to the gRPC peer address
// for unauthenticated calls.
func rateLimitKey(ctx context.Context) string {
	if uid, ok := service.ActorFrom(ctx); ok {
		return "uid:" + uid
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		return "ip:" + p.Addr.String()
	}
	return "ip:unknown"
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
