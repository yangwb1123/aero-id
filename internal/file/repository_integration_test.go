//go:build integration

package file_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/file"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func startPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return db
}

func TestFileRepo_CreateAndGet(t *testing.T) {
	db := startPostgres(t)
	repo := file.NewRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	f := &file.File{
		ID:          idgen.NewID(),
		OwnerUID:    "alice",
		Name:        "test.txt",
		Size:        1024,
		MimeType:    "text/plain",
		StorageKey:  "alice/2026/06/uuid",
		StorageType: "local",
		Visibility:  domain.VisibilityPublic,
		Checksum:    "abc123",
		Meta:        map[string]any{"key": "value"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := repo.Create(ctx, f); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "test.txt" {
		t.Errorf("name = %s", got.Name)
	}
	if got.OwnerUID != "alice" {
		t.Errorf("owner = %s", got.OwnerUID)
	}
}

func TestFileRepo_Delete(t *testing.T) {
	db := startPostgres(t)
	repo := file.NewRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	f := &file.File{
		ID:          idgen.NewID(),
		OwnerUID:    "alice",
		Name:        "delete-me.txt",
		Size:        100,
		MimeType:    "text/plain",
		StorageKey:  "alice/2026/06/del",
		StorageType: "local",
		Visibility:  domain.VisibilityPrivate,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := repo.Create(ctx, f); err != nil {
		t.Fatal(err)
	}

	if err := repo.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}

	_, err := repo.Get(ctx, f.ID)
	if err == nil {
		t.Fatal("expected error for deleted file")
	}
}

func TestFileRepo_ListByOwner(t *testing.T) {
	db := startPostgres(t)
	repo := file.NewRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		f := &file.File{
			ID:          idgen.NewID(),
			OwnerUID:    "alice",
			Name:        fmt.Sprintf("file-%d.txt", i),
			Size:        int64(100 + i),
			MimeType:    "text/plain",
			StorageKey:  fmt.Sprintf("alice/2026/06/f%d", i),
			StorageType: "local",
			Visibility:  domain.VisibilityPublic,
			CreatedAt:   now.Add(time.Duration(i) * time.Second),
			UpdatedAt:   now.Add(time.Duration(i) * time.Second),
		}
		if err := repo.Create(ctx, f); err != nil {
			t.Fatal(err)
		}
	}

	list, _, err := repo.ListByOwner(ctx, "alice", domain.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 5 {
		t.Fatalf("expected 5 files, got %d", len(list))
	}
}

func TestFileRepo_ListPagination(t *testing.T) {
	db := startPostgres(t)
	repo := file.NewRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		f := &file.File{
			ID:          idgen.NewID(),
			OwnerUID:    "alice",
			Name:        fmt.Sprintf("file-%d.txt", i),
			Size:        int64(100 + i),
			MimeType:    "text/plain",
			StorageKey:  fmt.Sprintf("alice/2026/06/p%d", i),
			StorageType: "local",
			Visibility:  domain.VisibilityPublic,
			CreatedAt:   now.Add(time.Duration(i) * time.Second),
			UpdatedAt:   now.Add(time.Duration(i) * time.Second),
		}
		if err := repo.Create(ctx, f); err != nil {
			t.Fatal(err)
		}
	}

	list, cursor, err := repo.ListByOwner(ctx, "alice", domain.Page{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("page size = %d, want 2", len(list))
	}
	if cursor == "" {
		t.Fatal("expected cursor for more pages")
	}

	list2, cursor2, err := repo.ListByOwner(ctx, "alice", domain.Page{Limit: 2, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(list2) != 2 {
		t.Fatalf("page size = %d, want 2", len(list2))
	}
	if cursor2 == "" {
		t.Fatal("expected cursor for more pages")
	}
}

func TestFileRepo_FindByChecksum(t *testing.T) {
	db := startPostgres(t)
	repo := file.NewRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	f := &file.File{
		ID:          idgen.NewID(),
		OwnerUID:    "alice",
		Name:        "dup.txt",
		Size:        500,
		MimeType:    "text/plain",
		StorageKey:  "alice/2026/06/dup",
		StorageType: "local",
		Visibility:  domain.VisibilityPrivate,
		Checksum:    "sha256-abc123",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := repo.Create(ctx, f); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindByChecksum(ctx, "alice", "sha256-abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != f.ID {
		t.Errorf("got ID %s, want %s", got.ID, f.ID)
	}
}

func TestFileRepo_UpdateMetadata(t *testing.T) {
	db := startPostgres(t)
	repo := file.NewRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	f := &file.File{
		ID:          idgen.NewID(),
		OwnerUID:    "alice",
		Name:        "update-me.txt",
		Size:        100,
		MimeType:    "text/plain",
		StorageKey:  "alice/2026/06/upd",
		StorageType: "local",
		Visibility:  domain.VisibilityPrivate,
		Meta:        map[string]any{},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := repo.Create(ctx, f); err != nil {
		t.Fatal(err)
	}

	fields := map[string]any{"meta": map[string]any{"updated": true}}
	if err := repo.UpdateMetadata(ctx, f.ID, fields); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got.Meta["updated"]; !ok || v != true {
		t.Errorf("meta updated = %v", got.Meta)
	}
}
