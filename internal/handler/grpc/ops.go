package grpc

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// HealthServer wraps the standard grpc health server so app.go can flip overall
// serving status during graceful shutdown without importing the grpc/health
// package directly.
type HealthServer struct {
	srv *health.Server
}

// RegisterHealth registers the standard gRPC health service on s and reports the
// overall server status (empty service name "") as SERVING. The returned handle
// lets the caller flip status (e.g. NOT_SERVING during graceful shutdown).
func RegisterHealth(s *grpc.Server) *HealthServer {
	hs := health.NewServer()
	healthpb.RegisterHealthServer(s, hs)
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	return &HealthServer{srv: hs}
}

// SetServing marks the overall server (the empty "" service) as SERVING or
// NOT_SERVING. Call with false at the start of graceful shutdown so load
// balancers drain this instance.
func (h *HealthServer) SetServing(serving bool) {
	if h == nil || h.srv == nil {
		return
	}
	st := healthpb.HealthCheckResponse_NOT_SERVING
	if serving {
		st = healthpb.HealthCheckResponse_SERVING
	}
	h.srv.SetServingStatus("", st)
}

// Shutdown transitions every tracked service to NOT_SERVING. The grpc health
// server also does this automatically, but calling it explicitly lets clients
// observe the drain before the listener closes.
func (h *HealthServer) Shutdown() {
	if h == nil || h.srv == nil {
		return
	}
	h.srv.Shutdown()
}

// RegisterReflection enables the gRPC server reflection service on s. It exposes
// the server's service/method descriptors so tools like grpcurl can introspect
// the API without a local .proto copy. It is gated by a config flag (off by
// default) because reflection reveals the full RPC surface — fine for internal
// ops, but not something to expose on an untrusted edge unconditionally.
func RegisterReflection(s *grpc.Server) {
	reflection.Register(s)
}
