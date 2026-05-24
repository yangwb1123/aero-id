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
		_ = repo.Create(ctx, &domain.File{
			ID: idgen.NewID(), OwnerUID: "u2", Name: "f", Size: 1,
			MimeType: "x", StorageKey: "k", StorageType: "local",
			CreatedAt: now, UpdatedAt: now,
		})
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
