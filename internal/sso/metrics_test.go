package sso_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aero/aero-id/internal/sso"
	"github.com/aero/aero-id/internal/sso/authzpb"
	"github.com/aero/aero-id/internal/sso/ssopb"
)

// recordingSink captures SSO observations for assertions. It satisfies sso.MetricsSink.
type recordingSink struct {
	mu        sync.Mutex
	cacheHit  map[string]int
	cacheMiss map[string]int
	callOK    map[string]int
	callErr   map[string]int
}

func newRecordingSink() *recordingSink {
	return &recordingSink{
		cacheHit:  map[string]int{},
		cacheMiss: map[string]int{},
		callOK:    map[string]int{},
		callErr:   map[string]int{},
	}
}

func (s *recordingSink) ObserveSSOCache(cache string, hit bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if hit {
		s.cacheHit[cache]++
	} else {
		s.cacheMiss[cache]++
	}
}

func (s *recordingSink) ObserveSSOCall(op string, _ time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.callOK[op]++
	} else {
		s.callErr[op]++
	}
}

func (s *recordingSink) snapshot() (hit, miss, callOK, callErr map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := func(m map[string]int) map[string]int {
		out := make(map[string]int, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	return cp(s.cacheHit), cp(s.cacheMiss), cp(s.callOK), cp(s.callErr)
}

// startMeteredSSO builds a bufconn client wired with the given metrics sink,
// reusing fakeSSOServer from client_test.go and adding an (initially granting)
// fakeAuthorizerServer so the "authorize" op is observable on the same conn.
func startMeteredSSO(t *testing.T, sink sso.MetricsSink) (*sso.Client, *fakeSSOServer, *fakeAuthorizerServer) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	fake := &fakeSSOServer{}
	authorizer := &fakeAuthorizerServer{allowed: true}
	ssopb.RegisterSSOServer(srv, fake)
	authzpb.RegisterAuthorizerServer(srv, authorizer)
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
		Metrics:       sink,
	})
	return c, fake, authorizer
}

func TestClient_MetricsTokenCacheHitMissAndErrors(t *testing.T) {
	sink := newRecordingSink()
	c, _, _ := startMeteredSSO(t, sink)
	ctx := context.Background()

	// Miss then hit on the same token.
	if _, err := c.ValidateToken(ctx, "abc"); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, err := c.ValidateToken(ctx, "abc"); err != nil {
		t.Fatalf("validate cached: %v", err)
	}
	// A rejected token drives an upstream error.
	if _, err := c.ValidateToken(ctx, "bad"); err == nil {
		t.Fatal("expected error for bad token")
	}

	hit, miss, callOK, callErr := sink.snapshot()
	if hit["token"] != 1 {
		t.Errorf("token cache hits = %d, want 1", hit["token"])
	}
	if miss["token"] != 2 {
		t.Errorf("token cache misses = %d, want 2", miss["token"])
	}
	if callOK["validate"] != 1 {
		t.Errorf("validate ok calls = %d, want 1", callOK["validate"])
	}
	if callErr["validate"] != 1 {
		t.Errorf("validate error calls = %d, want 1", callErr["validate"])
	}
}

func TestClient_MetricsUserCacheAndCall(t *testing.T) {
	sink := newRecordingSink()
	c, _, _ := startMeteredSSO(t, sink)
	ctx := context.Background()

	// First batch: both miss and trigger one upstream get_users.
	if _, err := c.GetUsers(ctx, []string{"u1", "u2"}); err != nil {
		t.Fatalf("get users: %v", err)
	}
	// Second batch: u1 hits cache, u3 misses.
	if _, err := c.GetUsers(ctx, []string{"u1", "u3"}); err != nil {
		t.Fatalf("get users 2: %v", err)
	}

	hit, miss, callOK, _ := sink.snapshot()
	if hit["user"] != 1 {
		t.Errorf("user cache hits = %d, want 1", hit["user"])
	}
	if miss["user"] != 3 {
		t.Errorf("user cache misses = %d, want 3 (u1,u2 then u3)", miss["user"])
	}
	if callOK["get_users"] != 2 {
		t.Errorf("get_users ok calls = %d, want 2", callOK["get_users"])
	}
}

// A nil registry/sink must remain safe: existing NewClientFromConn callers pass none.
func TestClient_NilMetricsSinkIsSafe(t *testing.T) {
	c, _ := startFakeSSO(t) // no Metrics in its ClientConfig
	if _, err := c.ValidateToken(context.Background(), "abc"); err != nil {
		t.Fatalf("validate with nil sink: %v", err)
	}
	if _, err := c.GetUsers(context.Background(), []string{"u1"}); err != nil {
		t.Fatalf("get users with nil sink: %v", err)
	}
}

// TestClient_MetricsAuthorizeCallAndErrors (A3): the MetricsSink fake records an
// "authorize" ObserveSSOCall with ok=false on an authz availability error and
// ok=true on a grant (Allowed=true is an RPC success), with no cross-contamination
// of the existing validate/get_users op counts.
func TestClient_MetricsAuthorizeCallAndErrors(t *testing.T) {
	sink := newRecordingSink()
	c, _, authorizer := startMeteredSSO(t, sink)
	ctx := context.Background()

	// Grant: the RPC succeeded (Allowed=true), so ok=true on "authorize".
	if ok, err := c.HasPermission(ctx, "u1", "vault.file.delete"); err != nil || !ok {
		t.Fatalf("authorize grant ok=%v err=%v", ok, err)
	}

	// Availability error: the RPC failed, so the same op records ok=false.
	authorizer.setErr(status.Error(codes.Unavailable, "authz down"))
	if _, err := c.HasPermission(ctx, "u2", "vault.file.delete"); err == nil {
		t.Fatal("expected error from down authorizer")
	}

	_, _, callOK, callErr := sink.snapshot()
	if callOK["authorize"] != 1 {
		t.Errorf("authorize ok calls = %d, want 1 (grant is an RPC success)", callOK["authorize"])
	}
	if callErr["authorize"] != 1 {
		t.Errorf("authorize error calls = %d, want 1", callErr["authorize"])
	}
	// No cross-contamination: validate/get_users emit no "authorize" op and vice versa.
	if callOK["validate"] != 0 || callOK["get_users"] != 0 || callErr["validate"] != 0 || callErr["get_users"] != 0 {
		t.Errorf("unexpected cross-contamination: validate ok=%d err=%d get_users ok=%d err=%d, want 0 each",
			callOK["validate"], callErr["validate"], callOK["get_users"], callErr["get_users"])
	}
}
