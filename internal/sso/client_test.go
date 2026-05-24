package sso_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
