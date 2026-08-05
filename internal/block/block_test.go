package block_test

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/block"
	"github.com/aero/aero-id/internal/domain"
)

type mockBlockRepo struct {
	BlockFn       func(ctx context.Context, blocker, blocked string) error
	UnblockFn     func(ctx context.Context, blocker, blocked string) error
	ListBlockedFn func(ctx context.Context, blocker string, page domain.Page) ([]string, string, error)
	IsBlockedFn   func(ctx context.Context, blocker, blocked string) (bool, error)
}

func (m *mockBlockRepo) Block(ctx context.Context, blocker, blocked string) error {
	if m.BlockFn != nil {
		return m.BlockFn(ctx, blocker, blocked)
	}
	return nil
}

func (m *mockBlockRepo) Unblock(ctx context.Context, blocker, blocked string) error {
	if m.UnblockFn != nil {
		return m.UnblockFn(ctx, blocker, blocked)
	}
	return nil
}

func (m *mockBlockRepo) ListBlocked(ctx context.Context, blocker string, page domain.Page) ([]string, string, error) {
	if m.ListBlockedFn != nil {
		return m.ListBlockedFn(ctx, blocker, page)
	}
	return nil, "", nil
}

func (m *mockBlockRepo) IsBlocked(ctx context.Context, blocker, blocked string) (bool, error) {
	if m.IsBlockedFn != nil {
		return m.IsBlockedFn(ctx, blocker, blocked)
	}
	return false, nil
}

func TestBlock_BlocksUser(t *testing.T) {
	called := false
	repo := &mockBlockRepo{BlockFn: func(_ context.Context, blocker, blocked string) error {
		called = true
		if blocker != "u1" || blocked != "u2" {
			t.Fatalf("block(%q, %q)", blocker, blocked)
		}
		return nil
	}}
	svc := block.NewService(repo)
	ctx := domain.WithActor(context.Background(), "u1")
	if err := svc.Block(ctx, "u2"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Block was not called")
	}
}

func TestBlock_SelfBlockRejected(t *testing.T) {
	svc := block.NewService(&mockBlockRepo{})
	ctx := domain.WithActor(context.Background(), "u1")
	if err := svc.Block(ctx, "u1"); err == nil {
		t.Fatal("expected error for self-block")
	}
}

func TestBlock_UnblockUser(t *testing.T) {
	called := false
	repo := &mockBlockRepo{UnblockFn: func(_ context.Context, blocker, blocked string) error {
		called = true
		return nil
	}}
	svc := block.NewService(repo)
	ctx := domain.WithActor(context.Background(), "u1")
	if err := svc.Unblock(ctx, "u2"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Unblock was not called")
	}
}

func TestBlock_ListBlocked(t *testing.T) {
	repo := &mockBlockRepo{ListBlockedFn: func(_ context.Context, _ string, _ domain.Page) ([]string, string, error) {
		return []string{"x", "y"}, "next", nil
	}}
	svc := block.NewService(repo)
	ctx := domain.WithActor(context.Background(), "u1")
	list, err := svc.ListBlocked(ctx, domain.Page{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.UIDs) != 2 || list.NextCursor != "next" {
		t.Fatalf("uids=%v cursor=%q", list.UIDs, list.NextCursor)
	}
}
