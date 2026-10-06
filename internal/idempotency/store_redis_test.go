package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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

func TestRedisStore_LeaseRejectsStaleLeaderAfterExpiry(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()
	store := NewRedisStore(client)
	ctx := context.Background()
	const uid, endpoint, key = "u-lease", "/v1/callback", "lease-key"
	_, reserved, first, err := store.ReserveLease(ctx, uid, endpoint, key)
	if err != nil || !reserved || first == "" {
		t.Fatalf("initial lease: reserved=%v token=%q err=%v", reserved, first, err)
	}
	srv.FastForward(3 * time.Minute)
	_, reclaimed, second, err := store.ReserveLease(ctx, uid, endpoint, key)
	if err != nil || !reclaimed || second == "" || second == first {
		t.Fatalf("replacement lease: reserved=%v token=%q err=%v", reclaimed, second, err)
	}
	if err := store.CompleteLease(ctx, uid, endpoint, key, first, Record{Status: 201}, time.Hour); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("stale complete error = %v, want ErrReservationLost", err)
	}
	if err := store.ReleaseLease(ctx, uid, endpoint, key, first); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("stale release error = %v, want ErrReservationLost", err)
	}
	if err := store.CompleteLease(ctx, uid, endpoint, key, second, Record{Status: 201, Body: []byte(`{"ok":true}`)}, time.Hour); err != nil {
		t.Fatalf("replacement complete: %v", err)
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

// seedLegacyRecord writes a pre-v1 (flat, colon-joined) reservation or receipt
// directly into Redis, modelling an instance that has not been rolled yet.
func seedLegacyRecord(t *testing.T, srv *miniredis.Miniredis, uid, endpoint, key string, rr redisRecord) {
	t.Helper()
	payload, err := json.Marshal(rr)
	if err != nil {
		t.Fatalf("marshal legacy record: %v", err)
	}
	srv.Set(legacyRedisKey(uid, endpoint, key), string(payload))
}

// assertNoV1Key proves the legacy probe is read-only: honoring a pre-deploy
// reservation must not also claim the command under the new key format.
func assertNoV1Key(t *testing.T, srv *miniredis.Miniredis) {
	t.Helper()
	for _, key := range srv.Keys() {
		if strings.HasPrefix(key, redisKeyPrefix) {
			t.Fatalf("legacy probe wrote a v1 key %q", key)
		}
	}
}

// TestRedisStore_LegacyReservationHonoredAfterFormatBump covers the rollout
// window: a retry arriving after the deploy must not re-execute a command a
// pre-deploy instance still holds, so the in-flight legacy record is returned
// instead of a fresh v1 reservation.
func TestRedisStore_LegacyReservationHonoredAfterFormatBump(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()
	store := NewRedisStore(client)
	ctx := context.Background()
	const uid, endpoint, key = "u1", "POST /v1/me/sync?x=a", "c"

	seedLegacyRecord(t, srv, uid, endpoint, key, redisRecord{Status: 0, Completed: false, Owner: "legacy-owner"})

	rec, reserved, token, err := store.ReserveLease(ctx, uid, endpoint, key)
	if err != nil {
		t.Fatalf("ReserveLease error = %v", err)
	}
	if reserved {
		t.Fatal("ReserveLease reserved=true for a command a legacy leader already holds")
	}
	if token != "" {
		t.Fatalf("ReserveLease token = %q, want empty (no new lease was taken)", token)
	}
	if rec == nil || rec.Completed {
		t.Fatalf("ReserveLease rec = %#v, want a non-completed in-flight record", rec)
	}
	assertNoV1Key(t, srv)

	// The colon-shifted sibling of the same command must not be entangled: it
	// has no legacy reservation of its own, so it is free to reserve.
	_, reservedSibling, _, err := store.ReserveLease(ctx, uid, "POST /v1/me/sync?x=a:b", "c")
	if err != nil || !reservedSibling {
		t.Fatalf("sibling ReserveLease reserved=%v err=%v, want true/nil", reservedSibling, err)
	}
}

// TestRedisStore_LegacyCompletedRecordReplaysAfterFormatBump proves a receipt
// written before the deploy still replays, so a retry spanning the rollout does
// not re-run the mutation.
func TestRedisStore_LegacyCompletedRecordReplaysAfterFormatBump(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()
	store := NewRedisStore(client)
	ctx := context.Background()
	const uid, endpoint, key = "u1", "POST /v1/files", "abc-123"

	seedLegacyRecord(t, srv, uid, endpoint, key, redisRecord{
		Status: http.StatusCreated, ContentType: "application/json",
		Body: []byte(`{"id":"file-9"}`), Completed: true, Owner: "legacy-owner",
	})

	rec, reserved, err := store.Reserve(ctx, uid, endpoint, key)
	if err != nil {
		t.Fatalf("Reserve error = %v", err)
	}
	if reserved {
		t.Fatal("Reserve returned reserved=true for a command with a legacy receipt")
	}
	if rec == nil || !rec.Completed || rec.Status != http.StatusCreated || string(rec.Body) != `{"id":"file-9"}` {
		t.Fatalf("legacy receipt not replayed: %#v", rec)
	}
	assertNoV1Key(t, srv)
}

// TestRedisStore_RoundTripUnderNewKeyFormat is the smoke test for the new
// keyspace: every entry point must agree on the same key, including the two
// Lua lease scripts, and the keys actually written must carry the v1 marker.
func TestRedisStore_RoundTripUnderNewKeyFormat(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()
	store := NewRedisStore(client)
	ctx := context.Background()
	const uid, endpoint, key = "u1", "POST /v1/messages?x=a:b", "c"

	if _, reserved, _, err := store.ReserveLease(ctx, uid, endpoint, key); err != nil || !reserved {
		t.Fatalf("ReserveLease reserved=%v err=%v, want true/nil", reserved, err)
	}
	if !srv.Exists(redisKey(uid, endpoint, key)) {
		t.Fatalf("reserve did not write the v1 key %q", redisKey(uid, endpoint, key))
	}
	if err := store.Complete(ctx, uid, endpoint, key, Record{
		Status: http.StatusCreated, ContentType: "application/json", Body: []byte(`{"id":"msg-1"}`),
	}, time.Hour); err != nil {
		t.Fatalf("Complete error = %v", err)
	}
	rec, reserved, err := store.Reserve(ctx, uid, endpoint, key)
	if err != nil || reserved || rec == nil || !rec.Completed || string(rec.Body) != `{"id":"msg-1"}` {
		t.Fatalf("replay after Complete: rec=%#v reserved=%v err=%v", rec, reserved, err)
	}

	// Lease path: the Lua scripts must resolve the same v1 key.
	const leaseUID, leaseEndpoint, leaseKey = "u1", "POST /v1/callback", "lease-key"
	if _, reserved, _, err := store.ReserveLease(ctx, leaseUID, leaseEndpoint, leaseKey); err != nil || !reserved {
		t.Fatalf("ReserveLease reserved=%v err=%v, want true/nil", reserved, err)
	}
	if !srv.Exists(redisKey(leaseUID, leaseEndpoint, leaseKey)) {
		t.Fatalf("lease did not write the v1 key %q", redisKey(leaseUID, leaseEndpoint, leaseKey))
	}
	_, reserved, second, err := store.ReserveLease(ctx, leaseUID, leaseEndpoint, leaseKey)
	if err != nil || reserved || second != "" {
		t.Fatalf("contended ReserveLease reserved=%v token=%q err=%v, want false/empty/nil", reserved, second, err)
	}
	if err := store.CompleteLease(ctx, leaseUID, leaseEndpoint, leaseKey, "", Record{
		Status: http.StatusCreated, Body: []byte(`{"id":"cb-1"}`),
	}, time.Hour); err != nil {
		t.Fatalf("CompleteLease error = %v", err)
	}

	// Release paths.
	if err := store.Release(ctx, uid, endpoint, key); err != nil {
		t.Fatalf("Release error = %v", err)
	}
	if srv.Exists(redisKey(uid, endpoint, key)) {
		t.Fatalf("Release left the v1 key %q behind", redisKey(uid, endpoint, key))
	}
	if err := store.ReleaseLease(ctx, leaseUID, leaseEndpoint, leaseKey, ""); err != nil {
		t.Fatalf("ReleaseLease error = %v", err)
	}
	if srv.Exists(redisKey(leaseUID, leaseEndpoint, leaseKey)) {
		t.Fatalf("ReleaseLease left the v1 key %q behind", redisKey(leaseUID, leaseEndpoint, leaseKey))
	}
}
