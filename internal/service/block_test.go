package service_test

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
)

func TestBlockService_BlockSelfBadRequest(t *testing.T) {
	svc := service.NewBlockService(&mocks.BlockRepo{})
	ctx := service.WithActor(context.Background(), "a")
	err := svc.Block(ctx, "a")
	assertAppCode(t, err, 400)
}

func TestBlockService_BlockBlankUIDBadRequest(t *testing.T) {
	svc := service.NewBlockService(&mocks.BlockRepo{})
	ctx := service.WithActor(context.Background(), "a")
	assertAppCode(t, svc.Block(ctx, ""), 400)
	assertAppCode(t, svc.Unblock(ctx, ""), 400)
}

func TestBlockService_BlockRequiresActor(t *testing.T) {
	svc := service.NewBlockService(&mocks.BlockRepo{})
	if err := svc.Block(context.Background(), "b"); err == nil {
		t.Fatal("expected unauthorized without actor")
	}
}

func TestBlockService_BlockHappyPath(t *testing.T) {
	var gotBlocker, gotBlocked string
	repo := &mocks.BlockRepo{BlockFn: func(_ context.Context, blocker, blocked string) error {
		gotBlocker, gotBlocked = blocker, blocked
		return nil
	}}
	svc := service.NewBlockService(repo)
	ctx := service.WithActor(context.Background(), "a")
	if err := svc.Block(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if gotBlocker != "a" || gotBlocked != "b" {
		t.Fatalf("repo args = (blocker=%q, blocked=%q), want (a, b)", gotBlocker, gotBlocked)
	}
}

func TestBlockService_UnblockHappyPath(t *testing.T) {
	var gotBlocker, gotBlocked string
	repo := &mocks.BlockRepo{UnblockFn: func(_ context.Context, blocker, blocked string) error {
		gotBlocker, gotBlocked = blocker, blocked
		return nil
	}}
	svc := service.NewBlockService(repo)
	ctx := service.WithActor(context.Background(), "a")
	if err := svc.Unblock(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if gotBlocker != "a" || gotBlocked != "b" {
		t.Fatalf("repo args = (blocker=%q, blocked=%q), want (a, b)", gotBlocker, gotBlocked)
	}
}

// TestBlockService_BlockEmitsAuditEvent (B2-1): with an auditor wired, the
// in-tx BlockWithAudit path carries the fact with adapter-parity fields plus
// target_uid.
func TestBlockService_BlockEmitsAuditEvent(t *testing.T) {
	var captured *domain.AuditFact
	calls := 0
	repo := &mocks.BlockRepo{BlockWithAuditFn: func(_ context.Context, blocker, blocked string, fact *domain.AuditFact) error {
		captured = fact
		calls++
		return nil
	}}
	svc := service.NewBlockService(repo, service.WithBlockAuditLogger(&mocks.Auditor{}))
	ctx := service.WithActor(context.Background(), "a")
	if err := svc.Block(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("BlockWithAudit called %d times, want 1", calls)
	}
	if captured.Action != domain.AuditUserBlock {
		t.Fatalf("action = %q, want %q", captured.Action, domain.AuditUserBlock)
	}
	if captured.ActorUID != "a" {
		t.Fatalf("actor = %q, want a", captured.ActorUID)
	}
	if captured.ResourceType != "user" || captured.ResourceID != "b" {
		t.Fatalf("resource wrong: type=%q id=%q", captured.ResourceType, captured.ResourceID)
	}
	if captured.Payload["target_uid"] != "b" {
		t.Fatalf("payload target_uid = %v, want b (adapter dropped it before B2-1)", captured.Payload["target_uid"])
	}
}

// TestBlockService_UnblockEmitsAuditEvent (B2-1): same fact-level assertion
// for the unblock path.
func TestBlockService_UnblockEmitsAuditEvent(t *testing.T) {
	var captured *domain.AuditFact
	calls := 0
	repo := &mocks.BlockRepo{UnblockWithAuditFn: func(_ context.Context, blocker, blocked string, fact *domain.AuditFact) error {
		captured = fact
		calls++
		return nil
	}}
	svc := service.NewBlockService(repo, service.WithBlockAuditLogger(&mocks.Auditor{}))
	ctx := service.WithActor(context.Background(), "a")
	if err := svc.Unblock(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("UnblockWithAudit called %d times, want 1", calls)
	}
	if captured.Action != domain.AuditUserUnblock {
		t.Fatalf("action = %q, want %q", captured.Action, domain.AuditUserUnblock)
	}
	if captured.ActorUID != "a" {
		t.Fatalf("actor = %q, want a", captured.ActorUID)
	}
	if captured.ResourceType != "user" || captured.ResourceID != "b" {
		t.Fatalf("resource wrong: type=%q id=%q", captured.ResourceType, captured.ResourceID)
	}
	if captured.Payload["target_uid"] != "b" {
		t.Fatalf("payload target_uid = %v, want b", captured.Payload["target_uid"])
	}
}

// TestBlockAuditFailureFailsClosed (B2-1): the in-tx BlockWithAudit path
// fails closed — the request fails and the block never commits.
func TestBlockAuditFailureFailsClosed(t *testing.T) {
	repo := &mocks.BlockRepo{BlockWithAuditFn: func(context.Context, string, string, *domain.AuditFact) error {
		return stderrors.New("ledger down")
	}}
	svc := service.NewBlockService(repo, service.WithBlockAuditLogger(&mocks.Auditor{}))
	ctx := service.WithActor(context.Background(), "a")

	err := svc.Block(ctx, "b")
	assertAppCode(t, err, 500)
}

// TestUnblockAuditFailureFailsClosed (B2-1): same fail-closed contract for unblock.
func TestUnblockAuditFailureFailsClosed(t *testing.T) {
	repo := &mocks.BlockRepo{UnblockWithAuditFn: func(context.Context, string, string, *domain.AuditFact) error {
		return stderrors.New("ledger down")
	}}
	svc := service.NewBlockService(repo, service.WithBlockAuditLogger(&mocks.Auditor{}))
	ctx := service.WithActor(context.Background(), "a")

	err := svc.Unblock(ctx, "b")
	assertAppCode(t, err, 500)
}

func TestBlockService_ListBlockedHappyPath(t *testing.T) {
	repo := &mocks.BlockRepo{ListBlockedFn: func(_ context.Context, blocker string, _ domain.Page) ([]string, string, error) {
		if blocker != "a" {
			t.Fatalf("blocker = %q, want a", blocker)
		}
		return []string{"b", "c"}, "cur", nil
	}}
	svc := service.NewBlockService(repo)
	ctx := service.WithActor(context.Background(), "a")
	list, err := svc.ListBlocked(ctx, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.UIDs) != 2 || list.UIDs[0] != "b" || list.UIDs[1] != "c" {
		t.Fatalf("uids = %v, want [b c]", list.UIDs)
	}
	if list.NextCursor != "cur" {
		t.Fatalf("cursor = %q, want cur", list.NextCursor)
	}
}
