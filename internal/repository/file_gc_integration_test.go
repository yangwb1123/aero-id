//go:build integration

package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// setupFileRepo is shared with file_test.go (same integration build + package).
//
// TestFileRepo_GCQueries exercises the reconciliation queries: soft-deleted rows past a
// retention window are listed and hard-deleted, while the storage-key reference counts
// (live-only and any-including-soft-deleted) drive refcount-safe blob deletion.
func TestFileRepo_GCQueries(t *testing.T) {
	ctx := context.Background()
	files := setupFileRepo(t)

	now := time.Now().UTC()
	mk := func(name, key string) *domain.File {
		f := &domain.File{
			ID: idgen.NewID(), OwnerUID: "owner", Name: name, Size: 1,
			MimeType: "text/plain", StorageKey: key, StorageType: "local",
			Visibility: domain.VisibilityPrivate, CreatedAt: now, UpdatedAt: now,
		}
		if err := files.Create(ctx, f); err != nil {
			t.Fatal(err)
		}
		return f
	}

	// Two rows share one blob (dedup); a third has its own blob.
	shared1 := mk("a", "owner/shared")
	shared2 := mk("b", "owner/shared")
	solo := mk("c", "owner/solo")

	// Soft-delete shared1 and solo (Delete is a soft delete).
	if err := files.Delete(ctx, shared1.ID); err != nil {
		t.Fatal(err)
	}
	if err := files.Delete(ctx, solo.ID); err != nil {
		t.Fatal(err)
	}

	// Nothing is older than a 1h retention yet.
	old, err := files.ListSoftDeletedBefore(ctx, now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 0 {
		t.Fatalf("ListSoftDeletedBefore(1h ago) = %d, want 0", len(old))
	}

	// Everything soft-deleted is older than "now + 1h".
	due, err := files.ListSoftDeletedBefore(ctx, now.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("ListSoftDeletedBefore(future) = %d, want 2", len(due))
	}

	// Live count excludes soft-deleted rows; "any" count includes them.
	if n, err := files.CountByStorageKey(ctx, "owner/shared"); err != nil || n != 1 {
		t.Fatalf("CountByStorageKey(shared) = %d err=%v, want 1 (only shared2 live)", n, err)
	}
	if n, err := files.CountAnyByStorageKey(ctx, "owner/shared"); err != nil || n != 2 {
		t.Fatalf("CountAnyByStorageKey(shared) = %d err=%v, want 2", n, err)
	}
	if n, err := files.CountAnyByStorageKey(ctx, "owner/solo"); err != nil || n != 1 {
		t.Fatalf("CountAnyByStorageKey(solo) = %d err=%v, want 1", n, err)
	}

	// Hard-delete the soft-deleted rows; afterwards the shared blob is referenced only
	// by the still-live shared2, while solo's blob has no references at all.
	if err := files.HardDelete(ctx, []domain.ID{shared1.ID, solo.ID}); err != nil {
		t.Fatal(err)
	}
	if n, err := files.CountAnyByStorageKey(ctx, "owner/shared"); err != nil || n != 1 {
		t.Fatalf("after purge CountAnyByStorageKey(shared) = %d err=%v, want 1 (shared2)", n, err)
	}
	if n, err := files.CountAnyByStorageKey(ctx, "owner/solo"); err != nil || n != 0 {
		t.Fatalf("after purge CountAnyByStorageKey(solo) = %d err=%v, want 0 (orphan blob)", n, err)
	}

	// shared2 remains live and fetchable.
	if _, err := files.Get(ctx, shared2.ID); err != nil {
		t.Fatalf("shared2 should remain live: %v", err)
	}

	// Purged rows are truly gone (not even soft-deleted anymore).
	gone, err := files.ListSoftDeletedBefore(ctx, now.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Fatalf("ListSoftDeletedBefore after purge = %d, want 0", len(gone))
	}
}
