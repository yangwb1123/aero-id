package sso_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aero/aero-id/internal/sso"
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

// startMeteredSSO builds a bufconn client wired with the given metrics sink, reusing
// the fakeSSOServer from client_test.go.
func startMeteredSSO(t *testing.T, sink sso.MetricsSink) (*sso.Client, *fakeSSOServer) {
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
		Metrics:       sink,
	})
	return c, fake
}

func TestClient_MetricsTokenCacheHitMissAndErrors(t *testing.T) {
	sink := newRecordingSink()
	c, _ := startMeteredSSO(t, sink)
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
	c, _ := startMeteredSSO(t, sink)
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
