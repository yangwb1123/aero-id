//go:build integration

package block_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/block"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func startPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return db
}

func TestBlockRepo_BlockAndList(t *testing.T) {
	db := startPostgres(t)
	repo := block.NewRepository(db)
	ctx := context.Background()

	if err := repo.Block(ctx, "u1", "u2"); err != nil {
		t.Fatal(err)
	}

	blocked, err := repo.IsBlocked(ctx, "u1", "u2")
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Fatal("u2 should be blocked by u1")
	}

	list, _, err := repo.ListBlocked(ctx, "u1", domain.Page{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0] != "u2" {
		t.Fatalf("list = %v", list)
	}
}

func TestBlockRepo_Unblock(t *testing.T) {
	db := startPostgres(t)
	repo := block.NewRepository(db)
	ctx := context.Background()

	if err := repo.Block(ctx, "u1", "u2"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Unblock(ctx, "u1", "u2"); err != nil {
		t.Fatal(err)
	}

	blocked, err := repo.IsBlocked(ctx, "u1", "u2")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Fatal("u2 should no longer be blocked")
	}
}

func TestBlockRepo_IdempotentBlock(t *testing.T) {
	db := startPostgres(t)
	repo := block.NewRepository(db)
	ctx := context.Background()

	if err := repo.Block(ctx, "u1", "u2"); err != nil {
		t.Fatal(err)
	}
	// Second block should be a no-op (ON CONFLICT DO NOTHING).
	if err := repo.Block(ctx, "u1", "u2"); err != nil {
		t.Fatal(err)
	}

	list, _, err := repo.ListBlocked(ctx, "u1", domain.Page{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 blocked user, got %d", len(list))
	}
}

func TestBlockRepo_ListPagination(t *testing.T) {
	db := startPostgres(t)
	repo := block.NewRepository(db)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		uid := fmt.Sprintf("u%d", i+1)
		if err := repo.Block(ctx, "blocker", uid); err != nil {
			t.Fatal(err)
		}
	}

	// First page: limit 2.
	list, cursor, err := repo.ListBlocked(ctx, "blocker", domain.Page{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("page size = %d, want 2", len(list))
	}
	if cursor == "" {
		t.Fatal("expected cursor for more pages")
	}

	// Second page.
	list2, cursor2, err := repo.ListBlocked(ctx, "blocker", domain.Page{Limit: 2, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(list2) != 2 {
		t.Fatalf("page size = %d, want 2", len(list2))
	}
	if cursor2 == "" {
		t.Fatal("expected cursor for more pages")
	}

	// Third page: should have 1 remaining.
	list3, cursor3, err := repo.ListBlocked(ctx, "blocker", domain.Page{Limit: 2, Cursor: cursor2})
	if err != nil {
		t.Fatal(err)
	}
	if len(list3) != 1 {
		t.Fatalf("page size = %d, want 1", len(list3))
	}
	if cursor3 != "" {
		t.Fatalf("expected no cursor, got %q", cursor3)
	}
}
