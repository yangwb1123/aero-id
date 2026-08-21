// Package grpc hosts aero-id's internal gRPC transport: a server with metrics, auth
// and recovery interceptors plus handlers that delegate to the shared service layer.
package grpc

import (
	"context"
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

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/pkg/logger"
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
	limiter            *ratelimit.Limiter
	distributedLimiter ratelimit.DistributedLimiter
	permChecker        middleware.PermissionChecker
	degradation        *middleware.DegradationState
	grpcOpts           []grpc.ServerOption // additional raw grpc options (keepalive, size limits, etc.)
}

// WithRateLimit installs a per-actor rate-limit interceptor. A nil limiter (or
// one disabled by config) is a no-op, so this is always safe to pass.
func WithRateLimit(l *ratelimit.Limiter) ServerOption {
	return func(o *serverOptions) { o.limiter = l }
}

func WithDistributedRateLimit(l ratelimit.DistributedLimiter) ServerOption {
	return func(o *serverOptions) { o.distributedLimiter = l }
}

// WithPermissionChecker installs a permission interceptor that enforces
// method-level RBAC. When set, the interceptor checks the caller's permission
// before allowing the RPC to proceed. A nil checker is a no-op.
func WithPermissionChecker(c middleware.PermissionChecker) ServerOption {
	return func(o *serverOptions) { o.permChecker = c }
}

// WithGRPCServerOptions passes additional raw grpc.ServerOption values to the
// underlying grpc.NewServer call. This is the escape hatch for transport-level
// hardening (keepalive enforcement, size limits, flow-control windows, etc.).
func WithGRPCServerOptions(opts ...grpc.ServerOption) ServerOption {
	return func(o *serverOptions) { o.grpcOpts = append(o.grpcOpts, opts...) }
}

// WithDegradation injects the global degradation state into the gRPC server so
// every response carries x-aero-degraded trailing metadata describing which
// upstream dependencies are currently degraded. A nil state is a no-op.
func WithDegradation(d *middleware.DegradationState) ServerOption {
	return func(o *serverOptions) { o.degradation = d }
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
	// next; the rate limiter runs AFTER auth so it can key on the resolved actor uid;
	// the permission interceptor runs AFTER auth so it can read the actor uid.
	interceptors := []grpc.UnaryServerInterceptor{
		RecoveryInterceptor(log),
		AuthInterceptor(validator),
	}
	if o.limiter != nil {
		interceptors = append(interceptors, RateLimitInterceptor(o.limiter, o.distributedLimiter))
	} else if o.distributedLimiter != nil {
		interceptors = append(interceptors, RateLimitInterceptor(nil, o.distributedLimiter))
	}
	if o.permChecker != nil {
		interceptors = append(interceptors, PermissionInterceptor(o.permChecker))
	}
	if reg != nil {
		// Outermost (after tracing) so it measures the whole call, including auth and recovery.
		interceptors = append([]grpc.UnaryServerInterceptor{MetricsInterceptor(reg)}, interceptors...)
	}
	// Tracing is outermost so the server span parents metrics, auth, recovery and the
	// handler. It relies on the global tracer/propagator (no-op when tracing is off).
	interceptors = append([]grpc.UnaryServerInterceptor{TracingInterceptor()}, interceptors...)
	// Degradation interceptor runs AFTER metrics/tracing and BEFORE recovery so that
	// it decorates every response (including errors) with x-aero-degraded trailing
	// metadata. A nil state is a no-op.
	if o.degradation != nil {
		interceptors = append(interceptors, DegradationInterceptor(o.degradation))
	}

	// Assemble: first the unary interceptors, then any extra raw options (keepalive
	// enforcement, size limits, flow-control windows, etc.) passed via WithGRPCServerOptions.
	allOpts := []grpc.ServerOption{grpc.ChainUnaryInterceptor(interceptors...)}
	allOpts = append(allOpts, o.grpcOpts...)
	s := grpc.NewServer(allOpts...)
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
			if values := md.Get("x-request-id"); len(values) > 0 && strings.TrimSpace(values[0]) != "" {
				requestID := strings.TrimSpace(values[0])
				ctx = service.WithRequestID(ctx, requestID)
				ctx = logger.WithRequestID(ctx, requestID)
				_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", requestID))
			}
		}
		ctx, span := tracer.Start(ctx, info.FullMethod,
			oteltrace.WithSpanKind(oteltrace.SpanKindServer),
			oteltrace.WithAttributes(semconv.RPCMethodKey.String(info.FullMethod)),
		)
		defer span.End()
		if spanContext := span.SpanContext(); spanContext.HasTraceID() {
			ctx = service.WithTraceID(ctx, spanContext.TraceID().String())
			ctx = logger.WithTraceID(ctx, spanContext.TraceID().String())
			_ = grpc.SetHeader(ctx, metadata.Pairs("x-trace-id", spanContext.TraceID().String()))
		}

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

// DegradationInterceptor reads the current degradation state and adds
// x-aero-degraded trailing metadata to every response. The value is a
// comma-separated list of degraded dependencies (e.g. "db,sso") or "none"
// when every dependency is healthy. Infrastructure methods (health, reflection)
// are always marked "none" since they have no dependencies.
//
// SetTrailer MUST be called BEFORE the handler runs: gRPC stores the reference
// to the metadata.MD (a map reference type) and populates it when the response
// is sent. Setting it after the handler is too late — the trailer is already
// finalised.
func DegradationInterceptor(state *middleware.DegradationState) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		status := state.Status()
		if info != nil && infraMethod(info.FullMethod) {
			status = "none"
		}
		// SetTrailer stores the metadata.MD reference internally; it will be
		// serialised when the response is sent. Error is best-effort: a degraded
		// metadata header is informative, not critical.
		_ = grpc.SetTrailer(ctx, metadata.Pairs("x-aero-degraded", status))
		return handler(ctx, req)
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
		identity, uid, err := validateIdentity(ctx, v, token)
		if err != nil || uid == "" {
			middleware.InvalidateTokenOnValidationFailure(v, token, err)
			return nil, status.Error(codes.Unauthenticated, "invalid or expired token")
		}
		ctx = logger.WithActor(ctx, uid)
		if identity != nil {
			return handler(service.WithIdentity(ctx, identity), req)
		}
		return handler(service.WithActor(ctx, uid), req)
	}
}

