package grpc_test

import (
	"context"
	"net"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
)

// dialHealth stands up a server with the health service registered (and no
// metrics) over bufconn, returning a connected client conn.
func dialHealth(t *testing.T) (*grpc.ClientConn, *grpcsrv.HealthServer) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpcsrv.NewServer(validator{uid: "u1"}, zap.NewNop(), nil)
	hs := grpcsrv.RegisterHealth(srv)
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
	return conn, hs
}

func TestHealth_reportsServing(t *testing.T) {
	conn, _ := dialHealth(t)
	client := healthpb.NewHealthClient(conn)
	// Health check needs no bearer token (it isn't gated by the auth interceptor,
	// which only chains onto the registered service handlers).
	resp, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.GetStatus())
	}
}

func TestHealth_setServingFlipsStatus(t *testing.T) {
	conn, hs := dialHealth(t)
	client := healthpb.NewHealthClient(conn)
	hs.SetServing(false)
	resp, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status = %v, want NOT_SERVING after SetServing(false)", resp.GetStatus())
	}
}
