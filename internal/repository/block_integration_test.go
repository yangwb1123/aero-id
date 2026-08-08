//go:build integration

package repository_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func setupBlockRepo(t *testing.T) repository.BlockRepository {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return repository.NewBlockRepository(db, repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "block-integration-audit-key-0123456789abcdef"}))
}

func TestBlockRepo_BlockIsBlockedUnblock(t *testing.T) {
	ctx := context.Background()
	blocks := setupBlockRepo(t)

	// Block is idempotent: blocking the same pair twice yields one row.
	if err := blocks.Block(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := blocks.Block(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}

	// Directional: a->b is blocked, b->a is not.
	if ok, err := blocks.IsBlocked(ctx, "a", "b"); err != nil || !ok {
		t.Fatalf("IsBlocked(a,b) = %v err=%v, want true", ok, err)
	}
	if ok, err := blocks.IsBlocked(ctx, "b", "a"); err != nil || ok {
		t.Fatalf("IsBlocked(b,a) = %v err=%v, want false", ok, err)
	}

	// Unblock removes the record; unblocking a non-existent pair is a no-op.
	if err := blocks.Unblock(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := blocks.Unblock(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if ok, err := blocks.IsBlocked(ctx, "a", "b"); err != nil || ok {
		t.Fatalf("IsBlocked(a,b) after unblock = %v err=%v, want false", ok, err)
	}
}

func TestBlockRepo_ListBlocked(t *testing.T) {
	ctx := context.Background()
	blocks := setupBlockRepo(t)

	if err := blocks.Block(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := blocks.Block(ctx, "a", "c"); err != nil {
		t.Fatal(err)
	}
	// A block by a different blocker must not appear in a's list.
	if err := blocks.Block(ctx, "x", "y"); err != nil {
		t.Fatal(err)
	}

	got, next, err := blocks.ListBlocked(ctx, "a", domain.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Errorf("next cursor = %q, want empty", next)
	}
	if len(got) != 2 {
		t.Fatalf("blocked = %v, want 2 entries", got)
	}
	seen := map[string]bool{}
	for _, u := range got {
		seen[u] = true
	}
	if !seen["b"] || !seen["c"] {
		t.Fatalf("blocked = %v, want b and c", got)
	}
}