func validateIdentity(ctx context.Context, validator TokenValidator, token string) (*domain.TokenIdentity, string, error) {
	if identityValidator, ok := validator.(interface {
		ValidateIdentity(context.Context, string) (*domain.TokenIdentity, error)
	}); ok {
		identity, err := identityValidator.ValidateIdentity(ctx, token)
		if identity == nil {
			return nil, "", err
		}
		return identity, identity.UID, err
	}
	uid, err := validator.ValidateToken(ctx, token)
	return nil, uid, err
}

// RateLimitInterceptor throttles unary calls using an in-memory per-key token
// bucket. It keys on the authenticated actor uid when present (so it must be
// chained AFTER auth), falling back to the client's peer address otherwise.
// Over-limit calls get codes.ResourceExhausted. A nil/disabled limiter is a
// no-op (fail-open by configuration).
func RateLimitInterceptor(l *ratelimit.Limiter, distributed ...ratelimit.DistributedLimiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if (l == nil && len(distributed) == 0) || (info != nil && infraMethod(info.FullMethod)) {
			return handler(ctx, req)
		}
		allowed, unavailable := allowGRPCRateLimit(ctx, rateLimitKey(ctx), l, firstDistributed(distributed), isGRPCRateLimitSensitive(info.FullMethod))
		if unavailable {
			return nil, status.Error(codes.Unavailable, "rate limiter unavailable; sensitive request refused")
		}
		if allowed {
			return handler(ctx, req)
		}
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
}

func firstDistributed(values []ratelimit.DistributedLimiter) ratelimit.DistributedLimiter {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

func allowGRPCRateLimit(ctx context.Context, key string, local *ratelimit.Limiter, distributed ratelimit.DistributedLimiter, sensitive bool) (bool, bool) {
	if distributed != nil {
		allowed, _, err := distributed.Allow(ctx, key)
		if err == nil {
			return allowed, false
		}
		// Interactive calls fall back to the local bucket. Sensitive calls only
		// proceed when that bucket can still make a decision; an outage with no
		// local limiter must not silently bypass the throttle.
		if sensitive && local == nil {
			return false, true
		}
	}
	if local == nil {
		return true, false
	}
	return local.Allow(key), false
}

// PermissionInterceptor checks that the authenticated actor has the required
// permission for the RPC method being called. The method→permission mapping is
// defined in requiredPermForMethod. Infrastructure methods (health, reflection)
// bypass the check. It MUST run AFTER AuthInterceptor so the actor uid is set.
func PermissionInterceptor(checker middleware.PermissionChecker) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info == nil || infraMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		perm, needsCheck := requiredPermForMethod(info.FullMethod)
		if !needsCheck {
			return handler(ctx, req)
		}
		uid, ok := service.ActorFrom(ctx)
		if !ok || uid == "" {
			return nil, status.Error(codes.Unauthenticated, "missing authenticated actor")
		}
		allowed, err := checker.HasPermission(ctx, uid, perm)
		if err != nil {
			return nil, status.Error(codes.Internal, "permission check failed")
		}
		if !allowed {
			return nil, status.Error(codes.PermissionDenied, "insufficient permissions")
		}
		return handler(ctx, req)
	}
}

// requiredPermForMethod returns the permission required for a gRPC method, or
// ("", false) if the method requires no special permission. This is the central
// registry for gRPC RBAC.
func requiredPermForMethod(fullMethod string) (string, bool) {
	m := map[string]string{
		"/aero_id.v1.AdminService/ListAudit":                 domain.PermAuditRead,
		"/aero_id.v1.AuditLedgerService/ListAuditFacts":      domain.PermAuditRead,
		"/aero_id.v1.AccountService/GetAccount":              service.AccountReadPermission,
		"/aero_id.v1.AccountService/ListAccounts":            service.AccountReadPermission,
		"/aero_id.v1.AggregationService/GetAccountOverview":  service.AccountReadPermission,
		"/aero_id.v1.OperationService/RunOperation":          domain.PermOperationManage,
		"/aero_id.v1.OperationService/ReconcileOperation":    domain.PermOperationManage,
		"/aero_id.v1.SyncJobService/ReconcileSyncJob":        domain.PermSourceSync,
		"/aero_id.v1.SourceIngestService/IngestEvents":       domain.PermSourceEventIngest,
		"/aero_id.v1.CallbackService/RegisterCallback":       domain.PermCallbackManage,
		"/aero_id.v1.CallbackService/ListCallbacks":          domain.PermCallbackManage,
		"/aero_id.v1.CallbackService/DeleteCallback":         domain.PermCallbackManage,
		"/aero_id.v1.CallbackService/ReplayCallbackDelivery": domain.PermCallbackManage,
		"/aero_id.v1.AdminService/GrantRole":                 domain.PermSystemConfig,
		"/aero_id.v1.AdminService/RevokeRole":                domain.PermSystemConfig,
	}
	perm, ok := m[fullMethod]
	return perm, ok
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
