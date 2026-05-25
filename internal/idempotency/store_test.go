//go:build integration

package idempotency_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/idempotency"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestGormStore_ReserveCompleteReplayRelease(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	store := idempotency.NewGormStore(db)
	ctx := context.Background()

	const uid, endpoint, key = "u1", "POST /v1/messages", "k-1"

	// First reserve wins.
	if _, reserved, err := store.Reserve(ctx, uid, endpoint, key); err != nil || !reserved {
		t.Fatalf("first reserve: reserved=%v err=%v", reserved, err)
	}
	// A second reserve sees an in-progress record (not completed).
	rec, reserved, err := store.Reserve(ctx, uid, endpoint, key)
	if err != nil || reserved || rec == nil || rec.Completed {
		t.Fatalf("second reserve: reserved=%v rec=%+v err=%v", reserved, rec, err)
	}

	// Complete it; a later reserve replays the stored response.
	body := []byte(`{"id":"msg-1"}`)
	if err := store.Complete(ctx, uid, endpoint, key, idempotency.Record{Status: 201, ContentType: "application/json", Body: body}, time.Hour); err != nil {
		t.Fatal(err)
	}
	rec, reserved, err = store.Reserve(ctx, uid, endpoint, key)
	if err != nil || reserved || rec == nil || !rec.Completed || rec.Status != 201 || string(rec.Body) != string(body) {
		t.Fatalf("after complete: reserved=%v rec=%+v err=%v", reserved, rec, err)
	}

	// Release lets a fresh reserve win again.
	if err := store.Release(ctx, uid, endpoint, key); err != nil {
		t.Fatal(err)
	}
	if _, reserved, err := store.Reserve(ctx, uid, endpoint, key); err != nil || !reserved {
		t.Fatalf("reserve after release: reserved=%v err=%v", reserved, err)
	}
}
