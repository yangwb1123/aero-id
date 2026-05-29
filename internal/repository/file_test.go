//go:build integration

package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func setupFileRepo(t *testing.T) repository.FileRepository {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return repository.NewFileRepository(db)
}

func TestFileRepo_CreateGetListDelete(t *testing.T) {
	ctx := context.Background()
	repo := setupFileRepo(t)

	now := time.Now().UTC().Truncate(time.Microsecond)
	f := &domain.File{
		ID: idgen.NewID(), OwnerUID: "u1", Name: "x.txt", Size: 7,
		MimeType: "text/plain", StorageKey: "u1/x", StorageType: "local",
		Checksum: "abc", Visibility: domain.VisibilityPrivate,
		Meta: map[string]any{"k": "v"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(ctx, f); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "x.txt" || got.Size != 7 || got.Checksum != "abc" {
		t.Fatalf("got %+v", got)
	}
	if got.Meta["k"] != "v" {
		t.Errorf("meta lost: %+v", got.Meta)
	}

	list, _, err := repo.ListByOwner(ctx, "u1", domain.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1, got %d", len(list))
	}

	if err := repo.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, f.ID); err != repository.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestFileRepo_ListPagination(t *testing.T) {
	ctx := context.Background()
	repo := setupFileRepo(t)

	for i := 0; i < 3; i++ {
		now := time.Now().UTC()
		if err := repo.Create(ctx, &domain.File{
			ID: idgen.NewID(), OwnerUID: "u2", Name: "f", Size: 1,
			MimeType: "x", StorageKey: "k", StorageType: "local",
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	page1, cursor1, err := repo.ListByOwner(ctx, "u2", domain.Page{Limit: 2})
	if err != nil || len(page1) != 2 || cursor1 == "" {
		t.Fatalf("page1=%d cursor=%q err=%v", len(page1), cursor1, err)
	}
	page2, cursor2, err := repo.ListByOwner(ctx, "u2", domain.Page{Limit: 2, Cursor: cursor1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || cursor2 != "" {
		t.Fatalf("page2=%d cursor=%q", len(page2), cursor2)
	}
}

func TestFileRepo_UpdateMetadataChangesOnlyProvidedColumns(t *testing.T) {
	ctx := context.Background()
	repo := setupFileRepo(t)

	now := time.Now().UTC().Truncate(time.Microsecond)
	f := &domain.File{
		ID: idgen.NewID(), OwnerUID: "u1", Name: "old.txt", Size: 7,
		MimeType: "text/plain", StorageKey: "u1/x", StorageType: "local",
		Checksum: "abc", Visibility: domain.VisibilityPrivate,
		Meta: map[string]any{"k": "v"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(ctx, f); err != nil {
		t.Fatal(err)
	}

	later := now.Add(time.Hour)
	err := repo.UpdateMetadata(ctx, f.ID, map[string]any{
		"name":       "new.txt",
		"visibility": int16(domain.VisibilityPublic),
		"meta":       map[string]any{"k": "v", "added": "1"},
		"updated_at": later,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := repo.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "new.txt" {
		t.Errorf("name = %q, want new.txt", got.Name)
	}
	if got.Visibility != domain.VisibilityPublic {
		t.Errorf("visibility = %d, want public", got.Visibility)
	}
	if got.Meta["k"] != "v" || got.Meta["added"] != "1" {
		t.Errorf("meta = %+v, want merged", got.Meta)
	}
	// Untouched columns must persist unchanged.
	if got.Size != 7 || got.Checksum != "abc" || got.StorageKey != "u1/x" {
		t.Errorf("untouched columns changed: %+v", got)
	}
	if !got.UpdatedAt.Equal(later) {
		t.Errorf("updated_at = %v, want %v", got.UpdatedAt, later)
	}

	// Updating a missing row reports ErrNotFound.
	if err := repo.UpdateMetadata(ctx, idgen.NewID(), map[string]any{"name": "z"}); err != repository.ErrNotFound {
		t.Errorf("update of missing row = %v, want ErrNotFound", err)
	}

	// Soft-deleted rows are out of scope -> ErrNotFound.
	if err := repo.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateMetadata(ctx, f.ID, map[string]any{"name": "zz"}); err != repository.ErrNotFound {
		t.Errorf("update of soft-deleted row = %v, want ErrNotFound", err)
	}
}

func TestFileRepo_FindByChecksumAndCount(t *testing.T) {
	ctx := context.Background()
	repo := setupFileRepo(t)
	now := time.Now().UTC()

	// Two live files for u1 share one storage key (a deduped pair).
	shared := "u1/shared-blob"
	a := &domain.File{ID: idgen.NewID(), OwnerUID: "u1", Name: "a", Size: 3, MimeType: "x", StorageKey: shared, StorageType: "local", Checksum: "deadbeef", CreatedAt: now, UpdatedAt: now}
	b := &domain.File{ID: idgen.NewID(), OwnerUID: "u1", Name: "b", Size: 3, MimeType: "x", StorageKey: shared, StorageType: "local", Checksum: "deadbeef", CreatedAt: now, UpdatedAt: now}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	// FindByChecksum returns a live match for the owner...
	got, err := repo.FindByChecksum(ctx, "u1", "deadbeef")
	if err != nil || got.Checksum != "deadbeef" {
		t.Fatalf("FindByChecksum got=%+v err=%v", got, err)
	}
	// ...but not for a different owner (dedup is per-owner).
	if _, err := repo.FindByChecksum(ctx, "u2", "deadbeef"); err != repository.ErrNotFound {
		t.Errorf("cross-owner FindByChecksum = %v, want ErrNotFound", err)
	}

	// Two live rows reference the shared blob; deleting them drops the count toward 0.
	if n, err := repo.CountByStorageKey(ctx, shared); err != nil || n != 2 {
		t.Fatalf("count=%d err=%v, want 2", n, err)
	}
	if err := repo.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.CountByStorageKey(ctx, shared); err != nil || n != 1 {
		t.Fatalf("count after one delete=%d err=%v, want 1 (blob must be kept)", n, err)
	}
	if err := repo.Delete(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.CountByStorageKey(ctx, shared); err != nil || n != 0 {
		t.Fatalf("count after final delete=%d err=%v, want 0 (blob may now be removed)", n, err)
	}
}
