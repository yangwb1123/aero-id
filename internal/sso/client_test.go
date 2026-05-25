package sso_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aero/aero-id/internal/sso"
	"github.com/aero/aero-id/internal/sso/ssopb"
)

type fakeSSOServer struct {
	ssopb.UnimplementedSSOServer
	validateCalls int
	getUsersCalls int
}

func (f *fakeSSOServer) ValidateToken(_ context.Context, req *ssopb.ValidateTokenRequest) (*ssopb.ValidateTokenResponse, error) {
	f.validateCalls++
	if req.Token == "bad" {
		return nil, grpc.Errorf(7, "invalid")
	}
	return &ssopb.ValidateTokenResponse{Uid: "uid-of-" + req.Token, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()}, nil
}

func (f *fakeSSOServer) GetUsers(_ context.Context, req *ssopb.GetUsersRequest) (*ssopb.GetUsersResponse, error) {
	f.getUsersCalls++
	users := make([]*ssopb.User, 0, len(req.Uids))
	for _, uid := range req.Uids {
		users = append(users, &ssopb.User{Uid: uid, Nickname: "n-" + uid, Avatar: "a"})
	}
	return &ssopb.GetUsersResponse{Users: users}, nil
}

func startFakeSSO(t *testing.T) (*sso.Client, *fakeSSOServer) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	fake := &fakeSSOServer{}
	ssopb.RegisterSSOServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.DialContext(context.Background()) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	c := sso.NewClientFromConn(conn, sso.ClientConfig{
		TokenCacheTTL: 60 * time.Second,
		UserCacheTTL:  60 * time.Second,
	})
	return c, fake
}

func TestClient_ValidateTokenCaches(t *testing.T) {
	c, fake := startFakeSSO(t)
	ctx := context.Background()

	uid, err := c.ValidateToken(ctx, "abc")
	if err != nil || uid != "uid-of-abc" {
		t.Fatalf("first call uid=%q err=%v", uid, err)
	}
	uid, _ = c.ValidateToken(ctx, "abc")
	if uid != "uid-of-abc" {
		t.Fatalf("second call uid=%q", uid)
	}
	if fake.validateCalls != 1 {
		t.Errorf("validate calls = %d, want 1 (cached)", fake.validateCalls)
	}
}

func TestClient_ValidateTokenBadReturnsError(t *testing.T) {
	c, _ := startFakeSSO(t)
	_, err := c.ValidateToken(context.Background(), "bad")
	if err == nil {
		t.Fatal("expected error for bad token")
	}
}

func TestClient_GetUsersDedupesAcrossCalls(t *testing.T) {
	c, fake := startFakeSSO(t)
	ctx := context.Background()

	_, _ = c.GetUsers(ctx, []string{"u1", "u2"})
	_, _ = c.GetUsers(ctx, []string{"u1", "u3"})

	if fake.getUsersCalls != 2 {
		t.Errorf("calls = %d, want 2 (one per non-cached batch)", fake.getUsersCalls)
	}
}

// slowSSOServer blocks ValidateToken for a configurable duration before responding.
type slowSSOServer struct {
	ssopb.UnimplementedSSOServer
	delay time.Duration
}

