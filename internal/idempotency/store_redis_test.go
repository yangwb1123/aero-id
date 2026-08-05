package idempotency

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/rueidis"
)

// TestRedisStore_Reserve_then_Complete verifies the happy path: Reserve claims a
// key, Complete stores the response, and a subsequent Reserve returns the
// completed response (not a conflict).
func TestRedisStore_Reserve_then_Complete(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()

	store := NewRedisStore(client)
	ctx := context.Background()
	const uid, endpoint, key = "u1", "/v1/files", "abc-123"

	// First Reserve should succeed (reserved=true).
	rec, reserved, err := store.Reserve(ctx, uid, endpoint, key)
	if err != nil {
		t.Fatalf("Reserve error = %v", err)
	}
	if !reserved {
		t.Fatal("Reserve returned reserved=false, want true")
	}
	if rec != nil {
		t.Fatalf("Reserve returned rec=%v, want nil", rec)
	}

	// Complete the reservation.
	completed := Record{Status: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`)}
	if err := store.Complete(ctx, uid, endpoint, key, completed, time.Hour); err != nil {
		t.Fatalf("Complete error = %v", err)
	}

	// A second Reserve (same key) should return the completed record.
	rec, reserved, err = store.Reserve(ctx, uid, endpoint, key)
	if err != nil {
		t.Fatalf("Second Reserve error = %v", err)
	}
	if reserved {
		t.Fatal("Second Reserve returned reserved=true, want false")
	}
	if rec == nil {
		t.Fatal("Second Reserve returned rec=nil, want completed record")
	}
	if !rec.Completed {
		t.Fatal("Second Reserve returned !Completed, want Completed")
	}
	if rec.Status != 200 {
		t.Fatalf("Second Reserve Status=%d, want 200", rec.Status)
	}
	if string(rec.Body) != `{"ok":true}` {
		t.Fatalf("Second Reserve Body=%q, want `{\"ok\":true}`", string(rec.Body))
	}
}

// TestRedisStore_Reserve_conflict_inFlight checks that a concurrent caller
// gets a non-completed record (in-flight conflict).
func TestRedisStore_Reserve_conflict_inFlight(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()

	store := NewRedisStore(client)
	ctx := context.Background()
	const uid, endpoint, key = "u1", "/v1/files", "abc-123"

	// First Reserve.
	_, reserved, err := store.Reserve(ctx, uid, endpoint, key)
	if err != nil {
		t.Fatalf("First Reserve error = %v", err)
	}
	if !reserved {
		t.Fatal("First Reserve returned reserved=false")
	}

	// Second Reserve (same key, without completing) — should return in-progress.
	rec, reserved, err := store.Reserve(ctx, uid, endpoint, key)
	if err != nil {
		t.Fatalf("Second Reserve error = %v", err)
	}
	if reserved {
		t.Fatal("Second Reserve returned reserved=true, want false")
	}
	if rec == nil {
		t.Fatal("Second Reserve returned rec=nil, want in-progress record")
	}
	if rec.Completed {
		t.Fatal("Second Reserve returned Completed=true, want false (in-flight)")
	}
}

// TestRedisStore_Release verifies that a released key can be re-reserved.
func TestRedisStore_Release(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()

	store := NewRedisStore(client)
	ctx := context.Background()
	const uid, endpoint, key = "u1", "/v1/files", "abc-123"

	_, reserved, err := store.Reserve(ctx, uid, endpoint, key)
	if err != nil {
		t.Fatalf("Reserve error = %v", err)
	}
	if !reserved {
		t.Fatal("Reserve returned reserved=false")
	}

	if err := store.Release(ctx, uid, endpoint, key); err != nil {
		t.Fatalf("Release error = %v", err)
	}

	// After Release, Reserve should succeed again.
	_, reserved, err = store.Reserve(ctx, uid, endpoint, key)
	if err != nil {
		t.Fatalf("Re-Reserve error = %v", err)
	}
	if !reserved {
		t.Fatal("Re-Reserve returned reserved=false, want true (key was released)")
	}
}

// TestRedisStore_Reserve_autoExpiry verifies that a stale reservation is
// auto-reclaimed by Redis TTL, making the key available again.
func TestRedisStore_Reserve_autoExpiry(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()

	store := NewRedisStore(client)
	ctx := context.Background()
	const uid, endpoint, key = "u1", "/v1/files", "abc-123"

	// Reserve — TTL is redisReservationTTL (2 min). Fast-forward past it.
	_, reserved, err := store.Reserve(ctx, uid, endpoint, key)
	if err != nil {
		t.Fatalf("Reserve error = %v", err)
	}
	if !reserved {
		t.Fatal("Reserve returned reserved=false")
	}

	// Fast-forward miniredis clock by 3 minutes (past the 2-minute TTL).
	srv.FastForward(3 * time.Minute)

	// After TTL expiry, Reserve should succeed again.
	_, reserved, err = store.Reserve(ctx, uid, endpoint, key)
	if err != nil {
		t.Fatalf("Re-Reserve after expiry error = %v", err)
	}
	if !reserved {
		t.Fatal("Re-Reserve after expiry returned reserved=false, want true")
	}
}

// newMiniRedis starts an in-process miniredis server and returns a rueidis
// client connected to it.
func newMiniRedis(t *testing.T) (*miniredis.Miniredis, rueidis.Client) {
	t.Helper()

	srv, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}

	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress:  []string{srv.Addr()},
		DisableCache: true, // miniredis does not support RESP3 client-side caching
		AlwaysRESP2:  true,
	})
	if err != nil {
		srv.Close()
		t.Fatalf("rueidis.NewClient: %v", err)
	}

	return srv, client
}