func (s *slowSSOServer) ValidateToken(ctx context.Context, req *ssopb.ValidateTokenRequest) (*ssopb.ValidateTokenResponse, error) {
	select {
	case <-time.After(s.delay):
		return &ssopb.ValidateTokenResponse{Uid: "uid-" + req.Token}, nil
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
}

// startSlowSSO builds a bufconn client backed by a slowSSOServer.
func startSlowSSO(t *testing.T, delay time.Duration, cfg sso.ClientConfig) *sso.Client {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	ssopb.RegisterSSOServer(srv, &slowSSOServer{delay: delay})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.DialContext(context.Background()) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	// conn ownership is transferred to the Client; Client.Close() will close it.
	c := sso.NewClientFromConn(conn, cfg)
	return c
}

func TestClient_CloseClosesConn(t *testing.T) {
	c, _ := startFakeSSO(t)

	// Close must not panic.
	c.Close()

	// Calling Close a second time must also be safe (idempotent).
	c.Close()

	// A subsequent RPC on a closed connection should fail.
	_, err := c.ValidateToken(context.Background(), "any")
	if err == nil {
		t.Error("expected error after Close, got nil")
	}
}

func TestClient_ValidateTokenTimesOut(t *testing.T) {
	const serverDelay = 200 * time.Millisecond
	const clientTimeout = 20 * time.Millisecond

	c := startSlowSSO(t, serverDelay, sso.ClientConfig{
		TokenCacheTTL: 60 * time.Second,
		UserCacheTTL:  60 * time.Second,
		Timeout:       clientTimeout,
	})
	defer c.Close()

	start := time.Now()
	_, err := c.ValidateToken(context.Background(), "tok")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	// The call must have returned well before the server would have replied.
	if elapsed >= serverDelay {
		t.Errorf("call took %v, expected it to time out before %v", elapsed, serverDelay)
	}

	// Verify the error signals deadline exceeded (either via context sentinel or gRPC status).
	st, _ := status.FromError(errors.Unwrap(err))
	if !errors.Is(err, context.DeadlineExceeded) && st.Code() != codes.DeadlineExceeded {
		t.Errorf("want DeadlineExceeded error, got: %v", err)
	}
}

func TestClient_HealthCheck(t *testing.T) {
	c, _ := startFakeSSO(t)

	// A live connection is healthy (no hard failure / shutdown).
	if err := c.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck on live conn = %v, want nil", err)
	}

	// After Close the connection is gone, so readiness must report unhealthy.
	c.Close()
	if err := c.HealthCheck(context.Background()); err == nil {
		t.Error("HealthCheck after Close = nil, want error")
	}
}

// flakySSOServer succeeds for the first successCalls ValidateToken calls, then
// returns Unavailable — simulating an SSO outage after the cache was warmed.
type flakySSOServer struct {
	ssopb.UnimplementedSSOServer
	calls        int
	successCalls int
}

func (f *flakySSOServer) ValidateToken(_ context.Context, req *ssopb.ValidateTokenRequest) (*ssopb.ValidateTokenResponse, error) {
	f.calls++
	if f.calls > f.successCalls {
		return nil, status.Error(codes.Unavailable, "sso down")
	}
	// Far-future expiry; the cache clamps it to the (tiny) configured TTL.
	return &ssopb.ValidateTokenResponse{Uid: "uid-" + req.Token, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()}, nil
}

func startFlakySSO(t *testing.T, successCalls int, cfg sso.ClientConfig) *sso.Client {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	ssopb.RegisterSSOServer(srv, &flakySSOServer{successCalls: successCalls})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.DialContext(context.Background()) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	c := sso.NewClientFromConn(conn, cfg)
	return c
}

func TestClient_ServesStaleWhenSSODown(t *testing.T) {
	c := startFlakySSO(t, 1, sso.ClientConfig{
		TokenCacheTTL: 15 * time.Millisecond,
		UserCacheTTL:  time.Minute,
		StaleTTL:      time.Hour,
	})
	defer c.Close()
	ctx := context.Background()

	// Warm the cache (SSO call #1 succeeds).
	if uid, err := c.ValidateToken(ctx, "tok"); err != nil || uid != "uid-tok" {
		t.Fatalf("warm uid=%q err=%v", uid, err)
	}

	// Let the cached entry expire, then call again: SSO call #2 fails, but the entry
	// is still within the stale grace window, so the last-known uid is served.
	time.Sleep(40 * time.Millisecond)
	uid, err := c.ValidateToken(ctx, "tok")
	if err != nil || uid != "uid-tok" {
		t.Fatalf("stale-serve uid=%q err=%v (want last-known uid, no error)", uid, err)
	}
}

func TestClient_NoStaleForUnknownTokenWhenDown(t *testing.T) {
	c := startFlakySSO(t, 0, sso.ClientConfig{ // fails from the very first call
		TokenCacheTTL: time.Minute,
		UserCacheTTL:  time.Minute,
		StaleTTL:      time.Hour,
	})
	defer c.Close()
	// No cached identity exists, so a down SSO must surface an error.
	if _, err := c.ValidateToken(context.Background(), "never-seen"); err == nil {
		t.Fatal("expected error for an uncached token when SSO is down")
	}
}
